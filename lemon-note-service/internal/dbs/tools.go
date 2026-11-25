package dbs

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
)

func svcOf(env *tools.Env) (*Service, error) {
	s, ok := env.Svc.DB.(*Service)
	if !ok || s == nil {
		return nil, fmt.Errorf("数据库模块不可用")
	}
	return s, nil
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
func num(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }
func anyOf(d string) map[string]any {
	return map[string]any{"description": d}
}

const propertySchemaDesc = "属性定义：{name, type, options?: [{name, color?, group?}], format?, expression?（公式）, database_id?（关联）, two_way?, relation?/target?/function?（汇总）, prefix?（编号）}。" +
	"type 取值：text number select multi_select status date person files checkbox url email phone formula relation rollup created_time created_by last_edited_time last_edited_by unique_id。" +
	"公式语法兼容 Notion：prop(\"数量\") * 2、if(prop(\"完成\"), \"是\", \"否\")、dateBetween(prop(\"截止\"), now(), \"days\")。汇总 function：count sum average min max median range earliest latest show_original percent_empty checked。"

func bindArgs(args json.RawMessage, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("参数格式不正确：%v", err)
	}
	return nil
}

func init() {
	tools.Default.Register(&tools.Tool{
		Name:  "create_database",
		Title: "新建数据库",
		Description: "新建一个数据库（Notion 式表格：每行是一个页面，带属性）。parent_id 为空时建在空间根目录。inline=true 时同时在上级页面正文末尾插入内嵌数据库块。" +
			"标题属性自动存在，不用定义。" + propertySchemaDesc,
		Schema: obj(map[string]any{
			"parent_id":  str("上级页面 id（可选）"),
			"space_id":   str("空间 id（可选）"),
			"title":      str("数据库标题"),
			"icon":       str("图标（可选）"),
			"inline":     map[string]any{"type": "boolean", "description": "是否内嵌在上级页面正文里"},
			"title_name": str("标题属性的名称（默认「名称」）"),
			"properties": map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "属性定义列表"},
			"views":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "额外的视图：{name, type: table|board|list|gallery|calendar|timeline|chart, group_by?, date_prop?}"},
		}, "title"),
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			var in struct {
				ParentID  string     `json:"parent_id"`
				SpaceID   string     `json:"space_id"`
				Title     string     `json:"title"`
				Icon      string     `json:"icon"`
				Inline    bool       `json:"inline"`
				TitleName string     `json:"title_name"`
				Props     []Property `json:"properties"`
				Views     []struct {
					Name     string `json:"name"`
					Type     string `json:"type"`
					GroupBy  string `json:"group_by"`
					DateProp string `json:"date_prop"`
				} `json:"views"`
			}
			if err := bindArgs(args, &in); err != nil {
				return nil, err
			}
			spaceID := in.SpaceID
			if in.ParentID != "" {
				m, err := env.NeedPage(in.ParentID, model.RoleEditor)
				if err != nil {
					return nil, err
				}
				spaceID = m.SpaceID
			} else {
				if spaceID == "" {
					spaceID = env.DefaultSpace()
				}
				if !model.RoleAtLeast(env.SpaceRole(spaceID), model.RoleEditor) {
					return nil, fmt.Errorf("没有在该空间新建的权限")
				}
			}
			tn := in.TitleName
			if tn == "" {
				tn = "名称"
			}
			sc := &Schema{Properties: []Property{{ID: "title", Name: tn, Type: TTitle}}}
			zh := env.User.Locale != "en"
			for _, p := range in.Props {
				p.ID = ""
				if p.Type == TStatus && len(p.Options) == 0 {
					p.Options = DefaultStatusOptions(zh)
				}
				if p.Type == TRelation && p.DatabaseID != "" {
					if _, err := env.NeedPage(p.DatabaseID, model.RoleViewer); err != nil {
						return nil, err
					}
				}
				sc.Properties = append(sc.Properties, p)
			}
			var pageID string
			var views []string
			_, err = env.Svc.Store.Write(env.Ctx, env.User.ID, func(tx *store.Tx) error {
				tx.SetOrigin(env.Origin)
				p, v, err := s.CreateTx(tx, env.User.ID, CreateInput{SpaceID: spaceID, ParentID: in.ParentID, Title: in.Title, Icon: in.Icon, Inline: in.Inline, Schema: sc, Zh: zh})
				if err != nil {
					return err
				}
				pageID = p.ID
				views = append(views, v.ID)
				for _, vw := range in.Views {
					cfg := ViewConfig{}
					if vw.GroupBy != "" {
						if gp := sc.Find(vw.GroupBy); gp != nil {
							cfg.GroupBy = &GroupBy{Prop: gp.ID}
						}
					}
					if vw.DateProp != "" {
						if dp := sc.Find(vw.DateProp); dp != nil {
							cfg.DateProp = dp.ID
						}
					}
					if (vw.Type == "board") && cfg.GroupBy == nil {
						for _, p := range sc.Properties {
							if p.Type == TStatus || p.Type == TSelect {
								cfg.GroupBy = &GroupBy{Prop: p.ID}
								break
							}
						}
					}
					nv, err := s.addViewTx(tx, env.User.ID, p.ID, "", vw.Name, vw.Type, cfg)
					if err != nil {
						return err
					}
					views = append(views, nv.ID)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if env.Created != nil {
				env.Created(pageID)
			}
			if in.Inline && in.ParentID != "" {
				nodes, _, err := env.LoadNodes(in.ParentID)
				if err == nil {
					nodes = append(nodes, lnmd.Node{Type: "childDatabase", Attrs: map[string]any{"pageId": pageID, "viewId": views[0]}})
					blocks, _ := tools.NodesToBlocks(nodes)
					_ = env.BeforeWriteSafe(in.ParentID)
					_, _ = env.Svc.Store.Write(env.Ctx, env.User.ID, func(tx *store.Tx) error {
						_, err := env.Svc.Pages.ReplaceBlocksTx(tx, env.User.ID, in.ParentID, blocks)
						return err
					})
				}
			}
			info, _ := s.Info(env.Ctx, pageID)
			return &tools.Result{Text: fmt.Sprintf("已新建数据库「%s」（id：%s）。结构：\n%s", in.Title, pageID, SchemaMarkdown(info.Schema)), Data: map[string]any{"page_id": pageID}}, nil
		},
	})

	tools.Default.Register(&tools.Tool{
		Name:  "update_database",
		Title: "修改数据库结构",
		Description: "修改数据库的属性：add（新增属性）、update（改名、改类型、改选项、改公式，按属性名或 id 指定）、delete（删除属性）、rename（改数据库标题）。" +
			"改选项时传完整的 options 列表（已有选项保留 name 即可）。" + propertySchemaDesc,
		Schema: obj(map[string]any{
			"database_id": str("数据库 id"),
			"add":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "新增的属性定义"},
			"update":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "修改：{property: 名称或id, name?, type?, options?, expression?, format?, ...}"},
			"delete":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要删除的属性名称或 id"},
			"title":       str("新的数据库标题（可选）"),
		}, "database_id"),
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			var in struct {
				DB     string           `json:"database_id"`
				Add    []Property       `json:"add"`
				Update []map[string]any `json:"update"`
				Delete []string         `json:"delete"`
				Title  *string          `json:"title"`
			}
			if err := bindArgs(args, &in); err != nil {
				return nil, err
			}
			if _, err := env.NeedPage(in.DB, model.RoleEditor); err != nil {
				return nil, err
			}
			info, err := s.Info(env.Ctx, in.DB)
			if err != nil {
				return nil, err
			}
			sc := info.Schema
			zh := env.User.Locale != "en"
			for _, p := range in.Add {
				p.ID = ""
				if p.Type == TStatus && len(p.Options) == 0 {
					p.Options = DefaultStatusOptions(zh)
				}
				sc.Properties = append(sc.Properties, p)
			}
			for _, u := range in.Update {
				key, _ := u["property"].(string)
				p := sc.Find(key)
				if p == nil {
					return nil, fmt.Errorf("没有属性「%s」", key)
				}
				delete(u, "property")
				// 选项按名称保留原 id
				if opts, ok := u["options"].([]any); ok {
					var next []Option
					for _, o := range opts {
						m, _ := o.(map[string]any)
						name, _ := m["name"].(string)
						if name == "" {
							if s, ok := o.(string); ok {
								name = s
							}
						}
						old := p.OptionByName(name)
						nopt := Option{Name: name}
						if old != nil {
							nopt = *old
						}
						if c, ok := m["color"].(string); ok {
							nopt.Color = c
						}
						if g, ok := m["group"].(string); ok {
							nopt.Group = g
						}
						next = append(next, nopt)
					}
					p.Options = next
					delete(u, "options")
				}
				b, _ := json.Marshal(u)
				id := p.ID
				if err := json.Unmarshal(b, p); err != nil {
					return nil, fmt.Errorf("属性「%s」的修改内容不正确", key)
				}
				p.ID = id
			}
			for _, d := range in.Delete {
				p := sc.Find(d)
				if p == nil {
					return nil, fmt.Errorf("没有属性「%s」", d)
				}
				if p.Type == TTitle {
					return nil, fmt.Errorf("标题属性不能删除")
				}
				var next []Property
				for _, x := range sc.Properties {
					if x.ID != p.ID {
						next = append(next, x)
					}
				}
				sc.Properties = next
			}
			if err := env.BeforeWriteSafe(in.DB); err != nil {
				return nil, err
			}
			out, err := s.UpdateSchema(env.Ctx, env.User.ID, in.DB, sc)
			if err != nil {
				return nil, err
			}
			if in.Title != nil {
				_, _ = env.Svc.Pages.Update(env.Ctx, env.User.ID, in.DB, pagesPatchTitle(*in.Title))
			}
			return &tools.Result{Text: "已更新数据库结构：\n" + SchemaMarkdown(out)}, nil
		},
	})

	tools.Default.Register(&tools.Tool{
		Name:  "query_database",
		Title: "查询数据库",
		Description: "查询数据库的结构与行，返回 Markdown 表格（含每行 id）。可以用 view_id 套用某个视图的筛选排序，也可以自己给 filter / sorts / search。" +
			"filter 格式：{\"and\":[{\"prop\":\"状态\",\"op\":\"is\",\"value\":\"进行中\"},{\"prop\":\"截止\",\"op\":\"before\",\"value\":\"2026-12-01\"}]}，也可以用 or 嵌套。" +
			"op：is is_not contains not_contains starts_with ends_with > < >= <= before after on_or_before on_or_after is_empty is_not_empty；日期可用 today this_week this_month past_week next_week 等相对值。" +
			"读某一行的正文请用 read_page。",
		Schema: obj(map[string]any{
			"database_id": str("数据库 id"),
			"view_id":     str("套用的视图 id（可选）"),
			"filter":      map[string]any{"type": "object", "description": "筛选条件"},
			"sorts":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "[{prop, dir: asc|desc}]"},
			"search":      str("在所有属性里模糊查找（可选）"),
			"properties":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "只返回这些属性（可选）"},
			"limit":       num("最多返回多少行（默认 100，最多 500）"),
			"offset":      num("跳过多少行（分页）"),
		}, "database_id"),
		ReadOnly: true,
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			var in struct {
				DB     string   `json:"database_id"`
				ViewID string   `json:"view_id"`
				Filter *Filter  `json:"filter"`
				Sorts  []Sort   `json:"sorts"`
				Search string   `json:"search"`
				Props  []string `json:"properties"`
				Limit  int      `json:"limit"`
				Offset int      `json:"offset"`
			}
			if err := bindArgs(args, &in); err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.DB, model.RoleViewer)
			if err != nil {
				return nil, err
			}
			if in.Limit <= 0 || in.Limit > 500 {
				in.Limit = 100
			}
			t, res, err := s.Query(env.Ctx, in.DB, QueryInput{ViewID: in.ViewID, Filter: in.Filter, Sorts: in.Sorts, Search: in.Search, Limit: in.Limit, Offset: in.Offset})
			if err != nil {
				return nil, err
			}
			rows := res.Rows
			for _, g := range res.Groups {
				rows = append(rows, g.Rows...)
			}
			cols := t.Schema.Properties
			if len(in.Props) > 0 {
				var sel []Property
				sel = append(sel, *t.Schema.Title())
				for _, n := range in.Props {
					if p := t.Schema.Find(n); p != nil && p.Type != TTitle {
						sel = append(sel, *p)
					}
				}
				cols = sel
			}
			var b strings.Builder
			fmt.Fprintf(&b, "# 数据库「%s」（id：%s）\n属性：\n%s", titleOr(meta.Title), meta.ID, SchemaMarkdown(t.Schema))
			views, _ := s.Views(env.Ctx, in.DB)
			b.WriteString("视图：")
			for i, v := range views {
				if i > 0 {
					b.WriteString("；")
				}
				fmt.Fprintf(&b, "%s（%s，id：%s）", v.Name, v.Type, v.ID)
			}
			fmt.Fprintf(&b, "\n\n共 %d 行符合条件，", res.Total)
			if len(rows) < res.Total {
				fmt.Fprintf(&b, "这里是第 %d-%d 行。\n\n", in.Offset+1, in.Offset+len(rows))
			} else {
				b.WriteString("全部列出。\n\n")
			}
			b.WriteString("| id |")
			for _, c := range cols {
				b.WriteString(" " + esc(c.Name) + " |")
			}
			b.WriteString("\n|---|")
			for range cols {
				b.WriteString("---|")
			}
			b.WriteString("\n")
			for _, r := range rows {
				b.WriteString("| " + r.ID + " |")
				for i := range cols {
					v := ToText(t.Value(r, &cols[i]))
					b.WriteString(" " + esc(v) + " |")
				}
				b.WriteString("\n")
			}
			return &tools.Result{Text: b.String()}, nil
		},
	})

	tools.Default.Register(&tools.Tool{
		Name:  "upsert_rows",
		Title: "写入数据库行",
		Description: "批量新建或修改数据库的行。每项：有 row_id 表示修改该行，否则新建。properties 按属性名写人类可读的值：单选 / 状态写选项名，" +
			"多选写名称数组，日期写 2026-11-25 或 {start, end}，人员写用户名，关联写对方行的 id 或标题，复选框写 true/false。不存在的单选 / 多选选项会自动新建。" +
			"content 是新行的正文（增强 Markdown，可选）。",
		Schema: obj(map[string]any{
			"database_id": str("数据库 id"),
			"rows": map[string]any{"type": "array", "description": "要写入的行", "items": obj(map[string]any{
				"row_id":     str("要修改的行 id（新建时不填）"),
				"title":      str("标题"),
				"properties": map[string]any{"type": "object", "description": "属性名 → 值"},
				"content":    str("新行的正文（可选）"),
			})},
		}, "database_id", "rows"),
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			var in struct {
				DB   string `json:"database_id"`
				Rows []struct {
					RowID   string         `json:"row_id"`
					Title   *string        `json:"title"`
					Props   map[string]any `json:"properties"`
					Content string         `json:"content"`
				} `json:"rows"`
			}
			if err := bindArgs(args, &in); err != nil {
				return nil, err
			}
			if _, err := env.NeedPage(in.DB, model.RoleEditor); err != nil {
				return nil, err
			}
			if len(in.Rows) == 0 || len(in.Rows) > 500 {
				return nil, fmt.Errorf("一次写入 1-500 行")
			}
			created, updated := 0, 0
			var newIDs []string
			for _, r := range in.Rows {
				props := rawProps(r.Props)
				if r.RowID != "" {
					m, err := env.NeedPage(r.RowID, model.RoleEditor)
					if err != nil {
						return nil, err
					}
					if m.DatabaseID != in.DB {
						return nil, fmt.Errorf("行 %s 不属于这个数据库", r.RowID)
					}
					if r.Title != nil {
						props["title"] = *r.Title
					}
					if err := env.BeforeWriteSafe(r.RowID); err != nil {
						return nil, err
					}
					_, err = env.Svc.Store.Write(env.Ctx, env.User.ID, func(tx *store.Tx) error {
						tx.SetOrigin(env.Origin)
						return s.SetPropsTx(tx, env.User.ID, r.RowID, props)
					})
					if err != nil {
						return nil, err
					}
					updated++
					continue
				}
				var blocks []pagesBlock
				if strings.TrimSpace(r.Content) != "" {
					nodes, err := lnmd.Parse(r.Content)
					if err != nil {
						return nil, fmt.Errorf("正文无法解析：%v", err)
					}
					blocks, _ = tools.NodesToBlocks(nodes)
				}
				title := ""
				if r.Title != nil {
					title = *r.Title
				}
				var newID string
				_, err := env.Svc.Store.Write(env.Ctx, env.User.ID, func(tx *store.Tx) error {
					tx.SetOrigin(env.Origin)
					p, err := s.CreateRowTx(tx, env.User.ID, in.DB, title, props, "", blocks, false)
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
				newIDs = append(newIDs, newID)
				created++
			}
			return &tools.Result{Text: fmt.Sprintf("完成：新建 %d 行，修改 %d 行。新行 id：%s", created, updated, strings.Join(newIDs, "、")), Data: map[string]any{"created": newIDs}}, nil
		},
	})

	tools.Default.Register(&tools.Tool{
		Name:  "manage_views",
		Title: "管理视图",
		Description: "新建、修改或删除数据库视图。type：table 表格、board 看板（按 group_by 分列）、list 列表、gallery 画廊、calendar 日历（需要 date_prop）、timeline 时间线（date_prop，可选 end_prop）、chart 图表。" +
			"config 里可设 filter、sorts、group_by（{prop}）、visible（显示的属性名或 id 列表）、date_prop、end_prop、chart（{type: bar|line|pie|area, x, y:[{property, agg}]}）。",
		Schema: obj(map[string]any{
			"database_id": str("数据库 id"),
			"action":      map[string]any{"type": "string", "enum": []string{"create", "update", "delete"}},
			"view_id":     str("修改 / 删除时的视图 id"),
			"name":        str("视图名称"),
			"type":        str("视图类型"),
			"config":      map[string]any{"type": "object", "description": "视图配置"},
		}, "database_id", "action"),
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			s, err := svcOf(env)
			if err != nil {
				return nil, err
			}
			var in struct {
				DB     string          `json:"database_id"`
				Action string          `json:"action"`
				ViewID string          `json:"view_id"`
				Name   string          `json:"name"`
				Type   string          `json:"type"`
				Config json.RawMessage `json:"config"`
			}
			if err := bindArgs(args, &in); err != nil {
				return nil, err
			}
			if _, err := env.NeedPage(in.DB, model.RoleEditor); err != nil {
				return nil, err
			}
			info, err := s.Info(env.Ctx, in.DB)
			if err != nil {
				return nil, err
			}
			var cfg *ViewConfig
			if len(in.Config) > 0 {
				var c ViewConfig
				if err := json.Unmarshal(in.Config, &c); err != nil {
					return nil, fmt.Errorf("config 格式不正确：%v", err)
				}
				resolveConfig(info.Schema, &c)
				cfg = &c
			}
			switch in.Action {
			case "create":
				c := ViewConfig{}
				if cfg != nil {
					c = *cfg
				}
				v, err := s.AddView(env.Ctx, env.User.ID, in.DB, "", in.Name, in.Type, c)
				if err != nil {
					return nil, err
				}
				return &tools.Result{Text: fmt.Sprintf("已新建视图「%s」（id：%s）", v.Name, v.ID)}, nil
			case "update":
				var name, typ *string
				if in.Name != "" {
					name = &in.Name
				}
				if in.Type != "" {
					typ = &in.Type
				}
				if _, _, err := s.View(env.Ctx, in.ViewID); err != nil {
					return nil, err
				}
				if _, err := s.UpdateView(env.Ctx, env.User.ID, in.ViewID, name, typ, cfg, nil); err != nil {
					return nil, err
				}
				return &tools.Result{Text: "已更新视图。"}, nil
			case "delete":
				if err := s.DeleteView(env.Ctx, env.User.ID, in.ViewID); err != nil {
					return nil, err
				}
				return &tools.Result{Text: "已删除视图。"}, nil
			}
			return nil, fmt.Errorf("未知的 action：%s", in.Action)
		},
	})
}

// resolveConfig 把视图配置里的属性名换成属性 id。
func resolveConfig(sc *Schema, c *ViewConfig) {
	id := func(k string) string {
		if p := sc.Find(k); p != nil {
			return p.ID
		}
		return k
	}
	var fix func(f *Filter)
	fix = func(f *Filter) {
		if f == nil {
			return
		}
		if f.Prop != "" {
			f.Prop = id(f.Prop)
		}
		for i := range f.And {
			fix(&f.And[i])
		}
		for i := range f.Or {
			fix(&f.Or[i])
		}
	}
	fix(c.Filter)
	for i := range c.Sorts {
		c.Sorts[i].Prop = id(c.Sorts[i].Prop)
	}
	if c.GroupBy != nil {
		c.GroupBy.Prop = id(c.GroupBy.Prop)
	}
	for i := range c.Visible {
		c.Visible[i] = id(c.Visible[i])
	}
	if c.DateProp != "" {
		c.DateProp = id(c.DateProp)
	}
	if c.EndProp != "" {
		c.EndProp = id(c.EndProp)
	}
}

func esc(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:200]) + "…"
	}
	return s
}

func titleOr(t string) string {
	if strings.TrimSpace(t) == "" {
		return "无标题"
	}
	return t
}
