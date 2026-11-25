package dbs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/fracidx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Service 是数据库服务。
type Service struct {
	st    *store.Store
	cat   *catalog.Catalog
	pages *pages.Service
}

func New(st *store.Store, cat *catalog.Catalog, pg *pages.Service) *Service {
	return &Service{st: st, cat: cat, pages: pg}
}

// 视图类型
var viewTypes = map[string]bool{"table": true, "board": true, "list": true, "gallery": true, "calendar": true, "timeline": true, "chart": true}

// Info 是数据库的完整信息。
type Info struct {
	Page   *model.Page    `json:"page"`
	Schema *Schema        `json:"schema"`
	Views  []ViewInfo     `json:"views"`
}

// ViewInfo 是视图。
type ViewInfo struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Position string     `json:"position"`
	HostID   string     `json:"host_id,omitempty"`
	Config   ViewConfig `json:"config"`
}

var ErrNotDB = errors.New("这个页面不是数据库")

// getDB 读结构（事务内或事务外）。
func (s *Service) getDB(q interface {
	Get(dest any, q string, args ...any) error
}, id string) (*model.Database, *Schema, error) {
	var d model.Database
	if err := q.Get(&d, "SELECT * FROM databases WHERE id = ?", id); err != nil {
		return nil, nil, ErrNotDB
	}
	return &d, ParseSchema(d.Schema), nil
}

type ctxGetter struct {
	ctx context.Context
	st  *store.Store
}

func (g ctxGetter) Get(dest any, q string, args ...any) error { return g.st.Get(g.ctx, dest, q, args...) }

// Info 读数据库信息。
func (s *Service) Info(ctx context.Context, id string) (*Info, error) {
	p, err := s.pages.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Kind != model.KindDatabase {
		return nil, ErrNotDB
	}
	_, sc, err := s.getDB(ctxGetter{ctx, s.st}, id)
	if err != nil {
		return nil, err
	}
	views, err := s.Views(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Info{Page: p, Schema: sc, Views: views}, nil
}

// Views 列出视图（按位置）。
func (s *Service) Views(ctx context.Context, dbID string) ([]ViewInfo, error) {
	var vs []model.DBView
	if err := s.st.Select(ctx, &vs, "SELECT * FROM db_views WHERE database_id = ?", dbID); err != nil {
		return nil, err
	}
	out := make([]ViewInfo, 0, len(vs))
	for _, v := range vs {
		vi := ViewInfo{ID: v.ID, Name: v.Name, Type: v.Type, Position: v.Position, HostID: model.Deref(v.HostID)}
		_ = json.Unmarshal([]byte(v.Config), &vi.Config)
		out = append(out, vi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out, nil
}

// CreateInput 新建数据库参数。
type CreateInput struct {
	SpaceID  string
	ParentID string
	Title    string
	Icon     string
	Inline   bool
	Schema   *Schema
	Zh       bool
	AfterID  string
}

// CreateTx 在事务里新建数据库（页面 + 结构 + 默认表格视图）。
func (s *Service) CreateTx(tx *store.Tx, actor string, in CreateInput) (*model.Page, *ViewInfo, error) {
	sc := in.Schema
	if sc == nil {
		sc = DefaultSchema(in.Zh)
	}
	if err := sc.Validate(); err != nil {
		return nil, nil, err
	}
	p, err := s.pages.CreateTx(tx, actor, pages.CreateInput{SpaceID: in.SpaceID, ParentID: in.ParentID, Kind: model.KindDatabase, Title: in.Title, Icon: in.Icon,
		AfterID: in.AfterID, Flags: model.PageFlags{Inline: in.Inline}})
	if err != nil {
		return nil, nil, err
	}
	b, _ := json.Marshal(sc)
	now := model.Now()
	if err := tx.Put("databases", model.Database{ID: p.ID, Schema: string(b), Settings: "{}", CreatedAt: now, UpdatedAt: now}); err != nil {
		return nil, nil, err
	}
	name := "表格"
	if !in.Zh {
		name = "Table"
	}
	v, err := s.addViewTx(tx, actor, p.ID, "", name, "table", ViewConfig{})
	return p, v, err
}

func (s *Service) addViewTx(tx *store.Tx, actor, dbID, hostID, name, typ string, cfg ViewConfig) (*ViewInfo, error) {
	if !viewTypes[typ] {
		return nil, fmt.Errorf("不支持的视图类型：%s", typ)
	}
	var last string
	_ = tx.QueryRow("SELECT COALESCE(max(position), '') FROM db_views WHERE database_id = ?", dbID).Scan(&last)
	pos, err := fracidx.Between(last, "")
	if err != nil {
		pos, _ = fracidx.Between("", "")
	}
	b, _ := json.Marshal(cfg)
	now := model.Now()
	v := model.DBView{ID: ids.New(), DatabaseID: dbID, HostID: model.StrPtr(hostID), Name: strings.TrimSpace(name), Type: typ, Position: pos, Config: string(b), CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	if v.Name == "" {
		v.Name = typ
	}
	if err := tx.Put("db_views", v); err != nil {
		return nil, err
	}
	return &ViewInfo{ID: v.ID, Name: v.Name, Type: typ, Position: pos, HostID: hostID, Config: cfg}, nil
}

// AddView 新增视图。
func (s *Service) AddView(ctx context.Context, actor, dbID, hostID, name, typ string, cfg ViewConfig) (*ViewInfo, error) {
	var out *ViewInfo
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		v, err := s.addViewTx(tx, actor, dbID, hostID, name, typ, cfg)
		out = v
		return err
	})
	return out, err
}

// UpdateView 修改视图（nil 表示不改）。
func (s *Service) UpdateView(ctx context.Context, actor, viewID string, name, typ *string, cfg *ViewConfig, position *string) (*ViewInfo, error) {
	var out ViewInfo
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var v model.DBView
		if err := tx.Get(&v, "SELECT * FROM db_views WHERE id = ?", viewID); err != nil {
			return errors.New("视图不存在")
		}
		if name != nil && strings.TrimSpace(*name) != "" {
			v.Name = strings.TrimSpace(*name)
		}
		if typ != nil {
			if !viewTypes[*typ] {
				return fmt.Errorf("不支持的视图类型：%s", *typ)
			}
			v.Type = *typ
		}
		if cfg != nil {
			b, _ := json.Marshal(cfg)
			v.Config = string(b)
		}
		if position != nil && fracidx.Validate(*position) == nil {
			v.Position = *position
		}
		v.UpdatedAt = model.Now()
		out = ViewInfo{ID: v.ID, Name: v.Name, Type: v.Type, Position: v.Position, HostID: model.Deref(v.HostID)}
		_ = json.Unmarshal([]byte(v.Config), &out.Config)
		return tx.Put("db_views", v)
	})
	return &out, err
}

