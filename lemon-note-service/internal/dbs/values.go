package dbs

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Row 是数据库的一行（对应一个页面）。
type Row struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Icon      string         `json:"icon,omitempty"`
	Position  string         `json:"position"`
	Props     map[string]any `json:"props"`            // 存储的属性值（按属性 id）
	Computed  map[string]any `json:"computed,omitempty"` // 计算出的值（公式、汇总等），给界面显示
	CreatedAt int64          `json:"created_at"`
	CreatedBy string         `json:"created_by"`
	UpdatedAt int64          `json:"updated_at"`
	UpdatedBy string         `json:"updated_by"`
	Template  bool           `json:"is_template,omitempty"`
}

// DateInput 是日期属性的存储格式。
type DateInput struct {
	Start string `json:"start"`
	End   string `json:"end,omitempty"`
}

func asStrings(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		var out []string
		for _, s := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == '，' || r == '、' }) {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			switch y := e.(type) {
			case string:
				if strings.TrimSpace(y) != "" {
					out = append(out, strings.TrimSpace(y))
				}
			case map[string]any:
				if id, ok := y["id"].(string); ok {
					out = append(out, id)
				} else if n, ok := y["name"].(string); ok {
					out = append(out, n)
				}
			default:
				out = append(out, fmt.Sprint(y))
			}
		}
		return out
	case []string:
		return x
	}
	return []string{fmt.Sprint(v)}
}

// Resolver 帮助把人类可读的输入（选项名、用户名、页面标题）转换成 id。
type Resolver struct {
	User     func(s string) (string, bool) // 用户名 / 显示名 / id → id
	Relation func(dbID, s string) (string, bool)
	// CreateOption 为真时自动新建不存在的选项
	CreateOption bool
}

