package comments

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
)

func svcOf(env *tools.Env) (*Service, error) {
	s, ok := env.Svc.Comments.(*Service)
	if !ok || s == nil {
		return nil, fmt.Errorf("评论功能不可用")
	}
	return s, nil
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}
func str(d string) map[string]any     { return map[string]any{"type": "string", "description": d} }
func boolean(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }

func bind(args json.RawMessage, dst any) error {
	if len(args) == 0 {
		args = []byte("{}")
	}
	if err := json.Unmarshal(args, dst); err != nil {
		return fmt.Errorf("参数格式不对：%v", err)
	}
	return nil
}

func userName(env *tools.Env, id string) string {
	if u, ok := env.Svc.Cat.User(id); ok && u.DisplayName != "" {
		return u.DisplayName
	}
	return id
}

func init() {
	tools.Default.Register(&tools.Tool{
		Name:  "list_comments",
		Title: "查看评论",
		Description: "列出页面上的评论线程：每个线程的 id、被评论的原文（anchor）、是否已解决、每条评论的作者、时间和内容。" +
			"默认只列未解决的，include_resolved=true 时也列已解决的。",
		Schema:   obj(map[string]any{"page_id": str("页面 id"), "include_resolved": boolean("是否包含已解决的线程")}, "page_id"),
		ReadOnly: true,
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			var in struct {
				PageID          string `json:"page_id"`
				IncludeResolved bool   `json:"include_resolved"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.PageID, model.RoleViewer)
			if err != nil {
				return nil, err
			}
			ths, err := s.List(env.Ctx, meta.ID, in.IncludeResolved)
			if err != nil {
				return nil, err
			}
			if len(ths) == 0 {
				return &tools.Result{Text: fmt.Sprintf("页面「%s」没有评论。", meta.Title)}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "页面「%s」共 %d 个评论线程：\n", meta.Title, len(ths))
			for _, th := range ths {
				state := "未解决"
				if th.ResolvedAt != nil {
					state = "已解决"
				}
				fmt.Fprintf(&b, "\n## 线程 %s（%s）\n", th.ID, state)
				if th.Anchor != "" {
					fmt.Fprintf(&b, "> %s\n", strings.ReplaceAll(th.Anchor, "\n", " "))
				} else {
					b.WriteString("（页面讨论，不针对具体段落）\n")
				}
				for _, c := range th.Comments {
					fmt.Fprintf(&b, "- %s（%s）：%s\n", userName(env, c.CreatedBy), fmtTime(c.CreatedAt), strings.ReplaceAll(c.Body, "\n", "\n  "))
				}
			}
			return &tools.Result{Text: b.String()}, nil
		},
	})
	tools.Default.Register(&tools.Tool{
		Name:  "add_comment",
		Title: "添加评论",
		Description: "在页面上发表评论。回复已有线程时给 thread_id；新开线程时可以给 quote（页面正文里的一段原文，用来指明评论的是哪一段），" +
			"不给 quote 就是针对整个页面的讨论。评论不会改动正文；要修改正文请用编辑类工具。",
		Schema: obj(map[string]any{
			"page_id":   str("页面 id"),
			"body":      str("评论内容（纯文本，可多行）"),
			"thread_id": str("要回复的线程 id（可选）"),
			"quote":     str("新线程针对的正文原文（可选，需与正文一字不差的一小段）"),
		}, "page_id", "body"),
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			var in struct {
				PageID   string `json:"page_id"`
				Body     string `json:"body"`
				ThreadID string `json:"thread_id"`
				Quote    string `json:"quote"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.PageID, model.RoleCommenter)
			if err != nil {
				return nil, err
			}
			add := AddInput{ThreadID: in.ThreadID, Body: in.Body}
			if in.ThreadID == "" && strings.TrimSpace(in.Quote) != "" {
				add.Anchor = in.Quote
				add.BlockID = findBlock(env, meta.ID, strings.TrimSpace(in.Quote))
				if add.BlockID == "" {
					return &tools.Result{Text: "正文里找不到这段原文，请先 read_page 确认原文，或不带 quote 发表页面讨论。", IsError: true}, nil
				}
			}
			th, err := s.Add(env.Ctx, env.User.ID, meta.ID, add)
			if err != nil {
				return nil, err
			}
			return &tools.Result{Text: fmt.Sprintf("已在页面「%s」发表评论（线程 %s）。", meta.Title, th.ID), Data: map[string]any{"thread_id": th.ID}}, nil
		},
	})
	tools.Default.Register(&tools.Tool{
		Name:        "resolve_comment",
		Title:       "解决评论",
		Description: "把评论线程标记为已解决（resolved=false 则重新打开）。",
		Schema:      obj(map[string]any{"thread_id": str("线程 id"), "resolved": boolean("默认 true")}, "thread_id"),
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			in := struct {
				ThreadID string `json:"thread_id"`
				Resolved *bool  `json:"resolved"`
			}{}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			pid, err := s.PageOfThread(env.Ctx, in.ThreadID)
			if err != nil {
				return nil, err
			}
			if _, err := env.NeedPage(pid, model.RoleCommenter); err != nil {
				return nil, err
			}
			want := in.Resolved == nil || *in.Resolved
			if _, err := s.Resolve(env.Ctx, env.User.ID, in.ThreadID, want); err != nil {
				return nil, err
			}
			if want {
				return &tools.Result{Text: "已标记为已解决。"}, nil
			}
			return &tools.Result{Text: "已重新打开。"}, nil
		},
	})
}

// findBlock 找到正文里包含这段原文的顶层块。
func findBlock(env *tools.Env, pageID, quote string) string {
	bs, err := env.Svc.Pages.Blocks(env.Ctx, pageID)
	if err != nil {
		return ""
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	q := norm(quote)
	for _, b := range bs {
		var n lnmd.Node
		if json.Unmarshal([]byte(b.Data), &n) != nil {
			continue
		}
		if strings.Contains(norm(lnmd.PlainText(n)), q) {
			return b.ID
		}
	}
	return ""
}

func fmtTime(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04") }