// DeleteView 删除视图（至少保留一个）。
func (s *Service) DeleteView(ctx context.Context, actor, viewID string) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var v model.DBView
		if err := tx.Get(&v, "SELECT * FROM db_views WHERE id = ?", viewID); err != nil {
			return errors.New("视图不存在")
		}
		var n int
		_ = tx.QueryRow("SELECT count(*) FROM db_views WHERE database_id = ?", v.DatabaseID).Scan(&n)
		if n <= 1 {
			return errors.New("至少要保留一个视图")
		}
		return tx.Delete("db_views", viewID)
	})
	return err
}

// View 读单个视图。
func (s *Service) View(ctx context.Context, viewID string) (*ViewInfo, string, error) {
	var v model.DBView
	if err := s.st.Get(ctx, &v, "SELECT * FROM db_views WHERE id = ?", viewID); err != nil {
		return nil, "", errors.New("视图不存在")
	}
	vi := &ViewInfo{ID: v.ID, Name: v.Name, Type: v.Type, Position: v.Position, HostID: model.Deref(v.HostID)}
	_ = json.Unmarshal([]byte(v.Config), &vi.Config)
	return vi, v.DatabaseID, nil
}

// ---------- 加载与查询 ----------

func rowFromPage(p *model.Page) *Row {
	r := &Row{ID: p.ID, Title: p.Title, Icon: p.Icon, Position: p.Position, Props: map[string]any{}, CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy,
		UpdatedAt: p.UpdatedAt, UpdatedBy: p.UpdatedBy, Template: p.IsTemplate}
	if p.Props != "" {
		dec := json.NewDecoder(strings.NewReader(p.Props))
		dec.UseNumber()
		_ = dec.Decode(&r.Props)
		for k, v := range r.Props {
			if n, ok := v.(json.Number); ok {
				f, _ := n.Float64()
				r.Props[k] = f
			}
		}
	}
	return r
}

// Load 加载整张表（不含回收站里的行；templates 为真时只要模板）。
func (s *Service) Load(ctx context.Context, dbID string) (*Table, error) {
	return s.load(ctx, dbID, map[string]*Table{})
}