// NormalizeValue 把输入规整成某个属性的存储格式。changed 为真表示结构被修改（新建了选项）。
func NormalizeValue(p *Property, in any, rs Resolver) (out any, changed bool, err error) {
	if in == nil {
		return nil, false, nil
	}
	switch p.Type {
	case TText, TURL, TEmail, TPhone:
		s := strings.TrimSpace(ToText(jsonToValue(in)))
		if s == "" {
			return nil, false, nil
		}
		if p.Type == TURL && !strings.Contains(s, "://") && strings.Contains(s, ".") {
			s = "https://" + s
		}
		return s, false, nil
	case TNumber:
		switch x := in.(type) {
		case float64:
			return x, false, nil
		case json.Number:
			f, err := x.Float64()
			return f, false, err
		case string:
			s := strings.TrimSpace(strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(x, ",", ""), "¥", ""), "%"))
			if s == "" {
				return nil, false, nil
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, false, fmt.Errorf("「%s」不是数字", x)
			}
			return f, false, nil
		case int, int64:
			return toNum(fmt.Sprint(x)), false, nil
		}
		return nil, false, fmt.Errorf("属性「%s」需要数字", p.Name)
	case TCheckbox:
		switch x := in.(type) {
		case bool:
			return x, false, nil
		case string:
			s := strings.ToLower(strings.TrimSpace(x))
			return s == "true" || s == "1" || s == "yes" || s == "是" || s == "y" || s == "√" || s == "已完成", false, nil
		case float64:
			return x != 0, false, nil
		}
		return false, false, nil
	case TSelect, TStatus:
		names := asStrings(in)
		if len(names) == 0 {
			return nil, false, nil
		}
		if o := p.OptionByName(names[0]); o != nil {
			return o.ID, false, nil
		}
		if !rs.CreateOption || p.Type == TStatus {
			return nil, false, fmt.Errorf("属性「%s」没有选项「%s」（可选：%s）", p.Name, names[0], optionNames(p))
		}
		o := Option{ID: NewOptionID(), Name: names[0], Color: optColorCycle[len(p.Options)%len(optColorCycle)]}
		p.Options = append(p.Options, o)
		return o.ID, true, nil
	case TMultiSelect:
		names := asStrings(in)
		var out []any
		changed := false
		for _, n := range names {
			o := p.OptionByName(n)
			if o == nil {
				if !rs.CreateOption {
					return nil, false, fmt.Errorf("属性「%s」没有选项「%s」（可选：%s）", p.Name, n, optionNames(p))
				}
				p.Options = append(p.Options, Option{ID: NewOptionID(), Name: n, Color: optColorCycle[len(p.Options)%len(optColorCycle)]})
				o = &p.Options[len(p.Options)-1]
				changed = true
			}
			if !containsAny(out, o.ID) {
				out = append(out, o.ID)
			}
		}
		if len(out) == 0 {
			return nil, changed, nil
		}
		return out, changed, nil
	case TDate:
		var start, end string
		switch x := in.(type) {
		case string:
			parts := strings.Split(x, "→")
			if len(parts) == 1 {
				parts = strings.Split(x, " ~ ")
			}
			start = strings.TrimSpace(parts[0])
			if len(parts) > 1 {
				end = strings.TrimSpace(parts[1])
			}
		case map[string]any:
			start, _ = x["start"].(string)
			end, _ = x["end"].(string)
		}
		if start == "" {
			return nil, false, nil
		}
		if _, ok := ParseDate(start); !ok {
			return nil, false, fmt.Errorf("「%s」不是有效日期（用 2026-11-25 或 2026-11-25 14:30）", start)
		}
		if end != "" {
			if _, ok := ParseDate(end); !ok {
				return nil, false, fmt.Errorf("「%s」不是有效日期", end)
			}
		}
		m := map[string]any{"start": start}
		if end != "" {
			m["end"] = end
		}
		return m, false, nil
	case TPerson:
		var out []any
		for _, n := range asStrings(in) {
			id, ok := n, true
			if rs.User != nil {
				id, ok = rs.User(n)
			}
			if !ok {
				return nil, false, fmt.Errorf("找不到用户「%s」", n)
			}
			if !containsAny(out, id) {
				out = append(out, id)
			}
		}
		if len(out) == 0 {
			return nil, false, nil
		}
		return out, false, nil
	case TRelation:
		var out []any
		for _, n := range asStrings(in) {
			id, ok := n, true
			if rs.Relation != nil {
				id, ok = rs.Relation(p.DatabaseID, n)
			}
			if !ok {
				return nil, false, fmt.Errorf("关联的数据库里找不到「%s」", n)
			}
			if !containsAny(out, id) {
				out = append(out, id)
			}
		}
		if len(out) == 0 {
			return nil, false, nil
		}
		return out, false, nil
	case TFiles:
		list, ok := in.([]any)
		if !ok {
			return nil, false, fmt.Errorf("属性「%s」需要文件列表", p.Name)
		}
		var out []any
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				if s, ok := e.(string); ok && strings.HasPrefix(s, "http") {
					out = append(out, map[string]any{"url": s, "name": s})
				}
				continue
			}
			f := map[string]any{}
			for _, k := range []string{"file_id", "url", "name"} {
				if v, ok := m[k].(string); ok {
					f[k] = v
				}
			}
			out = append(out, f)
		}
		return out, false, nil
	}
	return nil, false, fmt.Errorf("属性「%s」是计算出来的，不能直接修改", p.Name)
}

func optionNames(p *Property) string {
	var n []string
	for _, o := range p.Options {
		n = append(n, o.Name)
	}
	if len(n) == 0 {
		return "无"
	}
	return strings.Join(n, "、")
}

func containsAny(l []any, v any) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func jsonToValue(v any) Value {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case []any:
		out := make([]Value, len(x))
		for i, e := range x {
			out[i] = jsonToValue(e)
		}
		return out
	}
	return v
}

// ---------- 计算 ----------

