package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func kindLabel(p catalog.PageMeta) string {
	switch {
	case p.Kind == model.KindDatabase:
		return "数据库"
	case p.DatabaseID != "":
		return "数据库行"
	}
	return "页面"
}

func init() {
	Default.Register(&Tool{
		Name:  "search",
		Title: "搜索",
		Description: "全文搜索用户能访问的页面、数据库与数据库行（标题与正文，中英文都可以）。返回标题、id、路径与命中片段。" +
			"找到后用 read_page 读取完整内容。精确查找某个词在哪些页面的哪一行，用 grep_pages。",
		Schema: obj(map[string]any{
			"query":         str("搜索词"),
			"space_id":      str("只在某个空间里搜（可选）"),
			"under_page_id": str("只在某个页面的子孙里搜（可选）"),
			"limit":         num("最多返回多少条（默认 20，最多 50）"),
		}, "query"),
		ReadOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Query string `json:"query"`
				Space string `json:"space_id"`
				Under string `json:"under_page_id"`
				Limit int    `json:"limit"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if in.Limit <= 0 || in.Limit > 50 {
				in.Limit = 20
			}
			var spaces []string
			if in.Space != "" {
				spaces = []string{in.Space}
			}
			hits, err := env.Svc.Search.Search(env.Ctx, in.Query, spaces, in.Limit*4)
			if err != nil {
				return nil, err
			}
			var b strings.Builder
			n := 0
			var ids []string
			for _, h := range hits {
				m, ok := env.Svc.Cat.Page(h.PageID)
				if !ok || m.TrashedAt != 0 || env.PageRole(h.PageID) == model.RoleNone {
					continue
				}
				if in.Under != "" && !env.Svc.Cat.IsDescendant(h.PageID, in.Under) {
					continue
				}
				n++
				ids = append(ids, h.PageID)
				snip := strings.NewReplacer("<mark>", "【", "</mark>", "】", "&lt;", "<", "&gt;", ">", "&amp;", "&", "&#34;", "\"", "&#39;", "'").Replace(h.Snippet)
				fmt.Fprintf(&b, "%d. %s（%s，id：%s）\n   路径：%s · 更新于 %s\n   片段：%s\n", n, titleOr(m.Title), kindLabel(m), m.ID, env.PathOf(m.ID), fmtTime(m.UpdatedAt), strings.TrimSpace(snip))
				if n >= in.Limit {
					break
				}
			}
			if n == 0 {
				return &Result{Text: "没有找到匹配「" + in.Query + "」的内容。可以换个关键词，或用 list_pages 浏览目录。"}, nil
			}
			return &Result{Text: fmt.Sprintf("找到 %d 条：\n%s", n, b.String()), Data: map[string]any{"page_ids": ids}}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "list_pages",
		Title: "浏览页面目录",
		Description: "列出页面树。parent_id 为空时列出全部可访问空间的根目录；否则列出该页面下的子页面。depth 控制展开层数（默认 2，最多 6）。" +
			"返回标题、类型、id 与子页面数。",
		Schema: obj(map[string]any{
			"parent_id":    str("从哪个页面开始（可选）"),
			"space_id":     str("只列某个空间（可选）"),
			"depth":        num("展开层数（默认 2）"),
			"include_rows": boolean("是否列出数据库行（默认 false）"),
		}),
		ReadOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Parent string `json:"parent_id"`
				Space  string `json:"space_id"`
				Depth  int    `json:"depth"`
				Rows   bool   `json:"include_rows"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if in.Depth <= 0 {
				in.Depth = 2
			}
			if in.Depth > 6 {
				in.Depth = 6
			}
			var b strings.Builder
			budget := 400
			var walk func(spaceID, parentID string, depth int, indent string)
			walk = func(spaceID, parentID string, depth int, indent string) {
				kids := env.Svc.Cat.Children(spaceID, parentID)
				shown := 0
				for _, c := range kids {
					if c.TrashedAt != 0 || (c.DatabaseID != "" && !in.Rows) || env.PageRole(c.ID) == model.RoleNone {
						continue
					}
					if budget <= 0 {
						b.WriteString(indent + "- …（太多，缩小范围再看）\n")
						return
					}
					budget--
					shown++
					sub := 0
					for _, g := range env.Svc.Cat.Children(spaceID, c.ID) {
						if g.TrashedAt == 0 && (g.DatabaseID == "" || in.Rows) {
							sub++
						}
					}
					extra := ""
					if sub > 0 {
						extra = fmt.Sprintf("，%d 个子项", sub)
					}
					fmt.Fprintf(&b, "%s- %s（%s，id：%s%s）\n", indent, titleOr(c.Title), kindLabel(c), c.ID, extra)
					if depth > 1 && sub > 0 {
						walk(spaceID, c.ID, depth-1, indent+"  ")
					}
				}
				if shown == 0 && parentID != "" && indent == "" {
					b.WriteString("（没有子页面）\n")
				}
			}
			if in.Parent != "" {
				p, err := env.NeedPage(in.Parent, model.RoleViewer)
				if err != nil {
					return nil, err
				}
				fmt.Fprintf(&b, "%s（%s，id：%s）\n", titleOr(p.Title), kindLabel(p), p.ID)
				walk(p.SpaceID, p.ID, in.Depth, "")
			} else {
				for _, sp := range env.VisibleSpaces() {
					if in.Space != "" && sp.ID != in.Space {
						continue
					}
					kind := "个人空间"
					if sp.Kind == model.SpaceShared {
						kind = "共享空间"
					}
					fmt.Fprintf(&b, "空间「%s」（%s，id：%s）\n", sp.Name, kind, sp.ID)
					walk(sp.ID, "", in.Depth, "  ")
				}
				if shared := env.Svc.Cat.SharedWithUser(env.User.ID); len(shared) > 0 {
					b.WriteString("与我共享：\n")
					for _, p := range shared {
						fmt.Fprintf(&b, "  - %s（%s，id：%s）\n", titleOr(p.Title), kindLabel(p), p.ID)
					}
				}
			}
			return &Result{Text: b.String()}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "grep_pages",
		Title: "查找文字",
		Description: "在页面正文里逐行查找关键字或正则表达式，返回所在页面与行内容（类似 grep）。适合精确定位要修改的位置、统计某个词出现在哪里。" +
			"under_page_id 限定在某个页面及其子孙中查找；不填则查全部可访问页面（较慢，最多扫 2000 个页面）。",
		Schema: obj(map[string]any{
			"pattern":        str("关键字或正则表达式"),
			"regex":          boolean("pattern 是否为正则（默认 false）"),
			"case_sensitive": boolean("是否区分大小写（默认 false）"),
			"under_page_id":  str("只在这个页面及其子孙中查找（可选）"),
			"space_id":       str("只在某个空间查找（可选）"),
			"limit":          num("最多返回多少行（默认 50）"),
		}, "pattern"),
		ReadOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Pattern string `json:"pattern"`
				Regex   bool   `json:"regex"`
				Case    bool   `json:"case_sensitive"`
				Under   string `json:"under_page_id"`
				Space   string `json:"space_id"`
				Limit   int    `json:"limit"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if in.Limit <= 0 || in.Limit > 200 {
				in.Limit = 50
			}
			expr := in.Pattern
			if !in.Regex {
				expr = regexp.QuoteMeta(expr)
			}
			if !in.Case {
				expr = "(?i)" + expr
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				return nil, fail("正则表达式无效：%v", err)
			}
			var candidates []catalog.PageMeta
			if in.Under != "" {
				root, err := env.NeedPage(in.Under, model.RoleViewer)
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, root)
				for _, id := range env.Svc.Cat.Descendants(root.ID) {
					if m, ok := env.Svc.Cat.Page(id); ok {
						candidates = append(candidates, m)
					}
				}
			} else {
				for _, sp := range env.VisibleSpaces() {
					if in.Space == "" || in.Space == sp.ID {
						candidates = append(candidates, env.Svc.Cat.SpacePages(sp.ID)...)
					}
				}
				sort.Slice(candidates, func(i, j int) bool { return candidates[i].UpdatedAt > candidates[j].UpdatedAt })
			}
			var b strings.Builder
			hits, scanned := 0, 0
			for _, m := range candidates {
				if m.TrashedAt != 0 || m.Kind == model.KindDatabase || env.PageRole(m.ID) == model.RoleNone {
					continue
				}
				if scanned++; scanned > 2000 {
					b.WriteString("（页面太多，只扫描了最近更新的 2000 个，请用 under_page_id 缩小范围）\n")
					break
				}
				md, _, _, err := env.PageMarkdown(m.ID)
				if err != nil {
					continue
				}
				header := false
				for i, line := range strings.Split(md, "\n") {
					if re.MatchString(line) {
						if !header {
							fmt.Fprintf(&b, "%s（id：%s）\n", titleOr(m.Title), m.ID)
							header = true
						}
						fmt.Fprintf(&b, "  %d: %s\n", i+1, truncate(line, 300))
						if hits++; hits >= in.Limit {
							b.WriteString("（已达到返回上限）\n")
							return &Result{Text: b.String()}, nil
						}
					}
				}
				if re.MatchString(m.Title) && !header {
					fmt.Fprintf(&b, "%s（id：%s）标题匹配\n", titleOr(m.Title), m.ID)
					hits++
				}
			}
			if hits == 0 {
				return &Result{Text: "没有找到匹配的内容。"}, nil
			}
			return &Result{Text: b.String()}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "update_page",
		Title:       "修改页面信息",
		Description: "修改页面的标题、图标、封面与显示设置（锁定、全宽、小字号、字体）。数据库行的属性请用 upsert_rows。只传需要修改的字段。",
		Schema: obj(map[string]any{
			"page_id":    str("页面 id"),
			"title":      str("新标题"),
			"icon":       str("图标：一个字符，或 icon:<名字>；空串表示移除"),
			"cover":      str("封面：gradient:1 到 gradient:8，或 lemon://file/<文件id>；空串表示移除"),
			"locked":     boolean("锁定页面（锁定后正文不能修改）"),
			"full_width": boolean("全宽显示"),
			"small_text": boolean("小字号"),
			"font":       enum("字体", "default", "serif", "mono"),
		}, "page_id"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID    string  `json:"page_id"`
				Title     *string `json:"title"`
				Icon      *string `json:"icon"`
				Cover     *string `json:"cover"`
				Locked    *bool   `json:"locked"`
				FullWidth *bool   `json:"full_width"`
				SmallText *bool   `json:"small_text"`
				Font      *string `json:"font"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.PageID, model.RoleEditor)
			if err != nil {
				return nil, err
			}
			p, err := env.Svc.Pages.Get(env.Ctx, meta.ID)
			if err != nil {
				return nil, err
			}
			if err := env.beforeWrite(meta.ID); err != nil {
				return nil, err
			}
			f := p.GetFlags()
			if in.Locked != nil {
				f.Locked = *in.Locked
			}
			if in.FullWidth != nil {
				f.FullWidth = *in.FullWidth
			}
			if in.SmallText != nil {
				f.SmallText = *in.SmallText
			}
			if in.Font != nil {
				f.Font = *in.Font
			}
			_, err = env.Svc.Store.Write(env.Ctx, env.User.ID, func(tx *store.Tx) error {
				tx.SetOrigin(env.Origin)
				_, err := env.Svc.Pages.UpdateTx(tx, env.User.ID, meta.ID, pages.Patch{Title: in.Title, Icon: in.Icon, Cover: in.Cover, Flags: &f})
				return err
			})
			if err != nil {
				return nil, err
			}
			return &Result{Text: "已更新页面信息。", Data: map[string]any{"page_id": meta.ID}}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "move_pages",
		Title:       "移动页面",
		Description: "把页面移动到新的上级页面下（或某个空间的根目录），子页面一起移动。after_id 指定排在哪个兄弟页面之后。",
		Schema: obj(map[string]any{
			"page_ids":  arr(str("页面 id"), "要移动的页面"),
			"parent_id": str("新的上级页面 id；为空表示移到空间根目录"),
			"space_id":  str("移到空间根目录时的目标空间（可选，默认原空间）"),
			"after_id":  str("排在哪个兄弟页面之后（可选）"),
		}, "page_ids"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				IDs     []string `json:"page_ids"`
				Parent  string   `json:"parent_id"`
				Space   string   `json:"space_id"`
				AfterID string   `json:"after_id"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if in.Parent != "" {
				if _, err := env.NeedPage(in.Parent, model.RoleEditor); err != nil {
					return nil, err
				}
			} else if in.Space != "" && !model.RoleAtLeast(env.SpaceRole(in.Space), model.RoleEditor) {
				return nil, fail("没有目标空间的编辑权限")
			}
			after := in.AfterID
			var done []string
			for _, id := range in.IDs {
				m, err := env.NeedPage(id, model.RoleEditor)
				if err != nil {
					return nil, err
				}
				if in.Parent == "" && !model.RoleAtLeast(env.SpaceRole(m.SpaceID), model.RoleEditor) {
					return nil, fail("没有在空间根目录移动「%s」的权限", m.Title)
				}
				if err := env.Svc.Pages.Move(env.Ctx, env.User.ID, id, pages.MoveInput{SpaceID: in.Space, ParentID: in.Parent, AfterID: after}); err != nil {
					return nil, err
				}
				after = id
				done = append(done, titleOr(m.Title))
			}
			return &Result{Text: "已移动：" + strings.Join(done, "、")}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "duplicate_page",
		Title:       "复制页面",
		Description: "复制一个页面（数据库会连同结构、视图与行一起复制），副本放在原页面旁边。",
		Schema: obj(map[string]any{
			"page_id":       str("页面 id"),
			"with_children": boolean("是否连子页面一起复制（默认 true）"),
			"title":         str("副本标题（可选，默认原标题）"),
		}, "page_id"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID string `json:"page_id"`
				With   *bool  `json:"with_children"`
				Title  string `json:"title"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			m, err := env.NeedPage(in.PageID, model.RoleViewer)
			if err != nil {
				return nil, err
			}
			if m.ParentID != "" {
				if _, err := env.NeedPage(m.ParentID, model.RoleEditor); err != nil {
					return nil, err
				}
			} else if !model.RoleAtLeast(env.SpaceRole(m.SpaceID), model.RoleEditor) {
				return nil, fail("没有在这里新建页面的权限")
			}
			nid, err := env.Svc.Pages.Duplicate(env.Ctx, env.User.ID, m.ID, in.With == nil || *in.With, "")
			if err != nil {
				return nil, err
			}
			if in.Title != "" {
				_, _ = env.Svc.Pages.Update(env.Ctx, env.User.ID, nid, pages.Patch{Title: &in.Title})
			}
			if env.Created != nil {
				env.Created(nid)
			}
			return &Result{Text: fmt.Sprintf("已复制，新页面 id：%s", nid), Data: map[string]any{"page_id": nid}}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "delete_pages",
		Title:       "删除页面",
		Description: "把页面（连同子页面）移入回收站，30 天内可用 restore_pages 恢复。",
		Schema:      obj(map[string]any{"page_ids": arr(str("页面 id"), "要删除的页面")}, "page_ids"),
		Destructive: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				IDs []string `json:"page_ids"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			var done []string
			for _, id := range in.IDs {
				m, err := env.NeedPage(id, model.RoleEditor)
				if err != nil {
					return nil, err
				}
				if err := env.beforeWrite(id); err != nil {
					return nil, err
				}
				if err := env.Svc.Pages.Trash(env.Ctx, env.User.ID, id); err != nil {
					return nil, err
				}
				done = append(done, titleOr(m.Title))
			}
			return &Result{Text: "已移入回收站：" + strings.Join(done, "、"), Data: map[string]any{"trashed": in.IDs}}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "restore_pages",
		Title:       "恢复页面",
		Description: "从回收站恢复页面（同一次删除的子页面一起恢复）。",
		Schema:      obj(map[string]any{"page_ids": arr(str("页面 id"), "要恢复的页面")}, "page_ids"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				IDs []string `json:"page_ids"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			for _, id := range in.IDs {
				if _, err := env.NeedPage(id, model.RoleEditor); err != nil {
					return nil, err
				}
				if err := env.Svc.Pages.Restore(env.Ctx, env.User.ID, id); err != nil {
					return nil, err
				}
			}
			return &Result{Text: fmt.Sprintf("已恢复 %d 个页面。", len(in.IDs))}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "page_history",
		Title:       "历史版本",
		Description: "列出页面的历史版本（自动保存、智能体修改前、并发冲突前等），可用 restore_page_version 恢复；传 version_id 时返回该版本的正文。",
		Schema: obj(map[string]any{
			"page_id":    str("页面 id"),
			"version_id": str("要查看内容的版本 id（可选）"),
		}, "page_id"),
		ReadOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID    string `json:"page_id"`
				VersionID string `json:"version_id"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if _, err := env.NeedPage(in.PageID, model.RoleViewer); err != nil {
				return nil, err
			}
			if in.VersionID != "" {
				doc, err := env.Svc.Pages.VersionDoc(env.Ctx, in.PageID, in.VersionID)
				if err != nil {
					return nil, err
				}
				var nodes []lnmdNode
				for _, b := range doc.Blocks {
					var n lnmdNode
					if json.Unmarshal(b.Data, &n) == nil {
						nodes = append(nodes, n)
					}
				}
				return &Result{Text: fmt.Sprintf("# %s（历史版本 %s）\n---\n%s", titleOr(doc.Page.Title), in.VersionID, renderNodes(env, nodes))}, nil
			}
			vs, err := env.Svc.Pages.Versions(env.Ctx, in.PageID, 50)
			if err != nil {
				return nil, err
			}
			if len(vs) == 0 {
				return &Result{Text: "这个页面还没有历史版本。"}, nil
			}
			reasons := map[string]string{"auto": "自动保存", "conflict": "并发修改前", "ai": "智能体修改前", "mcp": "外部智能体修改前", "before_restore": "恢复前", "manual": "手动"}
			var b strings.Builder
			for _, v := range vs {
				r := reasons[v.Reason]
				if r == "" {
					r = v.Reason
				}
				fmt.Fprintf(&b, "- %s · %s · 标题「%s」（version_id：%s）\n", fmtTime(v.CreatedAt), r, v.Title, v.ID)
			}
			return &Result{Text: b.String()}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "restore_page_version",
		Title:       "恢复历史版本",
		Description: "把页面恢复到某个历史版本（恢复前会自动把当前内容再存一个版本，可以再恢复回来）。",
		Schema: obj(map[string]any{
			"page_id":    str("页面 id"),
			"version_id": str("版本 id"),
		}, "page_id", "version_id"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID    string `json:"page_id"`
				VersionID string `json:"version_id"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if _, err := env.NeedPage(in.PageID, model.RoleEditor); err != nil {
				return nil, err
			}
			if err := env.beforeWrite(in.PageID); err != nil {
				return nil, err
			}
			if err := env.Svc.Pages.RestoreVersion(env.Ctx, env.User.ID, in.PageID, in.VersionID); err != nil {
				return nil, err
			}
			return &Result{Text: "已恢复到该版本。"}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "list_spaces",
		Title:       "列出空间",
		Description: "列出用户可访问的空间（个人空间、共享空间）及角色，以及系统里的其他用户（分享、提及时用）。",
		Schema:      obj(map[string]any{}),
		ReadOnly:    true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var b strings.Builder
			b.WriteString("空间：\n")
			for _, sp := range env.VisibleSpaces() {
				kind := "个人空间"
				if sp.Kind == model.SpaceShared {
					kind = "共享空间"
				}
				fmt.Fprintf(&b, "- %s（%s，角色：%s，id：%s）\n", sp.Name, kind, env.SpaceRole(sp.ID), sp.ID)
			}
			b.WriteString("用户：\n")
			for _, u := range env.Svc.Cat.Users() {
				if u.Status == model.UserActive {
					me := ""
					if u.ID == env.User.ID {
						me = "（我）"
					}
					fmt.Fprintf(&b, "- %s @%s%s（id：%s）\n", u.DisplayName, u.Username, me, u.ID)
				}
			}
			return &Result{Text: b.String()}, nil
		},
	})
}