func (s *Service) load(ctx context.Context, dbID string, seen map[string]*Table) (*Table, error) {
	if t, ok := seen[dbID]; ok {
		return t, nil
	}
	p, err := s.pages.Get(ctx, dbID)
	if err != nil {
		return nil, err
	}
	_, sc, err := s.getDB(ctxGetter{ctx, s.st}, dbID)
	if err != nil {
		return nil, err
	}
	var ps []model.Page
	if err := s.st.Select(ctx, &ps, "SELECT * FROM pages WHERE database_id = ? AND trashed_at IS NULL", dbID); err != nil {
		return nil, err
	}
	rows := make([]*Row, 0, len(ps))
	for i := range ps {
		rows = append(rows, rowFromPage(&ps[i]))
	}
	t := NewTable(dbID, p.Title, sc, rows)
	seen[dbID] = t
	t.Related = func(id string) *Table {
		rt, err := s.load(ctx, id, seen)
		if err != nil {
			return nil
		}
		return rt
	}
	t.UserName = func(id string) string {
		if u, ok := s.cat.User(id); ok {
			return u.DisplayName
		}
		return id
	}
	return t, nil
}

// QueryInput 查询参数。
type QueryInput struct {
	ViewID   string   `json:"view_id"`
	Filter   *Filter  `json:"filter"`
	Sorts    []Sort   `json:"sorts"`
	GroupBy  *GroupBy `json:"group_by"`
	Search   string   `json:"search"`
	Limit    int      `json:"limit"`
	Offset   int      `json:"offset"`
	Template bool     `json:"templates"`
}

// QueryResult 查询结果。
type QueryResult struct {
	Rows   []*Row         `json:"rows"`
	Groups []*Group       `json:"groups,omitempty"`
	Total  int            `json:"total"`
	Calcs  map[string]any `json:"calcs,omitempty"`
}

// Query 按视图（或临时条件）查询。视图里的条件与临时条件叠加（与）。
func (s *Service) Query(ctx context.Context, dbID string, in QueryInput) (*Table, *QueryResult, error) {
	t, err := s.Load(ctx, dbID)
	if err != nil {
		return nil, nil, err
	}
	var cfg ViewConfig
	if in.ViewID != "" {
		v, vdb, err := s.View(ctx, in.ViewID)
		if err != nil || vdb != dbID {
			return nil, nil, errors.New("视图不存在")
		}
		cfg = v.Config
	}
	filter := cfg.Filter
	if in.Filter != nil {
		if filter != nil {
			filter = &Filter{And: []Filter{*filter, *in.Filter}}
		} else {
			filter = in.Filter
		}
	}
	sorts := cfg.Sorts
	if len(in.Sorts) > 0 {
		sorts = in.Sorts
	}
	var candidates []*Row
	for _, r := range t.Rows {
		if r.Template == in.Template {
			candidates = append(candidates, r)
		}
	}
	rows, err := t.Apply(candidates, filter, sorts, in.Search)
	if err != nil {
		return nil, nil, err
	}
	res := &QueryResult{Total: len(rows)}
	if len(cfg.Calculate) > 0 {
		res.Calcs = map[string]any{}
		for pid, fn := range cfg.Calculate {
			if p := t.Schema.Prop(pid); p != nil {
				res.Calcs[pid] = t.Calc(rows, p, fn)
			}
		}
	}
	gb := cfg.GroupBy
	if in.GroupBy != nil {
		gb = in.GroupBy
	}
	if gb != nil && gb.Prop != "" {
		groups, err := t.GroupRows(rows, gb)
		if err != nil {
			return nil, nil, err
		}
		for _, g := range groups {
			t.FillComputed(g.Rows)
		}
		res.Groups = groups
		res.Rows = []*Row{}
		return t, res, nil
	}
	if in.Offset > 0 {
		if in.Offset >= len(rows) {
			rows = nil
		} else {
			rows = rows[in.Offset:]
		}
	}
	if in.Limit > 0 && len(rows) > in.Limit {
		rows = rows[:in.Limit]
	}
	t.FillComputed(rows)
	if rows == nil {
		rows = []*Row{}
	}
	res.Rows = rows
	return t, res, nil
}

// ---------- 结构修改 ----------

// UpdateSchema 用新结构替换（校验、双向关联、类型转换）。
func (s *Service) UpdateSchema(ctx context.Context, actor, dbID string, next *Schema) (*Schema, error) {
	var out *Schema
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		sc, err := s.UpdateSchemaTx(tx, actor, dbID, next)
		out = sc
		return err
	})
	return out, err
}

