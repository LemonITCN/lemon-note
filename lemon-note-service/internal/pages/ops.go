package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/fracidx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// BlockOp 是客户端提交的块操作。
type BlockOp struct {
	Op          string          `json:"op"` // put | del | move
	ID          string          `json:"id"`
	Type        string          `json:"type,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Position    string          `json:"position,omitempty"`
	BaseVersion int64           `json:"base_version"`
}

// OpResult 单个操作的结果。
type OpResult struct {
	ID       string `json:"id"`
	Version  int64  `json:"version"`
	Conflict bool   `json:"conflict,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// ApplyResult 一批操作的结果。
type ApplyResult struct {
	Rev     int64      `json:"rev"`
	Results []OpResult `json:"results"`
}

// AutoVersionInterval 编辑时自动存历史版本的最小间隔。
const AutoVersionInterval = 10 * time.Minute

// MaxOpsPerBatch 单次提交的操作上限。
const MaxOpsPerBatch = 2000

// ApplyOps 应用一批块操作。并发冲突时后写者胜，被覆盖的内容先存进历史版本。
func (s *Service) ApplyOps(ctx context.Context, actor, origin, pageID string, ops []BlockOp) (*ApplyResult, error) {
	if len(ops) > MaxOpsPerBatch {
		return nil, fmt.Errorf("单次最多 %d 个操作", MaxOpsPerBatch)
	}
	res := &ApplyResult{}
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		tx.SetOrigin(origin)
		r, err := s.ApplyOpsTx(tx, actor, pageID, ops, true)
		if err != nil {
			return err
		}
		*res = *r
		return nil
	})
	return res, err
}

// ApplyOpsTx 在事务里应用块操作。autoVersion 为真时按间隔自动存历史版本。
func (s *Service) ApplyOpsTx(tx *store.Tx, actor, pageID string, ops []BlockOp, autoVersion bool) (*ApplyResult, error) {
	var p model.Page
	if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", pageID); err != nil {
		return nil, ErrNotFound
	}
	if p.GetFlags().Locked {
		return nil, ErrLocked
	}
	if p.TrashedAt != nil {
		return nil, errors.New("页面在回收站里，恢复后才能编辑")
	}
	var existing []model.Block
	if err := tx.Select(&existing, "SELECT * FROM blocks WHERE page_id = ?", pageID); err != nil {
		return nil, err
	}
	byID := make(map[string]model.Block, len(existing))
	for _, b := range existing {
		byID[b.ID] = b
	}
	conflict := false
	for _, op := range ops {
		if b, ok := byID[op.ID]; ok && op.Op != "move" && op.BaseVersion != b.Version {
			conflict = true
			break
		}
	}
	if conflict {
		if _, err := s.SaveVersionTx(tx, actor, pageID, "conflict", nil); err != nil {
			return nil, err
		}
	} else if autoVersion && len(existing) > 0 {
		var last int64
		_ = tx.QueryRow("SELECT COALESCE(max(created_at), 0) FROM page_versions WHERE page_id = ?", pageID).Scan(&last)
		if time.Since(time.UnixMilli(last)) > AutoVersionInterval && time.Since(time.UnixMilli(p.UpdatedAt)) < 30*24*time.Hour {
			if _, err := s.SaveVersionTx(tx, actor, pageID, "auto", nil); err != nil {
				return nil, err
			}
		}
	}
	now := model.Now()
	res := &ApplyResult{}
	changed := false
	notified := map[string]bool{}
	for _, op := range ops {
		if op.ID == "" {
			return nil, errors.New("块操作缺少 id")
		}
		cur, exists := byID[op.ID]
		r := OpResult{ID: op.ID}
		switch op.Op {
		case "put":
			data, typ, err := NormalizeBlockData(op.ID, op.Type, op.Data)
			if err != nil {
				return nil, err
			}
			pos := op.Position
			if fracidx.Validate(pos) != nil {
				if exists {
					pos = cur.Position
				} else {
					pos, _ = fracidx.Between(lastPosition(byID), "")
				}
			}
			b := model.Block{ID: op.ID, PageID: pageID, Position: pos, Type: typ, Data: data, Version: 1,
				CreatedAt: now, CreatedBy: actor, UpdatedAt: now, UpdatedBy: actor}
			if exists {
				r.Conflict = op.BaseVersion != cur.Version
				b.Version = cur.Version + 1
				b.CreatedAt, b.CreatedBy = cur.CreatedAt, cur.CreatedBy
				if cur.Data == data && cur.Position == pos && cur.Type == typ {
					r.Version = cur.Version
					res.Results = append(res.Results, r)
					continue
				}
			} else if op.BaseVersion > 0 {
				r.Conflict = true // 别人已删除，这里重新创建
			} else if !ids.Valid(op.ID) && len(op.ID) > 64 {
				return nil, errors.New("块 id 不合法")
			}
			if err := tx.Put("blocks", b); err != nil {
				return nil, err
			}
			// 新 @ 到的人发收件箱通知（只发给能看到这个页面的人，不发给自己）
			prev := ""
			if exists {
				prev = cur.Data
			}
			for _, uid := range newUserMentions(prev, data) {
				if uid == actor || notified[uid] || !s.cat.CanRead(uid, pageID) {
					continue
				}
				notified[uid] = true
				if err := tx.Put("notifications", model.Notification{ID: ids.New(), UserID: uid, Kind: "mention", PageID: model.StrPtr(pageID),
					ActorID: model.StrPtr(actor), Data: `{"block_id":"` + op.ID + `"}`, CreatedAt: now}); err != nil {
					return nil, err
				}
			}
			byID[op.ID] = b
			r.Version = b.Version
			changed = true
		case "move":
			if !exists {
				r.Deleted = true
				res.Results = append(res.Results, r)
				continue
			}
			if fracidx.Validate(op.Position) != nil {
				return nil, errors.New("块位置不合法")
			}
			cur.Position = op.Position
			cur.Version++
			cur.UpdatedAt, cur.UpdatedBy = now, actor
			if err := tx.Put("blocks", cur); err != nil {
				return nil, err
			}
			byID[op.ID] = cur
			r.Version = cur.Version
			changed = true
		case "del":
			if exists {
				r.Conflict = op.BaseVersion != 0 && op.BaseVersion != cur.Version
				if err := tx.DeleteHint("blocks", op.ID, store.Row{"page_id": pageID}); err != nil {
					return nil, err
				}
				delete(byID, op.ID)
				changed = true
			}
			r.Deleted = true
		default:
			return nil, fmt.Errorf("未知的块操作 %q", op.Op)
		}
		res.Results = append(res.Results, r)
	}
	if changed {
		p.ContentRev++
		p.UpdatedAt, p.UpdatedBy = now, actor
		if err := tx.Put("pages", &p); err != nil {
			return nil, err
		}
	}
	res.Rev = p.ContentRev
	return res, nil
}

