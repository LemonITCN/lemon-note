package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// RenderOpts 返回按当前用户权限解析标题的渲染选项。
func (env *Env) RenderOpts() lnmd.RenderOptions {
	return lnmd.RenderOptions{
		PageTitle: func(id string) (string, bool) {
			p, ok := env.Svc.Cat.Page(id)
			if !ok || env.PageRole(id) == model.RoleNone {
				return "", false
			}
			return titleOr(p.Title), true
		},
		UserName: func(id string) (string, bool) {
			u, ok := env.Svc.Cat.User(id)
			if !ok {
				return "", false
			}
			return u.DisplayName, true
		},
	}
}

// LoadNodes 读页面的块并转成节点。
func (env *Env) LoadNodes(pageID string) ([]lnmd.Node, *model.Page, error) {
	p, err := env.Svc.Pages.Get(env.Ctx, pageID)
	if err != nil {
		return nil, nil, err
	}
	bs, err := env.Svc.Pages.Blocks(env.Ctx, pageID)
	if err != nil {
		return nil, nil, err
	}
	nodes := make([]lnmd.Node, 0, len(bs))
	for _, b := range bs {
		var n lnmd.Node
		if err := json.Unmarshal([]byte(b.Data), &n); err != nil {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, p, nil
}

// NodesToBlocks 把节点转成块（id 取 attrs.id）。
func NodesToBlocks(nodes []lnmd.Node) ([]pages.BlockDoc, error) {
	out := make([]pages.BlockDoc, 0, len(nodes))
	for _, n := range nodes {
		id := ""
		if n.Attrs != nil {
			if s, ok := n.Attrs["id"].(string); ok {
				id = s
			}
		}
		b, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		out = append(out, pages.BlockDoc{ID: id, Type: n.Type, Data: b})
	}
	return out, nil
}

// PageMarkdown 渲染页面正文。
func (env *Env) PageMarkdown(pageID string) (string, *model.Page, []lnmd.Node, error) {
	nodes, p, err := env.LoadNodes(pageID)
	if err != nil {
		return "", nil, nil, err
	}
	return lnmd.Render(nodes, env.RenderOpts()), p, nodes, nil
}

func fmtTime(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04") }

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func init() {
	Default.Register(&Tool{
		Name:  "read_page",
		Title: "读取页面",
		Description: "读取一个页面（或数据库行）的完整内容：标题、路径、属性与增强 Markdown 正文。修改页面之前必须先读取。" +
			"正文很长时会分段返回，用 offset 与 limit（按行）继续读。数据库请用 query_database 读取行。",
		Schema: obj(map[string]any{
			"page_id": str("页面 id"),
			"offset":  num("从第几行开始读（从 1 开始，默认 1）"),
			"limit":   num("最多读多少行（默认 600）"),
		}, "page_id"),
		ReadOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID string `json:"page_id"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.PageID, model.RoleViewer)
			if err != nil {
				return nil, err
			}
			md, p, _, err := env.PageMarkdown(meta.ID)
			if err != nil {
				return nil, err
			}
			var b strings.Builder
			kind := "页面"
			if p.Kind == model.KindDatabase {
				kind = "数据库（行请用 query_database 读取）"
			} else if p.DatabaseID != nil {
				kind = "数据库行"
			}
			fmt.Fprintf(&b, "# %s\n- id：%s\n- 路径：%s\n- 类型：%s\n", titleOr(p.Title), p.ID, env.PathOf(p.ID), kind)
			if p.Icon != "" {
				fmt.Fprintf(&b, "- 图标：%s\n", p.Icon)
			}
			if f := p.GetFlags(); f.Locked {
				b.WriteString("- 已锁定（不能修改，除非先解锁）\n")
			}
			if props := env.rowProps(p); props != "" {
				b.WriteString("- 属性：\n" + props)
			}
			kids := 0
			for _, c := range env.Svc.Cat.Children(p.SpaceID, p.ID) {
				if c.TrashedAt == 0 && c.DatabaseID == "" {
					kids++
				}
			}
			if kids > 0 {
				fmt.Fprintf(&b, "- 子页面：%d 个（用 list_pages 查看）\n", kids)
			}
			if p.TrashedAt != nil {
				b.WriteString("- 在回收站中\n")
			}
			fmt.Fprintf(&b, "- 最后编辑：%s\n\n", fmtTime(p.UpdatedAt))
			total := countLines(md)
			limit := in.Limit
			if limit <= 0 || limit > 2000 {
				limit = 600
			}
			off := in.Offset
			if off < 1 {
				off = 1
			}
			if md == "" {
				b.WriteString("（正文为空）")
			} else if total <= limit && off == 1 {
				b.WriteString("---\n")
				b.WriteString(md)
				env.Reads.Mark(p.ID, p.ContentRev)
			} else {
				lines := strings.Split(md, "\n")
				end := off - 1 + limit
				if end > len(lines) {
					end = len(lines)
				}
				if off > len(lines) {
					return nil, fail("offset 超出范围，正文共 %d 行", len(lines))
				}
				fmt.Fprintf(&b, "---\n（第 %d-%d 行，共 %d 行）\n", off, end, total)
				b.WriteString(strings.Join(lines[off-1:end], "\n"))
				if end < len(lines) {
					fmt.Fprintf(&b, "\n\n（还有 %d 行，用 offset=%d 继续读）", len(lines)-end, end+1)
				}
				env.Reads.Mark(p.ID, p.ContentRev)
			}
			return &Result{Text: b.String(), Data: map[string]any{"page_id": p.ID, "title": p.Title}}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "edit_page",
		Title: "修改页面",
		Description: "对页面的增强 Markdown 正文做精确的字符串替换（可一次多处）。old_string 必须与 read_page 返回的正文逐字一致（包括空格与换行），" +
			"并且在正文里唯一；不唯一时加更多上下文，或者确实要全部替换时设置 replace_all。没改到的块原样保留（评论、颜色等不受影响）。" +
			"必须先用 read_page 读取，且读取后页面没被别人改过。大段重写或追加内容用 write_page。修改标题、图标、属性用 update_page。",
		Schema: obj(map[string]any{
			"page_id": str("页面 id"),
			"edits": arr(obj(map[string]any{
				"old_string":  str("要替换的原文（逐字匹配）"),
				"new_string":  str("替换成的新文本；空串表示删除"),
				"replace_all": boolean("替换所有出现（默认 false）"),
			}, "old_string", "new_string"), "按顺序执行的替换列表"),
		}, "page_id", "edits"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID string `json:"page_id"`
				Edits  []struct {
					Old string `json:"old_string"`
					New string `json:"new_string"`
					All bool   `json:"replace_all"`
				} `json:"edits"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if len(in.Edits) == 0 {
				return nil, fail("edits 不能为空")
			}
			meta, err := env.NeedPage(in.PageID, model.RoleEditor)
			if err != nil {
				return nil, err
			}
			md, p, nodes, err := env.PageMarkdown(meta.ID)
			if err != nil {
				return nil, err
			}
			if err := env.checkFresh(p); err != nil {
				return nil, err
			}
			cur := md
			total := 0
			for i, e := range in.Edits {
				if e.Old == "" {
					if cur != "" {
						return nil, fail("第 %d 处修改的 old_string 为空；向空页面写内容请用 write_page", i+1)
					}
					cur = e.New
					total++
					continue
				}
				if e.Old == e.New {
					return nil, fail("第 %d 处修改前后内容相同", i+1)
				}
				n := strings.Count(cur, e.Old)
				if n == 0 {
					return nil, fail("第 %d 处修改：old_string 在正文里找不到（必须逐字一致，包括空格、换行与 Markdown 标记）。%s", i+1, nearHint(cur, e.Old))
				}
				if n > 1 && !e.All {
					return nil, fail("第 %d 处修改：old_string 在正文里出现了 %d 次，请加入更多上下文使其唯一，或设置 replace_all", i+1, n)
				}
				if e.All {
					cur = strings.ReplaceAll(cur, e.Old, e.New)
					total += n
				} else {
					cur = strings.Replace(cur, e.Old, e.New, 1)
					total++
				}
			}
			newNodes, err := lnmd.Reconcile(nodes, cur, env.RenderOpts())
			if err != nil {
				return nil, fail("修改后的 Markdown 无法解析：%v", err)
			}
			changed, rev, err := env.replaceContent(p.ID, newNodes)
			if err != nil {
				return nil, err
			}
			env.Reads.Mark(p.ID, rev)
			snippet := ""
			for _, e := range in.Edits {
				if e.New != "" {
					snippet = e.New
				}
			}
			return &Result{Text: fmt.Sprintf("已修改页面「%s」：替换 %d 处，变化 %d 个块。%s", titleOr(p.Title), total, changed, previewOf(snippet)),
				Data: map[string]any{"page_id": p.ID, "title": p.Title, "changed_blocks": changed, "edits": in.Edits}}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "write_page",
		Title: "写入页面",
		Description: "用增强 Markdown 写页面正文：mode=replace 整页替换（未变化的块会被识别并保留）；append 追加到末尾；prepend 插到开头；" +
			"insert_after 插到包含 anchor 文本的那个块之后。整页替换前必须先 read_page。小范围修改优先用 edit_page。",
		Schema: obj(map[string]any{
			"page_id": str("页面 id"),
			"mode":    enum("写入方式", "replace", "append", "prepend", "insert_after"),
			"content": str("增强 Markdown 内容"),
			"anchor":  str("mode=insert_after 时，定位用的一段原文（取所在块，插到它后面）"),
		}, "page_id", "mode", "content"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID  string `json:"page_id"`
				Mode    string `json:"mode"`
				Content string `json:"content"`
				Anchor  string `json:"anchor"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.PageID, model.RoleEditor)
			if err != nil {
				return nil, err
			}
			nodes, p, err := env.LoadNodes(meta.ID)
			if err != nil {
				return nil, err
			}
			if meta.Kind == model.KindDatabase {
				return nil, fail("数据库页面没有正文，请用数据库工具")
			}
			var desired []lnmd.Node
			switch in.Mode {
			case "replace":
				if err := env.checkFresh(p); err != nil && len(nodes) > 0 {
					return nil, err
				}
				desired, err = lnmd.Reconcile(nodes, in.Content, env.RenderOpts())
				if err != nil {
					return nil, fail("Markdown 无法解析：%v", err)
				}
			case "append", "prepend", "insert_after":
				add, err := lnmd.Parse(in.Content)
				if err != nil {
					return nil, fail("Markdown 无法解析：%v", err)
				}
				nodes = trimTrailingEmpty(nodes)
				switch in.Mode {
				case "append":
					desired = append(append([]lnmd.Node{}, nodes...), add...)
				case "prepend":
					desired = append(append([]lnmd.Node{}, add...), nodes...)
				default:
					idx := -1
					for i, n := range nodes {
						if strings.Contains(lnmd.RenderBlock(n, env.RenderOpts()), in.Anchor) {
							idx = i
							break
						}
					}
					if in.Anchor == "" || idx < 0 {
						return nil, fail("找不到包含 anchor 的块")
					}
					desired = append(append(append([]lnmd.Node{}, nodes[:idx+1]...), add...), nodes[idx+1:]...)
				}
			default:
				return nil, fail("未知的 mode：%s", in.Mode)
			}
			changed, rev, err := env.replaceContent(p.ID, desired)
			if err != nil {
				return nil, err
			}
			env.Reads.Mark(p.ID, rev)
			return &Result{Text: fmt.Sprintf("已写入页面「%s」，变化 %d 个块。", titleOr(p.Title), changed), Data: map[string]any{"page_id": p.ID, "title": p.Title, "changed_blocks": changed}}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "create_pages",
		Title: "新建页面",
		Description: "批量新建页面。parent_id 是上级页面（为空则建在 space_id 指定空间的根目录，都为空则建在个人空间根目录）。" +
			"content 用增强 Markdown。上级是普通页面时，默认会在上级正文末尾加一个子页面链接块（link_in_parent=false 可关闭）。" +
			"往数据库里加行请用 upsert_rows。",
		Schema: obj(map[string]any{
			"pages": arr(obj(map[string]any{
				"parent_id":      str("上级页面 id（可选）"),
				"space_id":       str("空间 id（没有 parent_id 时使用，可选）"),
				"title":          str("标题"),
				"icon":           str("图标：可以是一个字符，或 icon:<名字>，例如 icon:book"),
				"content":        str("增强 Markdown 正文（可选）"),
				"after_id":       str("插在哪个兄弟页面之后（可选，默认末尾）"),
				"link_in_parent": boolean("是否在上级正文里加子页面链接（默认 true）"),
			}, "title"), "要新建的页面"),
		}, "pages"),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Pages []struct {
					ParentID string `json:"parent_id"`
					SpaceID  string `json:"space_id"`
					Title    string `json:"title"`
					Icon     string `json:"icon"`
					Content  string `json:"content"`
					AfterID  string `json:"after_id"`
					Link     *bool  `json:"link_in_parent"`
				} `json:"pages"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if len(in.Pages) == 0 || len(in.Pages) > 100 {
				return nil, fail("一次新建 1-100 个页面")
			}
			var lines []string
			var created []map[string]any
			for _, pg := range in.Pages {
				spaceID := pg.SpaceID
				if pg.ParentID != "" {
					parent, err := env.NeedPage(pg.ParentID, model.RoleEditor)
					if err != nil {
						return nil, err
					}
					if parent.Kind == model.KindDatabase {
						return nil, fail("「%s」是数据库，往数据库里加行请用 upsert_rows", parent.Title)
					}
					spaceID = parent.SpaceID
				} else {
					if spaceID == "" {
						spaceID = env.DefaultSpace()
					}
					if !model.RoleAtLeast(env.SpaceRole(spaceID), model.RoleEditor) {
						return nil, fail("没有在空间 %s 新建页面的权限", spaceID)
					}
				}
				var blocks []pages.BlockDoc
				if strings.TrimSpace(pg.Content) != "" {
					nodes, err := lnmd.Parse(pg.Content)
					if err != nil {
						return nil, fail("「%s」的 Markdown 无法解析：%v", pg.Title, err)
					}
					if blocks, err = NodesToBlocks(nodes); err != nil {
						return nil, err
					}
				}
				var newID string
				_, err := env.Svc.Store.Write(env.Ctx, env.User.ID, func(tx *store.Tx) error {
					tx.SetOrigin(env.Origin)
					p, err := env.Svc.Pages.CreateTx(tx, env.User.ID, pages.CreateInput{SpaceID: spaceID, ParentID: pg.ParentID, Title: pg.Title, Icon: pg.Icon, AfterID: pg.AfterID, Blocks: blocks})
					if err != nil {
						return err
					}
					newID = p.ID
					return nil
				})
				if err != nil {
					return nil, err
				}
				if env.Created != nil {
					env.Created(newID)
				}
				if pg.ParentID != "" && (pg.Link == nil || *pg.Link) {
					_ = env.appendChildLink(pg.ParentID, newID)
				}
				lines = append(lines, fmt.Sprintf("- %s（id：%s，路径：%s）", titleOr(pg.Title), newID, env.PathOf(newID)))
				created = append(created, map[string]any{"id": newID, "title": pg.Title})
				env.Reads.Mark(newID, 0)
			}
			return &Result{Text: "已新建：\n" + strings.Join(lines, "\n"), Data: map[string]any{"created": created}}, nil
		},
	})
}

// checkFresh 先读后改：本会话读过该页面，且读取后没被改过。
func (env *Env) checkFresh(p *model.Page) error {
	rev, ok := env.Reads.Get(p.ID)
	if !ok {
		return fail("修改前请先用 read_page 读取页面「%s」", titleOr(p.Title))
	}
	if rev != p.ContentRev {
		return fail("页面「%s」在你读取之后被修改过，请先用 read_page 重新读取最新内容再修改", titleOr(p.Title))
	}
	return nil
}

// replaceContent 用新节点替换页面正文，返回变化块数与新版本号。
func (env *Env) replaceContent(pageID string, nodes []lnmd.Node) (int, int64, error) {
	if err := env.beforeWrite(pageID); err != nil {
		return 0, 0, err
	}
	blocks, err := NodesToBlocks(nodes)
	if err != nil {
		return 0, 0, err
	}
	var changed int
	var rev int64
	_, err = env.Svc.Store.Write(env.Ctx, env.User.ID, func(tx *store.Tx) error {
		tx.SetOrigin(env.Origin)
		n, err := env.Svc.Pages.ReplaceBlocksTx(tx, env.User.ID, pageID, blocks)
		if err != nil {
			return err
		}
		changed = n
		return tx.QueryRow("SELECT content_rev FROM pages WHERE id = ?", pageID).Scan(&rev)
	})
	return changed, rev, err
}

// appendChildLink 在上级页面正文末尾追加子页面链接块。
func (env *Env) appendChildLink(parentID, childID string) error {
	nodes, _, err := env.LoadNodes(parentID)
	if err != nil {
		return err
	}
	nodes = trimTrailingEmpty(nodes)
	nodes = append(nodes, lnmd.Node{Type: "childPage", Attrs: map[string]any{"pageId": childID}})
	_, _, err = env.replaceContent(parentID, nodes)
	return err
}

func trimTrailingEmpty(nodes []lnmd.Node) []lnmd.Node {
	for len(nodes) > 0 {
		last := nodes[len(nodes)-1]
		if last.Type == "paragraph" && len(last.Content) == 0 {
			nodes = nodes[:len(nodes)-1]
			continue
		}
		break
	}
	return nodes
}

func previewOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) > 300 {
		s = string([]rune(s)[:300]) + "…"
	}
	return "\n新内容片段：\n" + s
}

// nearHint 找不到原文时，给出最相近的一行帮助模型纠正。
func nearHint(doc, needle string) string {
	first := strings.TrimSpace(strings.SplitN(needle, "\n", 2)[0])
	if first == "" {
		return "请重新 read_page 后复制原文。"
	}
	key := []rune(first)
	if len(key) > 12 {
		key = key[:12]
	}
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, string(key)) {
			return "最接近的一行是：「" + truncate(line, 200) + "」"
		}
	}
	return "请重新 read_page 后复制原文。"
}

func (env *Env) rowProps(p *model.Page) string {
	if p.DatabaseID == nil {
		return ""
	}
	if f, ok := env.Svc.DB.(interface{ RowPropsMarkdown(pageID string) string }); ok {
		return f.RowPropsMarkdown(p.ID)
	}
	return ""
}