// Table 是一个已加载的数据库（结构 + 行），附带计算缓存。
type Table struct {
	ID     string
	Title  string
	Schema *Schema
	Rows   []*Row
	byID   map[string]*Row
	// Related 加载关联数据库（汇总、关联标题需要）
	Related func(dbID string) *Table
	// UserName 用户显示名
	UserName func(id string) string

	cache   map[string]Value // rowID|propID → 值
	formula map[string]*Formula
	stack   map[string]bool
}

// NewTable 构造表。
func NewTable(id, title string, schema *Schema, rows []*Row) *Table {
	t := &Table{ID: id, Title: title, Schema: schema, Rows: rows, byID: map[string]*Row{}, cache: map[string]Value{}, formula: map[string]*Formula{}, stack: map[string]bool{}}
	for _, r := range rows {
		t.byID[r.ID] = r
	}
	return t
}

// Row 按 id 取行。
func (t *Table) Row(id string) *Row { return t.byID[id] }

// Value 计算某行某属性的值（公式、汇总、关联都在这里求出）。
func (t *Table) Value(r *Row, p *Property) Value {
	key := r.ID + "|" + p.ID
	if v, ok := t.cache[key]; ok {
		return v
	}
	if t.stack[key] {
		return nil // 循环引用
	}
	t.stack[key] = true
	defer delete(t.stack, key)
	v := t.compute(r, p)
	t.cache[key] = v
	return v
}

func (t *Table) compute(r *Row, p *Property) Value {
	raw := r.Props[p.ID]
	switch p.Type {
	case TTitle:
		return r.Title
	case TText, TURL, TEmail, TPhone:
		if s, ok := raw.(string); ok && s != "" {
			return s
		}
		return nil
	case TNumber:
		if raw == nil {
			return nil
		}
		return toNum(jsonToValue(raw))
	case TCheckbox:
		b, _ := raw.(bool)
		return b
	case TSelect, TStatus:
		id, _ := raw.(string)
		if o := p.Option(id); o != nil {
			return o.Name
		}
		return nil
	case TMultiSelect:
		var out []Value
		for _, id := range asStrings(raw) {
			if o := p.Option(id); o != nil {
				out = append(out, o.Name)
			}
		}
		return out
	case TDate:
		m, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		s, _ := m["start"].(string)
		d, ok := ParseDate(s)
		if !ok {
			return nil
		}
		if e, _ := m["end"].(string); e != "" {
			if ed, ok := ParseDate(e); ok {
				d.End = &ed.Start
			}
		}
		return d
	case TPerson:
		var out []Value
		for _, id := range asStrings(raw) {
			name := id
			if t.UserName != nil {
				name = t.UserName(id)
			}
			out = append(out, name)
		}
		return out
	case TFiles:
		var out []Value
		if l, ok := raw.([]any); ok {
			for _, e := range l {
				if m, ok := e.(map[string]any); ok {
					out = append(out, m["name"])
				}
			}
		}
		return out
	case TRelation:
		var out []Value
		rel := t.related(p.DatabaseID)
		for _, id := range asStrings(raw) {
			if rel != nil {
				if rr := rel.Row(id); rr != nil {
					out = append(out, rr.Title)
				}
			}
		}
		return out
	case TRollup:
		return t.rollup(r, p)
	case TFormula:
		f := t.formula[p.ID]
		if f == nil {
			var err error
			f, err = ParseFormula(p.Expression)
			if err != nil {
				return nil
			}
			t.formula[p.ID] = f
		}
		v, err := f.Eval(&EvalEnv{ID: r.ID, Prop: func(name string) (Value, error) {
			q := t.Schema.Find(name)
			if q == nil {
				return nil, fmt.Errorf("没有属性「%s」", name)
			}
			return t.Value(r, q), nil
		}})
		if err != nil {
			return nil
		}
		return v
	case TCreatedTime:
		return DateVal{Start: time.UnixMilli(r.CreatedAt), HasTime: true}
	case TLastEditedTime:
		return DateVal{Start: time.UnixMilli(r.UpdatedAt), HasTime: true}
	case TCreatedBy, TLastEditedBy:
		id := r.CreatedBy
		if p.Type == TLastEditedBy {
			id = r.UpdatedBy
		}
		if t.UserName != nil {
			return t.UserName(id)
		}
		return id
	case TUniqueID:
		if raw == nil {
			return nil
		}
		n := int64(toNum(jsonToValue(raw)))
		if p.Prefix != "" {
			return p.Prefix + "-" + strconv.FormatInt(n, 10)
		}
		return float64(n)
	}
	return nil
}

