package dbs

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Filter 是筛选条件（and / or 可嵌套）。
type Filter struct {
	And   []Filter `json:"and,omitempty"`
	Or    []Filter `json:"or,omitempty"`
	Prop  string   `json:"prop,omitempty"`
	Op    string   `json:"op,omitempty"`
	Value any      `json:"value,omitempty"`
}

// Sort 是排序条件。
type Sort struct {
	Prop string `json:"prop"`
	Dir  string `json:"dir"` // asc | desc
}

// GroupBy 是分组设置。
type GroupBy struct {
	Prop      string   `json:"prop"`
	By        string   `json:"by,omitempty"` // 日期：day | week | month | year
	HideEmpty bool     `json:"hide_empty,omitempty"`
	Order     []string `json:"order,omitempty"`
	Hidden    []string `json:"hidden,omitempty"`
}

// ViewConfig 是视图配置。
type ViewConfig struct {
	Filter    *Filter           `json:"filter,omitempty"`
	Sorts     []Sort            `json:"sorts,omitempty"`
	GroupBy   *GroupBy          `json:"group_by,omitempty"`
	Visible   []string          `json:"visible,omitempty"`
	Widths    map[string]int    `json:"widths,omitempty"`
	CardCover string            `json:"card_cover,omitempty"`
	CardSize  string            `json:"card_size,omitempty"`
	DateProp  string            `json:"date_prop,omitempty"`
	EndProp   string            `json:"end_prop,omitempty"`
	Wrap      bool              `json:"wrap,omitempty"`
	Chart     map[string]any    `json:"chart,omitempty"`
	Calculate map[string]string `json:"calculate,omitempty"` // 表格底部统计
	ShowTitle bool              `json:"show_title,omitempty"`
}

