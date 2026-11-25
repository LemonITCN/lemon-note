// Package comments 实现页面评论：页面讨论（不锚定）与段落批注（锚定选中的文字）。
//
// 一个线程 = 锚点（块 id + 被评论的原文）+ 若干条评论；可以标记为已解决，回复已解决的线程会重新打开。
// 正文里的锚点用 comment 标记（threadId）表示，标记由编辑器写进块里；线程删光评论后自动删除。
package comments

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

const (
	MaxBody   = 10000 // 单条评论最多字符数
	MaxAnchor = 500
)

var (
	ErrNotFound = errors.New("评论不存在")
	ErrEmpty    = errors.New("评论内容不能为空")
	ErrTooLong  = errors.New("评论太长")
	ErrNotOwner = errors.New("只能修改自己的评论")
)

// Service 评论服务。
type Service struct {
	st  *store.Store
	cat *catalog.Catalog
}

// New 创建评论服务。
func New(st *store.Store, cat *catalog.Catalog) *Service { return &Service{st: st, cat: cat} }

// Thread 是带评论的线程。
type Thread struct {
	model.CommentThread
	Comments []model.Comment `json:"comments"`
}

// List 返回页面的评论线程（按创建时间）。
func (s *Service) List(ctx context.Context, pageID string, includeResolved bool) ([]*Thread, error) {
	var ths []model.CommentThread
	if err := s.st.Select(ctx, &ths, "SELECT * FROM comment_threads WHERE page_id = ? ORDER BY created_at", pageID); err != nil {
		return nil, err
	}
	var cs []model.Comment
	if err := s.st.Select(ctx, &cs, "SELECT * FROM comments WHERE page_id = ? AND deleted_at IS NULL ORDER BY created_at", pageID); err != nil {
		return nil, err
	}
	by := map[string]*Thread{}
	out := []*Thread{}
	for _, t := range ths {
		if t.ResolvedAt != nil && !includeResolved {
			continue
		}
		th := &Thread{CommentThread: t, Comments: []model.Comment{}}
		by[t.ID] = th
		out = append(out, th)
	}
	for _, c := range cs {
		if th := by[c.ThreadID]; th != nil {
			th.Comments = append(th.Comments, c)
		}
	}
	// 没有评论的线程（理论上不会有）不返回
	kept := out[:0]
	for _, th := range out {
		if len(th.Comments) > 0 {
			kept = append(kept, th)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].CreatedAt < kept[j].CreatedAt })
	return kept, nil
}

// AddInput 新评论。ThreadID 为空表示新开线程。
type AddInput struct {
	ThreadID string `json:"thread_id"`
	BlockID  string `json:"block_id"`
	Anchor   string `json:"anchor"`
	Body     string `json:"body"`
}

