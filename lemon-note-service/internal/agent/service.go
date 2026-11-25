package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/realtime"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
)

// Service 是智能体服务。
type Service struct {
	st       *store.Store
	cat      *catalog.Catalog
	pages    *pages.Service
	settings *settings.Service
	box      *crypt.Box
	hub      *realtime.Hub
	log      *slog.Logger
	registry *tools.Registry
	toolSvc  *tools.Services

	mu       sync.Mutex
	runs     map[string]*Run // runID → run
	byConv   map[string]*Run // 会话 → 正在执行的 run
	reads    map[string]*tools.ReadTracker
	provKey  string
	provider *llm.OpenCodeGo
	clients  sync.Map // callID → chan json.RawMessage
}

func New(st *store.Store, cat *catalog.Catalog, pg *pages.Service, set *settings.Service, box *crypt.Box, hub *realtime.Hub, log *slog.Logger, toolSvc *tools.Services) *Service {
	return &Service{st: st, cat: cat, pages: pg, settings: set, box: box, hub: hub, log: log, registry: tools.Default, toolSvc: toolSvc,
		runs: map[string]*Run{}, byConv: map[string]*Run{}, reads: map[string]*tools.ReadTracker{}}
}

var ErrNoProvider = errors.New("管理员还没有配置模型供应商（设置 → 智能体与模型）")

// ProviderConfig 是供应商设置。
type ProviderConfig struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	HasKey  bool   `json:"has_key"`
}

// Provider 返回当前可用的供应商。
func (s *Service) Provider(ctx context.Context) (*llm.OpenCodeGo, error) {
	var p model.AIProvider
	if err := s.st.Get(ctx, &p, "SELECT * FROM ai_providers WHERE kind = ? AND enabled = 1 ORDER BY created_at LIMIT 1", "opencode-go"); err != nil {
		return nil, ErrNoProvider
	}
	key, err := s.box.Open(p.APIKey)
	if err != nil || key == "" {
		return nil, ErrNoProvider
	}
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:8])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nil || s.provKey != h {
		s.provider = &llm.OpenCodeGo{APIKey: key, ModelsURL: p.BaseURL}
		s.provKey = h
	}
	return s.provider, nil
}

// SaveProvider 保存 OpenCode Go 密钥（空串表示不改密钥）。
func (s *Service) SaveProvider(ctx context.Context, actor, apiKey, modelsURL string, enabled bool) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var p model.AIProvider
		now := model.Now()
		if err := tx.Get(&p, "SELECT * FROM ai_providers WHERE kind = ? ORDER BY created_at LIMIT 1", "opencode-go"); err != nil {
			p = model.AIProvider{ID: ids.New(), Kind: "opencode-go", Name: "OpenCode Go", Settings: "{}", CreatedAt: now}
		}
		if apiKey != "" {
			p.APIKey = s.box.Seal(apiKey)
		}
		p.BaseURL = modelsURL
		p.Enabled = enabled
		p.UpdatedAt = now
		return tx.Put("ai_providers", p)
	})
	s.mu.Lock()
	s.provider = nil
	s.mu.Unlock()
	return err
}

// ProviderStatus 返回供应商配置状态（不含密钥）。
func (s *Service) ProviderStatus(ctx context.Context) ProviderConfig {
	var p model.AIProvider
	if err := s.st.Get(ctx, &p, "SELECT * FROM ai_providers WHERE kind = ? ORDER BY created_at LIMIT 1", "opencode-go"); err != nil {
		return ProviderConfig{Kind: "opencode-go"}
	}
	return ProviderConfig{ID: p.ID, Kind: p.Kind, Enabled: p.Enabled, HasKey: p.APIKey != ""}
}

// DefaultModel 选默认模型：管理员设置的，否则套餐额度最大的推荐模型。
func (s *Service) DefaultModel(ctx context.Context, ms []llm.Model) string {
	want := s.settings.System(ctx).AgentDefaultModel
	for _, m := range ms {
		if m.ID == want && m.Type == "chat" {
			return want
		}
	}
	best := ""
	bestQ := -1
	for _, m := range ms {
		if m.Type != "chat" {
			continue
		}
		q := m.Quota
		if m.Featured {
			q += 1000
		}
		if q > bestQ {
			best, bestQ = m.ID, q
		}
	}
	return best
}

// ---------- 运行 ----------