func lastPosition(m map[string]model.Block) string {
	last := ""
	for _, b := range m {
		if b.Position > last {
			last = b.Position
		}
	}
	return last
}

// ReplaceBlocksTx 把页面正文整体替换成 desired（按顺序），尽量复用原有块与位置，只写真正变化的块。
// desired 里 id 为空的块会分配新 id。返回变化块数。
func (s *Service) ReplaceBlocksTx(tx *store.Tx, actor, pageID string, desired []BlockDoc) (int, error) {
	var existing []model.Block
	if err := tx.Select(&existing, "SELECT * FROM blocks WHERE page_id = ?", pageID); err != nil {
		return 0, err
	}
	byID := map[string]model.Block{}
	for _, b := range existing {
		byID[b.ID] = b
	}
	for i := range desired {
		if desired[i].ID == "" {
			desired[i].ID = ids.New()
		}
	}
	// 找出保持相对顺序不变的已有块（最长递增子序列），它们沿用原位置
	keep := make([]bool, len(desired))
	var seqIdx []int
	var seqPos []string
	for i, d := range desired {
		if b, ok := byID[d.ID]; ok {
			seqIdx = append(seqIdx, i)
			seqPos = append(seqPos, b.Position)
		}
	}
	for _, k := range lis(seqPos) {
		keep[seqIdx[k]] = true
	}
	var ops []BlockOp
	seen := map[string]bool{}
	prev := ""
	for i, d := range desired {
		seen[d.ID] = true
		var pos string
		if keep[i] {
			pos = byID[d.ID].Position
		} else {
			next := ""
			for j := i + 1; j < len(desired); j++ {
				if keep[j] {
					next = byID[desired[j].ID].Position
					break
				}
			}
			p, err := fracidx.Between(prev, next)
			if err != nil {
				return 0, err
			}
			pos = p
		}
		prev = pos
		data, typ, err := NormalizeBlockData(d.ID, d.Type, d.Data)
		if err != nil {
			return 0, err
		}
		if b, ok := byID[d.ID]; ok && b.Data == data && b.Position == pos && b.Type == typ {
			continue
		}
		base := int64(0)
		if b, ok := byID[d.ID]; ok {
			base = b.Version
		}
		ops = append(ops, BlockOp{Op: "put", ID: d.ID, Type: typ, Data: json.RawMessage(data), Position: pos, BaseVersion: base})
	}
	for _, b := range existing {
		if !seen[b.ID] {
			ops = append(ops, BlockOp{Op: "del", ID: b.ID, BaseVersion: b.Version})
		}
	}
	if len(ops) == 0 {
		return 0, nil
	}
	if _, err := s.ApplyOpsTx(tx, actor, pageID, ops, false); err != nil {
		return 0, err
	}
	return len(ops), nil
}

// lis 返回字符串序列最长严格递增子序列的下标。
func lis(a []string) []int {
	n := len(a)
	if n == 0 {
		return nil
	}
	tails := []int{}
	prev := make([]int, n)
	for i := range a {
		lo, hi := 0, len(tails)
		for lo < hi {
			mid := (lo + hi) / 2
			if a[tails[mid]] < a[i] {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			prev[i] = tails[lo-1]
		} else {
			prev[i] = -1
		}
		if lo == len(tails) {
			tails = append(tails, i)
		} else {
			tails[lo] = i
		}
	}
	out := make([]int, len(tails))
	k := tails[len(tails)-1]
	for i := len(tails) - 1; i >= 0; i-- {
		out[i] = k
		k = prev[k]
	}
	return out
}

// newUserMentions 返回 cur 里有、prev 里没有的用户提及（块 JSON 里 type=mention、attrs.kind=user 的节点）。
func newUserMentions(prev, cur string) []string {
	if !strings.Contains(cur, `"user"`) {
		return nil
	}
	had := map[string]bool{}
	for _, id := range userMentions(prev) {
		had[id] = true
	}
	var out []string
	for _, id := range userMentions(cur) {
		if !had[id] {
			had[id] = true
			out = append(out, id)
		}
	}
	return out
}

func userMentions(data string) []string {
	if data == "" {
		return nil
	}
	var n any
	if json.Unmarshal([]byte(data), &n) != nil {
		return nil
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if m["type"] == "mention" {
			if a, ok := m["attrs"].(map[string]any); ok && a["kind"] == "user" {
				if id, _ := a["id"].(string); id != "" {
					out = append(out, id)
				}
			}
		}
		if c, ok := m["content"].([]any); ok {
			for _, x := range c {
				walk(x)
			}
		}
	}
	walk(n)
	return out
}
