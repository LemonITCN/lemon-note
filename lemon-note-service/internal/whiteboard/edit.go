package whiteboard

import (
	"fmt"
	"math"
	"strings"
)

// AddItem 是新增元素的简化写法。
type AddItem struct {
	Key    string   `json:"key"`  // 本次编辑里引用它用的临时名（连线的 from / to 可以写它）
	Type   string   `json:"type"` // rectangle | ellipse | diamond | text | arrow | line
	Text   string   `json:"text"`
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	W      float64  `json:"w"`
	H      float64  `json:"h"`
	Stroke string   `json:"stroke"`
	Fill   string   `json:"fill"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Dashed bool     `json:"dashed"`
	Bend   float64  `json:"-"` // 连线向一侧弯出的距离（自动布局用，避免穿过中间的图形）
}

// UpdateItem 修改已有元素；只改给出的字段。
type UpdateItem struct {
	ID     string   `json:"id"`
	Text   *string  `json:"text"`
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	W      *float64 `json:"w"`
	H      *float64 `json:"h"`
	Stroke *string  `json:"stroke"`
	Fill   *string  `json:"fill"`
}

// EditInput 一次白板编辑。
type EditInput struct {
	Clear   bool         `json:"clear"`
	Delete  []string     `json:"delete"`
	Update  []UpdateItem `json:"update"`
	Add     []AddItem    `json:"add"`
	Mermaid string       `json:"mermaid"`
}

// EditResult 编辑结果。
type EditResult struct {
	Added   map[string]string // key → 新元素 id
	Created int
	Updated int
	Deleted int
}

var shapeTypes = map[string]bool{"rectangle": true, "ellipse": true, "diamond": true}

// Apply 在场景上执行编辑。
func Apply(s *Scene, in EditInput) (*EditResult, error) {
	res := &EditResult{Added: map[string]string{}}
	if in.Clear {
		res.Deleted = len(index(s).byID)
		s.Elements = nil
	}
	if err := applyDelete(s, in.Delete, res); err != nil {
		return nil, err
	}
	if err := applyUpdate(s, in.Update, res); err != nil {
		return nil, err
	}
	items := in.Add
	if strings.TrimSpace(in.Mermaid) != "" {
		g, err := ParseMermaid(in.Mermaid)
		if err != nil {
			return nil, err
		}
		items = append(items, g.Layout(s)...)
	}
	if err := applyAdd(s, items, res); err != nil {
		return nil, err
	}
	return res, nil
}

func applyDelete(s *Scene, ids []string, res *EditResult) error {
	if len(ids) == 0 {
		return nil
	}
	ix := index(s)
	kill := map[string]bool{}
	for _, id := range ids {
		if ix.byID[id] == nil {
			return fmt.Errorf("元素 %s 不存在", id)
		}
		kill[id] = true
		if t := ix.textOf[id]; t != nil { // 图形里的文字一起删
			kill[str(t, "id")] = true
		}
	}
	kept := s.Elements[:0]
	for _, e := range s.Elements {
		if kill[str(e, "id")] {
			res.Deleted++
			continue
		}
		// 连到被删图形的线解除绑定；文字容器没了的文字也删掉
		for _, k := range []string{"startBinding", "endBinding"} {
			if kill[binding(e, k)] {
				e[k] = nil
				touch(e)
			}
		}
		if list, ok := e["boundElements"].([]any); ok {
			out := list[:0]
			for _, x := range list {
				if m, ok := x.(map[string]any); ok && kill[str(m, "id")] {
					continue
				}
				out = append(out, x)
			}
			e["boundElements"] = out
		}
		kept = append(kept, e)
	}
	s.Elements = kept
	return nil
}

func applyUpdate(s *Scene, ups []UpdateItem, res *EditResult) error {
	ix := index(s)
	for _, u := range ups {
		e := ix.byID[u.ID]
		if e == nil {
			return fmt.Errorf("元素 %s 不存在", u.ID)
		}
		if u.X != nil {
			e["x"] = *u.X
		}
		if u.Y != nil {
			e["y"] = *u.Y
		}
		if u.W != nil && *u.W > 0 {
			e["width"] = *u.W
		}
		if u.H != nil && *u.H > 0 {
			e["height"] = *u.H
		}
		if u.Stroke != nil {
			e["strokeColor"] = *u.Stroke
		}
		if u.Fill != nil {
			e["backgroundColor"] = *u.Fill
		}
		if u.Text != nil {
			setText(s, ix, e, *u.Text)
		}
		touch(e)
		// 容器移动或改大小后，里面的文字跟着居中
		if t := ix.textOf[u.ID]; t != nil {
			center(e, t)
			touch(t)
		}
		res.Updated++
	}
	return nil
}

func setText(s *Scene, ix *sceneIndex, e map[string]any, text string) {
	if str(e, "type") == "text" {
		w, h := textSize(text, num(e, "fontSize"))
		e["text"], e["originalText"], e["width"], e["height"] = text, text, w, h
		return
	}
	if t := ix.textOf[str(e, "id")]; t != nil {
		if text == "" {
			t["isDeleted"] = true
			touch(t)
			return
		}
		w, h := textSize(text, fontSize)
		t["text"], t["originalText"], t["width"], t["height"] = text, text, w, h
		touch(t)
		return
	}
	if text == "" {
		return
	}
	t := textElement(text, 0, 0, str(e, "id"))
	center(e, t)
	addBound(e, "text", str(t, "id"))
	s.Elements = append(s.Elements, t)
	ix.textOf[str(e, "id")] = t
	ix.byID[str(t, "id")] = t
}

func center(container, t map[string]any) {
	if str(container, "type") == "arrow" || str(container, "type") == "line" {
		// 连线标签放在线段中点
		pts, _ := container["points"].([]any)
		mx, my := num(container, "x"), num(container, "y")
		if len(pts) == 3 { // 折线：标签放在折点
			if p, ok := pts[1].([]any); ok && len(p) == 2 {
				px, _ := p[0].(float64)
				py, _ := p[1].(float64)
				t["x"], t["y"] = mx+px-num(t, "width")/2, my+py-num(t, "height")/2
				return
			}
		}
		if len(pts) >= 2 {
			a, _ := pts[0].([]any)
			b, _ := pts[len(pts)-1].([]any)
			if len(a) == 2 && len(b) == 2 {
				ax, _ := a[0].(float64)
				ay, _ := a[1].(float64)
				bx, _ := b[0].(float64)
				by, _ := b[1].(float64)
				mx += (ax + bx) / 2
				my += (ay + by) / 2
			}
		}
		t["x"], t["y"] = mx-num(t, "width")/2, my-num(t, "height")/2
		return
	}
	t["x"] = num(container, "x") + (num(container, "width")-num(t, "width"))/2
	t["y"] = num(container, "y") + (num(container, "height")-num(t, "height"))/2
}

func applyAdd(s *Scene, items []AddItem, res *EditResult) error {
	if len(items) == 0 {
		return nil
	}
	ix := index(s)
	// 没给坐标的图形排在现有内容右侧，每行 4 个
	_, minY, maxX, _, ok := bounds(s)
	cx, cy := 0.0, 0.0
	if ok {
		cx, cy = maxX+120, minY
	}
	startX, col, rowH := cx, 0, 0.0
	place := func(w, h float64) (float64, float64) {
		x, y := cx, cy
		col++
		rowH = math.Max(rowH, h)
		if col >= 4 {
			col, cx, cy, rowH = 0, startX, cy+rowH+80, 0
		} else {
			cx += w + 80
		}
		return x, y
	}
	resolve := func(ref string) map[string]any {
		if ref == "" {
			return nil
		}
		if id, ok := res.Added[ref]; ok {
			return ix.byID[id]
		}
		return ix.byID[ref]
	}
	var arrows []AddItem
	for _, it := range items {
		typ := strings.ToLower(strings.TrimSpace(it.Type))
		if typ == "" {
			typ = "rectangle"
		}
		switch {
		case typ == "arrow" || typ == "line":
			arrows = append(arrows, it)
			continue
		case typ == "text":
			x, y := 0.0, 0.0
			if it.X != nil && it.Y != nil {
				x, y = *it.X, *it.Y
			} else {
				w, h := textSize(it.Text, fontSize)
				x, y = place(w, h)
			}
			t := textElement(it.Text, x, y, "")
			if it.Stroke != "" {
				t["strokeColor"] = it.Stroke
			}
			s.Elements = append(s.Elements, t)
			ix.byID[str(t, "id")] = t
			if it.Key != "" {
				res.Added[it.Key] = str(t, "id")
			}
			res.Created++
		case shapeTypes[typ]:
			w, h := it.W, it.H
			tw, th := textSize(it.Text, fontSize)
			if w <= 0 {
				w = math.Max(160, tw+48)
			}
			if h <= 0 {
				h = math.Max(72, th+36)
			}
			if typ == "diamond" {
				w, h = math.Max(w, tw*1.6+40), math.Max(h, th*1.6+40)
			}
			x, y := 0.0, 0.0
			if it.X != nil && it.Y != nil {
				x, y = *it.X, *it.Y
			} else {
				x, y = place(w, h)
			}
			e := base(typ, x, y, w, h)
			if it.Stroke != "" {
				e["strokeColor"] = it.Stroke
			}
			if it.Fill != "" {
				e["backgroundColor"] = it.Fill
			}
			if it.Dashed {
				e["strokeStyle"] = "dashed"
			}
			s.Elements = append(s.Elements, e)
			ix.byID[str(e, "id")] = e
			if it.Text != "" {
				setText(s, ix, e, it.Text)
			}
			if it.Key != "" {
				res.Added[it.Key] = str(e, "id")
			}
			res.Created++
		default:
			return fmt.Errorf("不支持的元素类型 %q（可用：rectangle、ellipse、diamond、text、arrow、line）", it.Type)
		}
	}
	for _, it := range arrows {
		from, to := resolve(it.From), resolve(it.To)
		if (it.From != "" && from == nil) || (it.To != "" && to == nil) {
			return fmt.Errorf("连线的 from / to 找不到：%s → %s", it.From, it.To)
		}
		var sx, sy, ex, ey float64
		switch {
		case from != nil && to != nil && it.Bend != 0:
			sx, sy, ex, ey = 0, 0, 0, 0
		case from != nil && to != nil:
			sx, sy, ex, ey = edgePoints(from, to)
		case it.X != nil && it.Y != nil:
			sx, sy = *it.X, *it.Y
			ex, ey = sx+math.Max(it.W, 120), sy+it.H
		default:
			return fmt.Errorf("连线要么给 from 和 to，要么给 x、y（与 w、h 作为终点偏移）")
		}
		typ := strings.ToLower(it.Type)
		head := typ == "arrow"
		if typ == "line" && (from != nil || to != nil) {
			typ = "arrow" // 只有 arrow 能绑定到图形上；不带箭头的连接线用无箭头的 arrow 表示
		}
		var pts []any
		if from != nil && to != nil && it.Bend != 0 {
			// 从两个中心的中点向法线方向偏移出一个折点，起止点朝折点出发
			acx, acy := num(from, "x")+num(from, "width")/2, num(from, "y")+num(from, "height")/2
			bcx, bcy := num(to, "x")+num(to, "width")/2, num(to, "y")+num(to, "height")/2
			dx, dy := bcx-acx, bcy-acy
			l := math.Hypot(dx, dy)
			if l == 0 {
				l = 1
			}
			mx, my := (acx+bcx)/2-dy/l*it.Bend, (acy+bcy)/2+dx/l*it.Bend
			sx, sy = boxEdge(from, mx, my)
			ex, ey = boxEdge(to, mx, my)
			pts = []any{[]any{0.0, 0.0}, []any{mx - sx, my - sy}, []any{ex - sx, ey - sy}}
		} else {
			pts = []any{[]any{0.0, 0.0}, []any{ex - sx, ey - sy}}
		}
		e := base(typ, sx, sy, math.Abs(ex-sx), math.Abs(ey-sy))
		e["points"] = pts
		e["lastCommittedPoint"] = nil
		e["startArrowhead"] = nil
		e["endArrowhead"] = nil
		if typ == "arrow" {
			e["elbowed"] = false
		}
		if head {
			e["endArrowhead"] = "arrow"
		}
		if it.Stroke != "" {
			e["strokeColor"] = it.Stroke
		}
		if it.Dashed {
			e["strokeStyle"] = "dashed"
		}
		e["startBinding"], e["endBinding"] = nil, nil
		if from != nil {
			e["startBinding"] = map[string]any{"elementId": str(from, "id"), "focus": 0, "gap": 6}
			addBound(from, "arrow", str(e, "id"))
		}
		if to != nil {
			e["endBinding"] = map[string]any{"elementId": str(to, "id"), "focus": 0, "gap": 6}
			addBound(to, "arrow", str(e, "id"))
		}
		s.Elements = append(s.Elements, e)
		ix.byID[str(e, "id")] = e
		if it.Text != "" {
			t := textElement(it.Text, 0, 0, str(e, "id"))
			center(e, t)
			addBound(e, "text", str(t, "id"))
			s.Elements = append(s.Elements, t)
		}
		if it.Key != "" {
			res.Added[it.Key] = str(e, "id")
		}
		res.Created++
	}
	return nil
}

// edgePoints 计算两个图形之间连线的起止点：从中心连线与边框的交点出发，各留 6 像素间隙。
func edgePoints(a, b map[string]any) (sx, sy, ex, ey float64) {
	acx, acy := num(a, "x")+num(a, "width")/2, num(a, "y")+num(a, "height")/2
	bcx, bcy := num(b, "x")+num(b, "width")/2, num(b, "y")+num(b, "height")/2
	sx, sy = boxEdge(a, bcx, bcy)
	ex, ey = boxEdge(b, acx, acy)
	return
}

func boxEdge(e map[string]any, tx, ty float64) (float64, float64) {
	cx, cy := num(e, "x")+num(e, "width")/2, num(e, "y")+num(e, "height")/2
	dx, dy := tx-cx, ty-cy
	if dx == 0 && dy == 0 {
		return cx, cy
	}
	hw, hh := num(e, "width")/2+6, num(e, "height")/2+6
	scale := math.Min(math.Abs(hw/nonZero(dx)), math.Abs(hh/nonZero(dy)))
	return cx + dx*scale, cy + dy*scale
}

func nonZero(v float64) float64 {
	if v == 0 {
		return 1e-9
	}
	return v
}