// Run 是一轮执行。
type Run struct {
	ID       string
	ConvID   string
	UserID   string
	OriginWS string
	ctx      context.Context
	cancel   context.CancelFunc

	mu      sync.Mutex
	events  []Event
	subs    map[chan struct{}]struct{}
	state   string // running | waiting | done | error | stopped
	errMsg  string
	pending map[string]chan Reply
	steer   []string
	started time.Time

	stepStart   int                       // 当前这一步 assistant_start 事件的序号（断线续传用）
	pendingData map[string]map[string]any // 等待用户回应的审批 / 提问

	checkpoints map[string]string // pageID → 修改前的版本 id
	created     []string
	trashed     []string
	ckptMu      sync.Mutex
}

// Reply 是用户对审批 / 提问的回应。
type Reply struct {
	Approve bool            `json:"approve"`
	Always  bool            `json:"always"`
	Answer  string          `json:"answer"`
	Data    json.RawMessage `json:"data"`
}

func (r *Run) emit(typ string, data any) {
	r.mu.Lock()
	ev := Event{Seq: len(r.events) + 1, Type: typ, Data: data}
	r.events = append(r.events, ev)
	if typ == "assistant_start" {
		r.stepStart = ev.Seq
	}
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	r.mu.Unlock()
}

// Since 返回序号之后的事件与当前状态。
func (r *Run) Since(seq int) ([]Event, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq < 0 {
		seq = 0
	}
	if seq > len(r.events) {
		seq = len(r.events)
	}
	out := append([]Event(nil), r.events[seq:]...)
	return out, r.state
}

// Subscribe 订阅新事件通知。
func (r *Run) Subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

func (r *Run) setState(st, msg string) {
	r.mu.Lock()
	r.state, r.errMsg = st, msg
	r.mu.Unlock()
}

// State 返回状态。
func (r *Run) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Resume 返回断线续传信息：从哪个序号之后重放、待处理的交互。
func (r *Run) Resume() (int, []map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	from := len(r.events)
	if r.stepStart > 0 {
		from = r.stepStart - 1
	}
	var pend []map[string]any
	for _, d := range r.pendingData {
		pend = append(pend, d)
	}
	return from, pend
}

// GetRun 取运行（只能取自己的）。
func (s *Service) GetRun(userID, runID string) (*Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok || r.UserID != userID {
		return nil, false
	}
	return r, true
}

// ActiveRun 返回会话正在执行的运行。
func (s *Service) ActiveRun(convID string) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byConv[convID]
}

func (s *Service) readTracker(convID string) *tools.ReadTracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.reads[convID]
	if t == nil {
		t = tools.NewReadTracker()
		s.reads[convID] = t
	}
	return t
}

// SendInput 是用户发来的一条消息。
type SendInput struct {
	Text        string          `json:"text"`
	Refs        []Ref           `json:"refs"`
	Images      []ImageInput    `json:"images"`
	Model       string          `json:"model"`
	Effort      string          `json:"effort"`
	Mode        string          `json:"mode"`
	CurrentPage string          `json:"current_page"`
	Meta        json.RawMessage `json:"meta"`
}

// ImageInput 是聊天里附带的图片（附件 id）。
type ImageInput struct {
	FileID string `json:"file_id"`
}

// Send 发送用户消息并开始执行；会话正在执行时作为插话排队。
func (s *Service) Send(ctx context.Context, user *model.User, convID, originWS string, in SendInput) (*Run, *Message, error) {
	conv, err := s.getConv(ctx, user.ID, convID)
	if err != nil {
		return nil, nil, err
	}
	if in.Model != "" {
		conv.Cfg.Model = in.Model
	}
	if in.Effort != "" {
		conv.Cfg.Effort = in.Effort
	}
	switch in.Mode {
	case ModePlan, ModeAskAll, ModeAskDestructive, ModeAuto:
		conv.Cfg.Mode = in.Mode
	}
	env := s.toolEnv(ctx, user, conv, nil)
	parts, err := s.buildUserParts(env, in)
	if err != nil {
		return nil, nil, err
	}
	msg, err := s.appendMessage(ctx, conv, "user", parts, map[string]any{"current_page": in.CurrentPage})
	if err != nil {
		return nil, nil, err
	}
	if conv.Title == "" {
		conv.Title = autoTitle(in.Text)
	}
	if err := s.saveConv(ctx, conv); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	if r := s.byConv[conv.ID]; r != nil && (r.State() == "running" || r.State() == "waiting") {
		s.mu.Unlock()
		r.mu.Lock()
		r.steer = append(r.steer, msg.ID)
		r.mu.Unlock()
		r.emit("user_message", msg)
		return r, msg, nil
	}
	rctx, cancel := context.WithCancel(context.Background())
	run := &Run{ID: ids.New(), ConvID: conv.ID, UserID: user.ID, OriginWS: originWS, ctx: rctx, cancel: cancel, subs: map[chan struct{}]struct{}{},
		state: "running", pending: map[string]chan Reply{}, started: time.Now(), checkpoints: map[string]string{}}
	s.runs[run.ID] = run
	s.byConv[conv.ID] = run
	s.mu.Unlock()
	run.emit("run_start", map[string]any{"run_id": run.ID, "conversation_id": conv.ID})
	run.emit("user_message", msg)
	go s.execute(run, user)
	return run, msg, nil
}