func cleanBody(b string) (string, error) {
	b = strings.TrimSpace(strings.ReplaceAll(b, "\r\n", "\n"))
	if b == "" {
		return "", ErrEmpty
	}
	if utf8.RuneCountInString(b) > MaxBody {
		return "", ErrTooLong
	}
	return b, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Add 新增评论（必要时新开线程），并给线程里的其他参与者发通知。
func (s *Service) Add(ctx context.Context, actor, pageID string, in AddInput) (*Thread, error) {
	body, err := cleanBody(in.Body)
	if err != nil {
		return nil, err
	}
	var threadID string
	_, err = s.st.Write(ctx, actor, func(tx *store.Tx) error {
		now := model.Now()
		var th model.CommentThread
		if in.ThreadID != "" {
			if err := tx.Get(&th, "SELECT * FROM comment_threads WHERE id = ? AND page_id = ?", in.ThreadID, pageID); err != nil {
				return ErrNotFound
			}
			if th.ResolvedAt != nil { // 回复已解决的线程：重新打开
				th.ResolvedAt, th.ResolvedBy = nil, nil
				if err := tx.Put("comment_threads", th); err != nil {
					return err
				}
			}
		} else {
			th = model.CommentThread{ID: ids.New(), PageID: pageID, Anchor: clip(in.Anchor, MaxAnchor), CreatedAt: now, CreatedBy: actor}
			if in.BlockID != "" {
				th.BlockID = model.StrPtr(in.BlockID)
			}
			if err := tx.Put("comment_threads", th); err != nil {
				return err
			}
		}
		threadID = th.ID
		c := model.Comment{ID: ids.New(), ThreadID: th.ID, PageID: pageID, Body: body, CreatedAt: now, CreatedBy: actor, UpdatedAt: now}
		if err := tx.Put("comments", c); err != nil {
			return err
		}
		// 通知：线程发起人与之前回复过的人（不含自己、不含已经看不到这个页面的人）
		var who []string
		if err := tx.Select(&who, "SELECT DISTINCT created_by FROM comments WHERE thread_id = ? AND deleted_at IS NULL", th.ID); err != nil {
			return err
		}
		seen := map[string]bool{actor: true}
		data, _ := json.Marshal(map[string]any{"thread_id": th.ID, "comment_id": c.ID, "excerpt": clip(body, 140)})
		for _, uid := range append([]string{th.CreatedBy}, who...) {
			if seen[uid] || !s.cat.CanRead(uid, pageID) {
				continue
			}
			seen[uid] = true
			if err := tx.Put("notifications", model.Notification{ID: ids.New(), UserID: uid, Kind: "comment", PageID: model.StrPtr(pageID),
				ActorID: model.StrPtr(actor), Data: string(data), CreatedAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Thread(ctx, threadID)
}

// Thread 读一个线程（含评论）。
func (s *Service) Thread(ctx context.Context, id string) (*Thread, error) {
	var th model.CommentThread
	if err := s.st.Get(ctx, &th, "SELECT * FROM comment_threads WHERE id = ?", id); err != nil {
		return nil, ErrNotFound
	}
	out := &Thread{CommentThread: th, Comments: []model.Comment{}}
	if err := s.st.Select(ctx, &out.Comments, "SELECT * FROM comments WHERE thread_id = ? AND deleted_at IS NULL ORDER BY created_at", id); err != nil {
		return nil, err
	}
	return out, nil
}

// Comment 读一条评论。
func (s *Service) Comment(ctx context.Context, id string) (*model.Comment, error) {
	var c model.Comment
	if err := s.st.Get(ctx, &c, "SELECT * FROM comments WHERE id = ? AND deleted_at IS NULL", id); err != nil {
		return nil, ErrNotFound
	}
	return &c, nil
}

// Edit 修改自己的评论。
func (s *Service) Edit(ctx context.Context, actor, id, body string) (*model.Comment, error) {
	b, err := cleanBody(body)
	if err != nil {
		return nil, err
	}
	var out model.Comment
	_, err = s.st.Write(ctx, actor, func(tx *store.Tx) error {
		if err := tx.Get(&out, "SELECT * FROM comments WHERE id = ? AND deleted_at IS NULL", id); err != nil {
			return ErrNotFound
		}
		if out.CreatedBy != actor {
			return ErrNotOwner
		}
		out.Body, out.UpdatedAt = b, model.Now()
		return tx.Put("comments", out)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete 删除评论（软删除）；线程里没有评论了就删掉线程。返回线程是否已删除。
// canModerate 表示调用者对页面有完全访问权限，可以删别人的评论。
func (s *Service) Delete(ctx context.Context, actor, id string, canModerate bool) (threadGone bool, err error) {
	_, err = s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var c model.Comment
		if err := tx.Get(&c, "SELECT * FROM comments WHERE id = ? AND deleted_at IS NULL", id); err != nil {
			return ErrNotFound
		}
		if c.CreatedBy != actor && !canModerate {
			return ErrNotOwner
		}
		now := model.Now()
		c.DeletedAt = &now
		if err := tx.Put("comments", c); err != nil {
			return err
		}
		var left int
		if err := tx.QueryRow("SELECT count(*) FROM comments WHERE thread_id = ? AND deleted_at IS NULL", c.ThreadID).Scan(&left); err != nil {
			return err
		}
		if left == 0 {
			threadGone = true
			return tx.DeleteHint("comment_threads", c.ThreadID, store.Row{"page_id": c.PageID})
		}
		return nil
	})
	return threadGone, err
}

// Resolve 标记线程已解决 / 重新打开。
func (s *Service) Resolve(ctx context.Context, actor, threadID string, resolved bool) (*Thread, error) {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var th model.CommentThread
		if err := tx.Get(&th, "SELECT * FROM comment_threads WHERE id = ?", threadID); err != nil {
			return ErrNotFound
		}
		if resolved == (th.ResolvedAt != nil) {
			return nil
		}
		if resolved {
			now := model.Now()
			th.ResolvedAt, th.ResolvedBy = &now, model.StrPtr(actor)
		} else {
			th.ResolvedAt, th.ResolvedBy = nil, nil
		}
		return tx.Put("comment_threads", th)
	})
	if err != nil {
		return nil, err
	}
	return s.Thread(ctx, threadID)
}

// PageOfThread 返回线程所属页面。
func (s *Service) PageOfThread(ctx context.Context, threadID string) (string, error) {
	var pid string
	if err := s.st.QueryRow(ctx, "SELECT page_id FROM comment_threads WHERE id = ?", threadID).Scan(&pid); err != nil {
		return "", ErrNotFound
	}
	return pid, nil
}

// Counts 返回页面未解决的线程数（页面顶栏角标用）。
func (s *Service) OpenCount(ctx context.Context, pageID string) int {
	var n int
	_ = s.st.QueryRow(ctx, "SELECT count(*) FROM comment_threads WHERE page_id = ? AND resolved_at IS NULL", pageID).Scan(&n)
	return n
}