// UpdateSchemaTx 事务内修改结构。
func (s *Service) UpdateSchemaTx(tx *store.Tx, actor, dbID string, next *Schema) (*Schema, error) {
	d, old, err := s.getDB(tx, dbID)
	if err != nil {
		return nil, err
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	// 双向关联：新增的双向关联在对方数据库补一个反向属性
	for i := range next.Properties {
		p := &next.Properties[i]
		if p.Type != TRelation {
			continue
		}
		if _, err := s.pages.Get(tx.Context(), p.DatabaseID); err != nil {
			return nil, fmt.Errorf("关联属性「%s」指向的数据库不存在", p.Name)
		}
		if p.TwoWay && p.RelatedProp == "" && p.DatabaseID != dbID {
			od, osc, err := s.getDB(tx, p.DatabaseID)
			if err != nil {
				return nil, err
			}
			title := "关联"
			if pg, err := s.pages.Get(tx.Context(), dbID); err == nil && pg.Title != "" {
				title = pg.Title
			}
			name := title
			for k := 2; osc.Find(name) != nil; k++ {
				name = fmt.Sprintf("%s %d", title, k)
			}
			rp := Property{ID: newPropID(), Name: name, Type: TRelation, DatabaseID: dbID, TwoWay: true, RelatedProp: p.ID}
			osc.Properties = append(osc.Properties, rp)
			p.RelatedProp = rp.ID
			b, _ := json.Marshal(osc)
			od.Schema = string(b)
			od.UpdatedAt = model.Now()
			if err := tx.Put("databases", od); err != nil {
				return nil, err
			}
		}
	}
	// 类型变化的属性：尽量把旧值转换成新类型
	for i := range next.Properties {
		np := &next.Properties[i]
		op := old.Prop(np.ID)
		if op == nil || op.Type == np.Type || Computed(np.Type) || np.Type == TTitle {
			continue
		}
		if err := s.convertValues(tx, actor, dbID, old, op, np); err != nil {
			return nil, err
		}
	}
	b, _ := json.Marshal(next)
	d.Schema = string(b)
	d.UpdatedAt = model.Now()
	if err := tx.Put("databases", d); err != nil {
		return nil, err
	}
	return next, nil
}

func (s *Service) convertValues(tx *store.Tx, actor, dbID string, oldSc *Schema, op, np *Property) error {
	var ps []model.Page
	if err := tx.Select(&ps, "SELECT * FROM pages WHERE database_id = ?", dbID); err != nil {
		return err
	}
	t := NewTable(dbID, "", oldSc, nil)
	for i := range ps {
		r := rowFromPage(&ps[i])
		v := t.Value(r, op)
		var in any
		switch x := v.(type) {
		case []Value:
			var parts []any
			for _, e := range x {
				parts = append(parts, ToText(e))
			}
			in = parts
			if np.Type != TMultiSelect && np.Type != TPerson && np.Type != TRelation {
				in = ToText(v)
			}
		case DateVal:
			in = x.Start.Format("2006-01-02")
		case nil:
			in = nil
		default:
			in = ToText(v)
			if np.Type == TCheckbox {
				in = x
			}
		}
		nv, _, err := NormalizeValue(np, in, Resolver{CreateOption: true})
		if err != nil {
			nv = nil
		}
		if nv == nil {
			delete(r.Props, np.ID)
		} else {
			r.Props[np.ID] = nv
		}
		b, _ := json.Marshal(r.Props)
		ps[i].Props = string(b)
		if err := tx.Put("pages", &ps[i]); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 行 ----------

// resolver 构造输入解析器。
func (s *Service) resolver(ctx context.Context) Resolver {
	return Resolver{
		CreateOption: true,
		User: func(n string) (string, bool) {
			n = strings.TrimPrefix(strings.TrimSpace(n), "@")
			for _, u := range s.cat.Users() {
				if u.ID == n || strings.EqualFold(u.Username, n) || u.DisplayName == n {
					return u.ID, true
				}
			}
			return "", false
		},
		Relation: func(db, n string) (string, bool) {
			if m, ok := s.cat.Page(n); ok && m.DatabaseID == db {
				return n, true
			}
			for _, p := range s.cat.Children(s.spaceOf(db), db) {
				if p.TrashedAt == 0 && strings.EqualFold(strings.TrimSpace(p.Title), strings.TrimSpace(n)) {
					return p.ID, true
				}
			}
			return "", false
		},
	}
}

func (s *Service) spaceOf(id string) string {
	m, _ := s.cat.Page(id)
	return m.SpaceID
}

// SetPropsTx 写行属性（keys 可以是属性 id 或名称；值是人类可读形式）。返回结构是否被修改。
func (s *Service) SetPropsTx(tx *store.Tx, actor, rowID string, props map[string]any) error {
	var p model.Page
	if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", rowID); err != nil {
		return pages.ErrNotFound
	}
	if p.DatabaseID == nil {
		return errors.New("这个页面不是数据库里的行")
	}
	dbID := *p.DatabaseID
	d, sc, err := s.getDB(tx, dbID)
	if err != nil {
		return err
	}
	r := rowFromPage(&p)
	rs := s.resolver(tx.Context())
	schemaChanged := false
	type relChange struct {
		prop          *Property
		before, after []string
	}
	var rels []relChange
	for key, val := range props {
		prop := sc.Find(key)
		if prop == nil {
			return fmt.Errorf("没有属性「%s」（现有：%s）", key, propNames(sc))
		}
		if prop.Type == TTitle {
			if s, ok := val.(string); ok {
				p.Title = strings.TrimSpace(s)
			}
			continue
		}
		if Computed(prop.Type) {
			return fmt.Errorf("属性「%s」是自动计算的，不能修改", prop.Name)
		}
		nv, changed, err := NormalizeValue(prop, val, rs)
		if err != nil {
			return err
		}
		schemaChanged = schemaChanged || changed
		if prop.Type == TRelation && prop.TwoWay && prop.RelatedProp != "" {
			rels = append(rels, relChange{prop: prop, before: asStrings(r.Props[prop.ID]), after: asStrings(nv)})
		}
		if nv == nil {
			delete(r.Props, prop.ID)
		} else {
			r.Props[prop.ID] = nv
		}
	}
	b, _ := json.Marshal(r.Props)
	p.Props = string(b)
	p.UpdatedAt, p.UpdatedBy = model.Now(), actor
	if err := tx.Put("pages", &p); err != nil {
		return err
	}
	if schemaChanged {
		sb, _ := json.Marshal(sc)
		d.Schema = string(sb)
		d.UpdatedAt = model.Now()
		if err := tx.Put("databases", d); err != nil {
			return err
		}
	}
	for _, rc := range rels {
		if err := s.syncReverse(tx, actor, rowID, rc.prop, rc.before, rc.after); err != nil {
			return err
		}
	}
	return nil
}

// syncReverse 双向关联：维护对方行里的反向属性。
func (s *Service) syncReverse(tx *store.Tx, actor, rowID string, prop *Property, before, after []string) error {
	in := func(l []string, x string) bool {
		for _, y := range l {
			if y == x {
				return true
			}
		}
		return false
	}
	touch := func(otherID string, add bool) error {
		var op model.Page
		if err := tx.Get(&op, "SELECT * FROM pages WHERE id = ?", otherID); err != nil {
			return nil
		}
		or := rowFromPage(&op)
		cur := asStrings(or.Props[prop.RelatedProp])
		var next []any
		for _, x := range cur {
			if x != rowID {
				next = append(next, x)
			}
		}
		if add {
			next = append(next, rowID)
		}
		if len(next) == 0 {
			delete(or.Props, prop.RelatedProp)
		} else {
			or.Props[prop.RelatedProp] = next
		}
		b, _ := json.Marshal(or.Props)
		op.Props = string(b)
		op.UpdatedAt, op.UpdatedBy = model.Now(), actor
		return tx.Put("pages", &op)
	}
	for _, x := range after {
		if !in(before, x) {
			if err := touch(x, true); err != nil {
				return err
			}
		}
	}
	for _, x := range before {
		if !in(after, x) {
			if err := touch(x, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func propNames(sc *Schema) string {
	var n []string
	for _, p := range sc.Properties {
		n = append(n, p.Name)
	}
	return strings.Join(n, "、")
}

// CreateRowTx 新建一行。
func (s *Service) CreateRowTx(tx *store.Tx, actor, dbID, title string, props map[string]any, afterID string, blocks []pages.BlockDoc, first bool) (*model.Page, error) {
	d, sc, err := s.getDB(tx, dbID)
	if err != nil {
		return nil, err
	}
	db, err := s.pages.Get(tx.Context(), dbID)
	if err != nil {
		return nil, err
	}
	// 编号属性
	d.RowCounter++
	initial := map[string]any{}
	for _, p := range sc.Properties {
		if p.Type == TUniqueID {
			initial[p.ID] = float64(d.RowCounter)
		}
	}
	d.UpdatedAt = model.Now()
	if err := tx.Put("databases", d); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(initial)
	p, err := s.pages.CreateTx(tx, actor, pages.CreateInput{SpaceID: db.SpaceID, ParentID: dbID, DatabaseID: dbID, Title: title, Props: b, AfterID: afterID, First: first, Blocks: blocks})
	if err != nil {
		return nil, err
	}
	if len(props) > 0 {
		if err := s.SetPropsTx(tx, actor, p.ID, props); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// MoveRow 调整行的手动顺序。
func (s *Service) MoveRowTx(tx *store.Tx, actor, rowID, afterID string, first bool) error {
	var p model.Page
	if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", rowID); err != nil || p.DatabaseID == nil {
		return errors.New("行不存在")
	}
	sibs := s.cat.Children(p.SpaceID, *p.DatabaseID)
	var keys []string
	idx := -1
	for _, sb := range sibs {
		if sb.ID == rowID || sb.TrashedAt != 0 {
			continue
		}
		keys = append(keys, sb.Position)
		if sb.ID == afterID {
			idx = len(keys) - 1
		}
	}
	var a, b string
	switch {
	case first || (afterID == "" && len(keys) > 0 && false):
		if len(keys) > 0 {
			b = keys[0]
		}
	case idx >= 0:
		a = keys[idx]
		if idx+1 < len(keys) {
			b = keys[idx+1]
		}
	default:
		if len(keys) > 0 {
			a = keys[len(keys)-1]
		}
	}
	pos, err := fracidx.Between(a, b)
	if err != nil {
		pos, err = fracidx.Between(a, "")
		if err != nil {
			return err
		}
	}
	p.Position = pos
	p.UpdatedAt, p.UpdatedBy = model.Now(), actor
	return tx.Put("pages", &p)
}

// RowPropsMarkdown 把行属性渲染成「- 属性：值」列表（给智能体读）。
func (s *Service) RowPropsMarkdown(pageID string) string {
	m, ok := s.cat.Page(pageID)
	if !ok || m.DatabaseID == "" {
		return ""
	}
	t, err := s.Load(context.Background(), m.DatabaseID)
	if err != nil {
		return ""
	}
	r := t.Row(pageID)
	if r == nil {
		return ""
	}
	var b strings.Builder
	for i := range t.Schema.Properties {
		p := &t.Schema.Properties[i]
		if p.Type == TTitle {
			continue
		}
		v := ToText(t.Value(r, p))
		if v == "" {
			v = "（空）"
		}
		fmt.Fprintf(&b, "  - %s：%s\n", p.Name, v)
	}
	return b.String()
}

// SchemaMarkdown 把结构渲染给模型看。
func SchemaMarkdown(sc *Schema) string {
	var b strings.Builder
	for _, p := range sc.Properties {
		fmt.Fprintf(&b, "- %s（%s，id：%s）", p.Name, typeLabel(p.Type), p.ID)
		switch p.Type {
		case TSelect, TMultiSelect, TStatus:
			var names []string
			for _, o := range p.Options {
				names = append(names, o.Name)
			}
			fmt.Fprintf(&b, " 选项：%s", strings.Join(names, "、"))
		case TFormula:
			fmt.Fprintf(&b, " 公式：%s", p.Expression)
		case TRelation:
			fmt.Fprintf(&b, " 关联数据库：%s", p.DatabaseID)
		case TRollup:
			fmt.Fprintf(&b, " 汇总：%s → %s（%s）", p.Relation, p.Target, p.Function)
		case TNumber:
			if p.Format != "" {
				fmt.Fprintf(&b, " 格式：%s", p.Format)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func typeLabel(t string) string {
	return map[string]string{TTitle: "标题", TText: "文本", TNumber: "数字", TSelect: "单选", TMultiSelect: "多选", TStatus: "状态", TDate: "日期",
		TPerson: "人员", TFiles: "文件", TCheckbox: "复选框", TURL: "网址", TEmail: "邮箱", TPhone: "电话", TFormula: "公式", TRelation: "关联",
		TRollup: "汇总", TCreatedTime: "创建时间", TCreatedBy: "创建者", TLastEditedTime: "编辑时间", TLastEditedBy: "编辑者", TUniqueID: "编号"}[t]
}
