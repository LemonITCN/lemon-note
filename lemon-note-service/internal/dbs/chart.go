package dbs

import (
	"context"
	"sort"
)

// ChartSpec 是图表块的数据（source.kind=database 时由服务端计算）。
type ChartSpec struct {
	Type   string `json:"type"` // bar | line | pie | area | scatter
	Title  string `json:"title"`
	Source struct {
		Kind       string `json:"kind"`
		DatabaseID string `json:"databaseId"`
		ViewID     string `json:"viewId"`
		X          string `json:"x"`
		Y          []struct {
			Property string `json:"property"`
			Agg      string `json:"agg"` // count | sum | average | min | max
		} `json:"y"`
		GroupBy string `json:"groupBy"`
	} `json:"source"`
}

// ChartOption 按数据库视图聚合出 ECharts 配置。
func (s *Service) ChartOption(ctx context.Context, spec ChartSpec) (map[string]any, error) {
	t, res, err := s.Query(ctx, spec.Source.DatabaseID, QueryInput{ViewID: spec.Source.ViewID})
	if err != nil {
		return nil, err
	}
	rows := res.Rows
	for _, g := range res.Groups {
		rows = append(rows, g.Rows...)
	}
	xp := t.Schema.Find(spec.Source.X)
	if xp == nil {
		xp = t.Schema.Title()
	}
	type key = string
	buckets := map[key][]*Row{}
	var order []key
	for _, r := range rows {
		v := t.Value(r, xp)
		ks := []string{ToText(v)}
		if l, ok := v.([]Value); ok {
			ks = nil
			for _, e := range l {
				ks = append(ks, ToText(e))
			}
		}
		if d, ok := v.(DateVal); ok {
			ks = []string{d.Start.Format("2006-01-02")}
		}
		for _, k := range ks {
			if k == "" {
				k = "（空）"
			}
			if _, ok := buckets[k]; !ok {
				order = append(order, k)
			}
			buckets[k] = append(buckets[k], r)
		}
	}
	if xp.Type == TSelect || xp.Type == TStatus || xp.Type == TMultiSelect {
		pos := map[string]int{}
		for i, o := range xp.Options {
			pos[o.Name] = i
		}
		sort.SliceStable(order, func(i, j int) bool { return pos[order[i]] < pos[order[j]] })
	} else if xp.Type == TDate || xp.Type == TNumber {
		sort.Strings(order)
	}
	ys := spec.Source.Y
	if len(ys) == 0 {
		ys = append(ys, struct {
			Property string `json:"property"`
			Agg      string `json:"agg"`
		}{Agg: "count"})
	}
	var series []map[string]any
	typ := spec.Type
	if typ == "" {
		typ = "bar"
	}
	for _, y := range ys {
		yp := t.Schema.Find(y.Property)
		var data []any
		for _, k := range order {
			rs := buckets[k]
			if y.Agg == "count" || yp == nil {
				data = append(data, len(rs))
				continue
			}
			var vals []Value
			for _, r := range rs {
				vals = append(vals, t.Value(r, yp))
			}
			data = append(data, Display(aggNum(vals, y.Agg)))
		}
		name := "数量"
		if yp != nil {
			name = yp.Name
		}
		sr := map[string]any{"name": name, "type": typ, "data": data}
		if typ == "area" {
			sr["type"] = "line"
			sr["areaStyle"] = map[string]any{}
		}
		if typ == "pie" {
			var pd []any
			for i, k := range order {
				pd = append(pd, map[string]any{"name": k, "value": data[i]})
			}
			sr["data"] = pd
			sr["radius"] = []string{"40%", "70%"}
		}
		series = append(series, sr)
	}
	opt := map[string]any{"tooltip": map[string]any{"trigger": "axis"}, "series": series, "grid": map[string]any{"left": 40, "right": 20, "top": 40, "bottom": 30, "containLabel": true}}
	if spec.Title != "" {
		opt["title"] = map[string]any{"text": spec.Title, "left": "center", "textStyle": map[string]any{"fontSize": 14}}
	}
	if typ == "pie" {
		opt["tooltip"] = map[string]any{"trigger": "item"}
		opt["legend"] = map[string]any{"bottom": 0}
	} else {
		opt["xAxis"] = map[string]any{"type": "category", "data": order}
		opt["yAxis"] = map[string]any{"type": "value"}
		if len(series) > 1 {
			opt["legend"] = map[string]any{"top": 0}
		}
	}
	return opt, nil
}