// Matches 判断行是否满足筛选。
func (t *Table) Matches(r *Row, f *Filter) (bool, error) {
	if f == nil {
		return true, nil
	}
	if len(f.And) > 0 {
		for i := range f.And {
			ok, err := t.Matches(r, &f.And[i])
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}
	if len(f.Or) > 0 {
		for i := range f.Or {
			ok, err := t.Matches(r, &f.Or[i])
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	if f.Prop == "" {
		return true, nil
	}
	p := t.Schema.Find(f.Prop)
	if p == nil {
		return false, fmt.Errorf("筛选里的属性「%s」不存在", f.Prop)
	}
	v := t.Value(r, p)
	return evalOp(p, v, f.Op, f.Value)
}

func isEmpty(v Value) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []Value:
		return len(x) == 0
	}
	return false
}

func optionName(p *Property, want any) string {
	s := ToText(jsonToValue(want))
	if o := p.OptionByName(s); o != nil {
		return o.Name
	}
	return s
}

func evalOp(p *Property, v Value, op string, want any) (bool, error) {
	op = strings.ToLower(op)
	switch op {
	case "is_empty", "empty":
		return isEmpty(v), nil
	case "is_not_empty", "not_empty":
		return !isEmpty(v), nil
	}
	wantV := jsonToValue(want)
	switch x := v.(type) {
	case []Value:
		target := ToText(wantV)
		if p.Type == TMultiSelect {
			target = optionName(p, want)
		}
		has := false
		for _, e := range x {
			if strings.EqualFold(ToText(e), target) {
				has = true
			}
		}
		switch op {
		case "contains", "is", "has":
			return has, nil
		case "not_contains", "is_not":
			return !has, nil
		}
	case DateVal:
		return dateOp(x, op, wantV)
	case bool:
		w := truthy(wantV)
		if s, ok := wantV.(string); ok {
			w = s == "true" || s == "是" || s == "1"
		}
		switch op {
		case "is", "==", "=", "equals":
			return x == w, nil
		case "is_not", "!=":
			return x != w, nil
		}
	case float64:
		w := toNum(wantV)
		switch op {
		case "=", "==", "is", "equals":
			return x == w, nil
		case "!=", "is_not":
			return x != w, nil
		case ">", "gt":
			return x > w, nil
		case "<", "lt":
			return x < w, nil
		case ">=", "gte":
			return x >= w, nil
		case "<=", "lte":
			return x <= w, nil
		}
	}
	if v == nil {
		switch op {
		case "is_not", "not_contains", "!=":
			return true, nil
		}
		return false, nil
	}
	s := strings.ToLower(ToText(v))
	w := strings.ToLower(ToText(wantV))
	if p.Type == TSelect || p.Type == TStatus {
		w = strings.ToLower(optionName(p, want))
	}
	switch op {
	case "is", "==", "=", "equals":
		return s == w, nil
	case "is_not", "!=":
		return s != w, nil
	case "contains":
		return strings.Contains(s, w), nil
	case "not_contains":
		return !strings.Contains(s, w), nil
	case "starts_with":
		return strings.HasPrefix(s, w), nil
	case "ends_with":
		return strings.HasSuffix(s, w), nil
	case "in":
		for _, x := range asStrings(want) {
			if strings.EqualFold(optionName(p, x), ToText(v)) {
				return true, nil
			}
		}
		return false, nil
	case ">", "<", ">=", "<=":
		c := Compare(v, wantV)
		return map[string]bool{">": c > 0, "<": c < 0, ">=": c >= 0, "<=": c <= 0}[op], nil
	}
	return false, fmt.Errorf("不支持的筛选条件 %s", op)
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// relRange 把相对日期（today、this_week、past_7_days……）转成区间。
func relRange(s string) (time.Time, time.Time, bool) {
	now := time.Now()
	today := dayStart(now)
	switch s {
	case "today":
		return today, today.AddDate(0, 0, 1), true
	case "tomorrow":
		return today.AddDate(0, 0, 1), today.AddDate(0, 0, 2), true
	case "yesterday":
		return today.AddDate(0, 0, -1), today, true
	case "this_week":
		wd := int(today.Weekday())
		if wd == 0 {
			wd = 7
		}
		start := today.AddDate(0, 0, 1-wd)
		return start, start.AddDate(0, 0, 7), true
	case "this_month":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
		return start, start.AddDate(0, 1, 0), true
	case "past_week", "past_7_days":
		return today.AddDate(0, 0, -7), today.AddDate(0, 0, 1), true
	case "past_month", "past_30_days":
		return today.AddDate(0, 0, -30), today.AddDate(0, 0, 1), true
	case "next_week", "next_7_days":
		return today, today.AddDate(0, 0, 8), true
	case "next_month", "next_30_days":
		return today, today.AddDate(0, 0, 31), true
	}
	return time.Time{}, time.Time{}, false
}

func dateOp(d DateVal, op string, want Value) (bool, error) {
	ws := ToText(want)
	if a, b, ok := relRange(ws); ok {
		in := !d.Start.Before(a) && d.Start.Before(b)
		switch op {
		case "is", "within", "==", "=":
			return in, nil
		case "is_not", "!=":
			return !in, nil
		case "before", "<":
			return d.Start.Before(a), nil
		case "after", ">":
			return !d.Start.Before(b), nil
		case "on_or_before", "<=":
			return d.Start.Before(b), nil
		case "on_or_after", ">=":
			return !d.Start.Before(a), nil
		}
	}
	w, ok := toDate(want)
	if !ok {
		return false, fmt.Errorf("「%s」不是有效日期", ws)
	}
	ds, wsd := dayStart(d.Start), dayStart(w.Start)
	switch op {
	case "is", "==", "=", "equals":
		return ds.Equal(wsd), nil
	case "is_not", "!=":
		return !ds.Equal(wsd), nil
	case "before", "<":
		return ds.Before(wsd), nil
	case "after", ">":
		return ds.After(wsd), nil
	case "on_or_before", "<=":
		return !ds.After(wsd), nil
	case "on_or_after", ">=":
		return !ds.Before(wsd), nil
	}
	return false, fmt.Errorf("不支持的日期筛选条件 %s", op)
}

// Apply 筛选 + 排序（无排序条件时按手动顺序）。
func (t *Table) Apply(rows []*Row, f *Filter, sorts []Sort, search string) ([]*Row, error) {
	var out []*Row
	q := strings.ToLower(strings.TrimSpace(search))
	for _, r := range rows {
		ok, err := t.Matches(r, f)
		if err != nil {
			return nil, err
		}
		if ok && q != "" {
			ok = strings.Contains(strings.ToLower(r.Title), q)
			if !ok {
				for i := range t.Schema.Properties {
					p := &t.Schema.Properties[i]
					if strings.Contains(strings.ToLower(ToText(t.Value(r, p))), q) {
						ok = true
						break
					}
				}
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	if len(sorts) == 0 {
		SortRowsByPosition(out)
		return out, nil
	}
	var props []*Property
	for _, s := range sorts {
		p := t.Schema.Find(s.Prop)
		if p == nil {
			return nil, fmt.Errorf("排序里的属性「%s」不存在", s.Prop)
		}
		props = append(props, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		for k, s := range sorts {
			a, b := t.sortKey(out[i], props[k]), t.sortKey(out[j], props[k])
			// 空值总是排在后面
			if isEmpty(a) != isEmpty(b) {
				return !isEmpty(a)
			}
			c := Compare(a, b)
			if c == 0 {
				continue
			}
			if strings.EqualFold(s.Dir, "desc") {
				return c > 0
			}
			return c < 0
		}
		return out[i].Position < out[j].Position
	})
	return out, nil
}

// sortKey 选项按选项顺序排，其余按值。
func (t *Table) sortKey(r *Row, p *Property) Value {
	if p.Type == TSelect || p.Type == TStatus {
		id, _ := r.Props[p.ID].(string)
		for i, o := range p.Options {
			if o.ID == id {
				return float64(i)
			}
		}
		return nil
	}
	return t.Value(r, p)
}

// Group 是一个分组。
type Group struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Color string `json:"color,omitempty"`
	Rows  []*Row `json:"rows"`
	Count int    `json:"count"`
}

// GroupRows 按分组设置分组（选项按选项顺序，其他按值排序）。
func (t *Table) GroupRows(rows []*Row, g *GroupBy) ([]*Group, error) {
	p := t.Schema.Find(g.Prop)
	if p == nil {
		return nil, fmt.Errorf("分组属性「%s」不存在", g.Prop)
	}
	groups := map[string]*Group{}
	var order []string
	add := func(key, label, color string) *Group {
		if gr, ok := groups[key]; ok {
			return gr
		}
		gr := &Group{Key: key, Label: label, Color: color, Rows: []*Row{}}
		groups[key] = gr
		order = append(order, key)
		return gr
	}
	if p.Type == TSelect || p.Type == TStatus || p.Type == TMultiSelect {
		for _, o := range p.Options {
			add(o.ID, o.Name, o.Color)
		}
	}
	if p.Type == TCheckbox {
		add("true", "true", "")
		add("false", "false", "")
	}
	add("", "", "")
	for _, r := range rows {
		switch p.Type {
		case TSelect, TStatus:
			id, _ := r.Props[p.ID].(string)
			if p.Option(id) == nil {
				id = ""
			}
			gr := groups[id]
			gr.Rows = append(gr.Rows, r)
		case TMultiSelect, TPerson, TRelation:
			keys := asStrings(r.Props[p.ID])
			if len(keys) == 0 {
				groups[""].Rows = append(groups[""].Rows, r)
			}
			for _, k := range keys {
				label := k
				if p.Type == TMultiSelect {
					o := p.Option(k)
					if o == nil {
						continue
					}
					label = o.Name
				} else if p.Type == TPerson && t.UserName != nil {
					label = t.UserName(k)
				} else if p.Type == TRelation {
					if rel := t.related(p.DatabaseID); rel != nil {
						if rr := rel.Row(k); rr != nil {
							label = rr.Title
						}
					}
				}
				add(k, label, "").Rows = append(groups[k].Rows, r)
			}
		case TCheckbox:
			b, _ := r.Props[p.ID].(bool)
			groups[fmt.Sprint(b)].Rows = append(groups[fmt.Sprint(b)].Rows, r)
		default:
			v := t.Value(r, p)
			key, label := "", ""
			if d, ok := v.(DateVal); ok {
				key, label = dateBucket(d.Start, g.By)
			} else if !isEmpty(v) {
				label = ToText(v)
				key = label
			}
			add(key, label, "").Rows = append(groups[key].Rows, r)
		}
	}
	// 非选项类分组按键排序（空组最后）
	if !(p.Type == TSelect || p.Type == TStatus || p.Type == TMultiSelect || p.Type == TCheckbox) {
		sort.SliceStable(order, func(i, j int) bool {
			if order[i] == "" || order[j] == "" {
				return order[j] == ""
			}
			return order[i] < order[j]
		})
	} else {
		// 空组放最后
		var head []string
		for _, k := range order {
			if k != "" {
				head = append(head, k)
			}
		}
		order = append(head, "")
	}
	if len(g.Order) > 0 {
		pos := map[string]int{}
		for i, k := range g.Order {
			pos[k] = i + 1
		}
		sort.SliceStable(order, func(i, j int) bool {
			a, b := pos[order[i]], pos[order[j]]
			if a == 0 {
				a = 1 << 30
			}
			if b == 0 {
				b = 1 << 30
			}
			return a < b
		})
	}
	hidden := map[string]bool{}
	for _, h := range g.Hidden {
		hidden[h] = true
	}
	var out []*Group
	for _, k := range order {
		gr := groups[k]
		gr.Count = len(gr.Rows)
		if hidden[k] || (gr.Count == 0 && (g.HideEmpty || k == "")) {
			continue
		}
		out = append(out, gr)
	}
	return out, nil
}

func dateBucket(t time.Time, by string) (string, string) {
	switch by {
	case "year":
		return t.Format("2006"), t.Format("2006")
	case "month":
		return t.Format("2006-01"), t.Format("2006-01")
	case "week":
		y, w := t.ISOWeek()
		k := fmt.Sprintf("%d-W%02d", y, w)
		return k, k
	}
	return t.Format("2006-01-02"), t.Format("2006-01-02")
}

// Calc 计算列统计（表格底部）。
func (t *Table) Calc(rows []*Row, p *Property, fn string) any {
	var vals []Value
	for _, r := range rows {
		vals = append(vals, t.Value(r, p))
	}
	empty := 0
	for _, v := range vals {
		if isEmpty(v) {
			empty++
		}
	}
	switch fn {
	case "count":
		return len(vals)
	case "count_values", "not_empty":
		return len(vals) - empty
	case "empty":
		return empty
	case "percent_empty":
		if len(vals) == 0 {
			return 0
		}
		return float64(empty*1000/len(vals)) / 10
	case "checked":
		n := 0
		for _, v := range vals {
			if b, _ := v.(bool); b {
				n++
			}
		}
		return n
	case "sum", "average", "median", "min", "max", "range":
		return Display(aggNum(vals, fn))
	case "unique":
		seen := map[string]bool{}
		for _, v := range vals {
			if !isEmpty(v) {
				seen[ToText(v)] = true
			}
		}
		return len(seen)
	}
	return nil
}
