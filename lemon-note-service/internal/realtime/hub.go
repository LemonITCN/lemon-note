// Package realtime 通过 WebSocket 把数据变化实时推给浏览器：块变化、页面树变化、通知。
// 每条消息发送前都按连接用户的当前权限过滤。
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/coder/websocket"
)

// Hub 管理全部连接。
type Hub struct {
	cat   *catalog.Catalog
	log   *slog.Logger
	mu    sync.RWMutex
	conns map[string]*Conn
}

// Conn 是一个浏览器连接。
type Conn struct {
	ID     string
	UserID string
	hub    *Hub
	ws     *websocket.Conn
	send   chan []byte
	mu     sync.Mutex
	subs   map[string]bool // page:<id>
	// OnMessage 处理客户端发来的其他消息（例如智能体浏览器工具的回执）
	closed chan struct{}
}

func New(cat *catalog.Catalog, st *store.Store, log *slog.Logger) *Hub {
	h := &Hub{cat: cat, log: log, conns: map[string]*Conn{}}
	st.Subscribe(h.onCommit)
	return h
}

// Message 是推给浏览器的消息。
type Message struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type blockEvent struct {
	Op       string          `json:"op"`
	ID       string          `json:"id"`
	Type     string          `json:"type,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Position string          `json:"position,omitempty"`
	Version  int64           `json:"version,omitempty"`
}

func (h *Hub) onCommit(c store.Commit) {
	blocksByPage := map[string][]blockEvent{}
	var pageIDs []string
	revs := map[string]int64{}
	for _, op := range c.Ops {
		switch op.T {
		case "blocks":
			pid, _ := op.Row["page_id"].(string)
			if pid == "" {
				continue
			}
			if op.O == "del" {
				blocksByPage[pid] = append(blocksByPage[pid], blockEvent{Op: "del", ID: op.ID})
				continue
			}
			ev := blockEvent{Op: "put", ID: op.ID}
			ev.Type, _ = op.Row["type"].(string)
			if s, ok := op.Row["data"].(string); ok {
				ev.Data = json.RawMessage(s)
			}
			ev.Position, _ = op.Row["position"].(string)
			ev.Version = toInt(op.Row["version"])
			blocksByPage[pid] = append(blocksByPage[pid], ev)
		case "pages":
			pageIDs = append(pageIDs, op.ID)
			if op.O == "put" {
				revs[op.ID] = toInt(op.Row["content_rev"])
			}
		}
	}
	for pid, evs := range blocksByPage {
		msg, _ := json.Marshal(Message{Type: "blocks", Data: map[string]any{"page_id": pid, "rev": revs[pid], "ops": evs, "origin": c.Origin, "actor": c.Actor}})
		h.broadcast(func(conn *Conn) bool {
			return conn.ID != c.Origin && conn.subscribed("page:"+pid) && h.cat.CanRead(conn.UserID, pid)
		}, msg)
	}
	for _, pid := range pageIDs {
		meta, ok := h.cat.Page(pid)
		var msg []byte
		if ok {
			msg, _ = json.Marshal(Message{Type: "page", Data: map[string]any{"meta": meta, "origin": c.Origin}})
		} else {
			msg, _ = json.Marshal(Message{Type: "page_removed", Data: map[string]any{"id": pid}})
		}
		h.broadcast(func(conn *Conn) bool {
			if !ok {
				return true
			}
			return h.cat.CanRead(conn.UserID, pid)
		}, msg)
	}
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func (h *Hub) broadcast(filter func(*Conn) bool, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.conns {
		if filter(c) {
			c.enqueue(msg)
		}
	}
}

// SendToUser 给某个用户的全部连接发消息。
func (h *Hub) SendToUser(userID string, m Message) {
	b, _ := json.Marshal(m)
	h.broadcast(func(c *Conn) bool { return c.UserID == userID }, b)
}

// SendToConn 给指定连接发消息，返回是否找到连接。
func (h *Hub) SendToConn(connID string, m Message) bool {
	b, _ := json.Marshal(m)
	h.mu.RLock()
	c := h.conns[connID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	c.enqueue(b)
	return true
}

// UserConns 返回用户当前的连接 id。
func (h *Hub) UserConns(userID string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	for id, c := range h.conns {
		if c.UserID == userID {
			out = append(out, id)
		}
	}
	return out
}

// Count 返回连接数。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

func (c *Conn) subscribed(ch string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[ch]
}

func (c *Conn) enqueue(b []byte) {
	select {
	case c.send <- b:
	default:
		// 发送队列满：客户端太慢，断开让它重连后全量刷新
		c.ws.Close(websocket.StatusPolicyViolation, "slow consumer")
	}
}

// Handler 接受 WebSocket 连接。userID 由上层认证后传入；onClientMessage 处理未知类型的客户端消息。
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID string, onClientMessage func(conn *Conn, typ string, data json.RawMessage)) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	c := &Conn{ID: ids.New(), UserID: userID, hub: h, ws: ws, send: make(chan []byte, 256), subs: map[string]bool{}, closed: make(chan struct{})}
	h.mu.Lock()
	h.conns[c.ID] = c
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.conns, c.ID)
		h.mu.Unlock()
		close(c.closed)
		ws.CloseNow()
	}()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	hello, _ := json.Marshal(Message{Type: "hello", Data: map[string]any{"conn_id": c.ID}})
	c.send <- hello
	go func() {
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-c.send:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Type    string          `json:"type"`
			Channel string          `json:"channel"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "sub":
			if strings.HasPrefix(m.Channel, "page:") && h.cat.CanRead(userID, strings.TrimPrefix(m.Channel, "page:")) {
				c.mu.Lock()
				if len(c.subs) < 200 {
					c.subs[m.Channel] = true
				}
				c.mu.Unlock()
			}
		case "unsub":
			c.mu.Lock()
			delete(c.subs, m.Channel)
			c.mu.Unlock()
		case "ping":
			pong, _ := json.Marshal(Message{Type: "pong"})
			c.enqueue(pong)
		default:
			if onClientMessage != nil {
				onClientMessage(c, m.Type, m.Data)
			}
		}
	}
}