// Stop 停止运行。
func (s *Service) Stop(userID, runID string) error {
	r, ok := s.GetRun(userID, runID)
	if !ok {
		return errors.New("运行不存在")
	}
	r.cancel()
	return nil
}

// Respond 回应审批或提问。
func (s *Service) Respond(userID, runID, interactionID string, rep Reply) error {
	r, ok := s.GetRun(userID, runID)
	if !ok {
		return errors.New("运行不存在")
	}
	r.mu.Lock()
	ch := r.pending[interactionID]
	delete(r.pending, interactionID)
	r.mu.Unlock()
	if ch == nil {
		return errors.New("这个请求已经处理过了")
	}
	ch <- rep
	return nil
}

func (r *Run) wait(ctx context.Context, kind string, data map[string]any) (Reply, error) {
	id := ids.New()
	ch := make(chan Reply, 1)
	data["id"] = id
	data["kind"] = kind
	r.mu.Lock()
	r.pending[id] = ch
	if r.pendingData == nil {
		r.pendingData = map[string]map[string]any{}
	}
	r.pendingData[id] = data
	r.state = "waiting"
	r.mu.Unlock()
	r.emit(kind, data)
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		delete(r.pendingData, id)
		if r.state == "waiting" && len(r.pending) == 0 {
			r.state = "running"
		}
		r.mu.Unlock()
	}()
	select {
	case rep := <-ch:
		r.emit("interaction_done", map[string]any{"id": id})
		return rep, nil
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	}
}

// ---------- 浏览器端工具 ----------

type clientBridge struct {
	s   *Service
	run *Run
}

func (b *clientBridge) CallClient(ctx context.Context, tool string, args any) (json.RawMessage, error) {
	callID := ids.New()
	ch := make(chan json.RawMessage, 1)
	b.s.clients.Store(callID, ch)
	defer b.s.clients.Delete(callID)
	msg := realtime.Message{Type: "client_tool", Data: map[string]any{"call_id": callID, "tool": tool, "args": args, "run_id": b.run.ID}}
	if b.run.OriginWS == "" || !b.s.hub.SendToConn(b.run.OriginWS, msg) {
		if len(b.s.hub.UserConns(b.run.UserID)) == 0 {
			return nil, errors.New("用户的浏览器不在线")
		}
		b.s.hub.SendToUser(b.run.UserID, msg)
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(25 * time.Second):
		return nil, errors.New("浏览器没有在规定时间内响应")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ResolveClient 浏览器回传浏览器端工具的结果。
func (s *Service) ResolveClient(userID string, data json.RawMessage) {
	var in struct {
		CallID string          `json:"call_id"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &in) != nil {
		return
	}
	if v, ok := s.clients.Load(in.CallID); ok {
		select {
		case v.(chan json.RawMessage) <- in.Result:
		default:
		}
	}
}

// StopAll 中断全部运行（服务停止时）。
func (s *Service) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		r.cancel()
	}
}

// gc 清理已结束很久的运行。
func (s *Service) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.runs {
		st := r.State()
		if (st == "done" || st == "error" || st == "stopped") && time.Since(r.started) > 30*time.Minute {
			delete(s.runs, id)
			if s.byConv[r.ConvID] == r {
				delete(s.byConv, r.ConvID)
			}
		}
	}
}

func fmtErr(err error) string {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return ae.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "已停止"
	}
	return fmt.Sprint(err)
}