func (t *Table) related(dbID string) *Table {
	if dbID == t.ID {
		return t
	}
	if t.Related == nil {
		return nil
	}
	return t.Related(dbID)
}

func (t *Table) rollup(r *Row, p *Property) Value {
	relProp := t.Schema.Prop(p.Relation)
	if relProp == nil || relProp.Type != TRelation {
		return nil
	}
	rel := t.related(relProp.DatabaseID)
	if rel == nil {
		return nil
	}
	target := rel.Schema.Prop(p.Target)
	if target == nil {
		return nil
	}
	var vals []Value
	for _, id := range asStrings(r.Props[relProp.ID]) {
		if rr := rel.Row(id); rr != nil {
			v := rel.Value(rr, target)
			if l, ok := v.([]Value); ok {
				vals = append(vals, l...)
			} else {
				vals = append(vals, v)
			}
		}
	}
	nonEmpty := 0
	for _, v := range vals {
		if truthy(v) || v == float64(0) {
			nonEmpty++
		}
	}
	switch p.Function {
	case "count":
		return float64(len(vals))
	case "count_values", "not_empty":
		return float64(nonEmpty)
	case "empty":
		return float64(len(vals) - nonEmpty)
	case "unique":
		seen := map[string]bool{}
		for _, v := range vals {
			seen[ToText(v)] = true
		}
		return float64(len(seen))
	case "percent_empty":
		if len(vals) == 0 {
			return 0.0
		}
		return math.Round(float64(len(vals)-nonEmpty)/float64(len(vals))*1000) / 10
	case "percent_not_empty":
		if len(vals) == 0 {
			return 0.0
		}
		return math.Round(float64(nonEmpty)/float64(len(vals))*1000) / 10
	case "sum", "average", "median", "min", "max", "range":
		return aggNum(vals, p.Function)
	case "earliest", "latest":
		var best *DateVal
		for _, v := range vals {
			if d, ok := v.(DateVal); ok {
				if best == nil || (p.Function == "earliest" && d.Start.Before(best.Start)) || (p.Function == "latest" && d.Start.After(best.Start)) {
					dd := d
					best = &dd
				}
			}
		}
		if best == nil {
			return nil
		}
		return *best
	case "checked":
		n := 0.0
		for _, v := range vals {
			if b, ok := v.(bool); ok && b {
				n++
			}
		}
		return n
	}
	return vals // show_original
}

// Display 把值转成给界面与模型看的形式（日期转文本，列表保持数组）。
func Display(v Value) any {
	switch x := v.(type) {
	case DateVal:
		return ToText(x)
	case []Value:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Display(e)
		}
		return out
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
	}
	return v
}

// FillComputed 为界面填好计算属性的值。
func (t *Table) FillComputed(rows []*Row) {
	for _, r := range rows {
		r.Computed = map[string]any{}
		for i := range t.Schema.Properties {
			p := &t.Schema.Properties[i]
			switch p.Type {
			case TFormula, TRollup, TRelation, TUniqueID:
				r.Computed[p.ID] = Display(t.Value(r, p))
			}
		}
	}
}

// SortRowsByPosition 按手动顺序排序。
func SortRowsByPosition(rows []*Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Position != rows[j].Position {
			return rows[i].Position < rows[j].Position
		}
		return rows[i].ID < rows[j].ID
	})
}
