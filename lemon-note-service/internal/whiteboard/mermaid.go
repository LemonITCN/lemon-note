package whiteboard

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Graph 是从 Mermaid 流程图解析出的节点与连线。
type Graph struct {
	Dir   string // LR | RL | TB | BT
	Nodes []*gNode
	Edges []gEdge
	byID  map[string]*gNode
}

type gNode struct {
	ID, Label, Shape string
	rank, order      int
}

type gEdge struct {
	From, To, Label string
	Dashed, Arrow   bool
}

var (
	reHeader = regexp.MustCompile(`^(?:flowchart|graph)\s*(LR|RL|TB|TD|BT)?\s*$`)
	// 节点写法：A、A[文字]、A(文字)、A((文字))、A{文字}、A([文字])、A[(文字)]、A>文字]
	reNode = regexp.MustCompile(`^\s*([A-Za-z0-9_\-\p{Han}]+)\s*(\(\(.*?\)\)|\(\[.*?\]\)|\[\(.*?\)\]|\[\[.*?\]\]|\[.*?\]|\(.*?\)|\{.*?\}|>.*?\])?`)
	// 连线写法：-->、---、-.->、==>、-- 文字 -->、-->|文字|
	reLink = regexp.MustCompile(`^\s*(--\s*[^->|]+?\s*-->|--\s*[^->|]+?\s*---|-\.\s*[^->|]+?\s*\.->|==\s*[^=>|]+?\s*==>|-->|---|-\.->|-\.-|==>|===|--o|--x)\s*(\|[^|]*\|)?`)
)

// ParseMermaid 解析 Mermaid 流程图（flowchart / graph）。不支持的语句（子图、样式、点击等）跳过。
func ParseMermaid(src string) (*Graph, error) {
	g := &Graph{Dir: "TB", byID: map[string]*gNode{}}
	lines := strings.Split(strings.ReplaceAll(src, ";", "\n"), "\n")
	started := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if !started {
			if m := reHeader.FindStringSubmatch(line); m != nil {
				started = true
				if m[1] != "" {
					g.Dir = strings.Replace(m[1], "TD", "TB", 1)
				}
				continue
			}
			return nil, fmt.Errorf("只支持 Mermaid 流程图（以 flowchart 或 graph 开头）")
		}
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "subgraph") || low == "end" || strings.HasPrefix(low, "classdef") || strings.HasPrefix(low, "class ") ||
			strings.HasPrefix(low, "style") || strings.HasPrefix(low, "linkstyle") || strings.HasPrefix(low, "click") || strings.HasPrefix(low, "direction") {
			continue
		}
		g.parseStatement(line)
	}
	if !started {
		return nil, fmt.Errorf("只支持 Mermaid 流程图（以 flowchart 或 graph 开头）")
	}
	if len(g.Nodes) == 0 {
		return nil, fmt.Errorf("流程图里没有节点")
	}
	if len(g.Nodes) > 200 {
		return nil, fmt.Errorf("节点太多（最多 200 个）")
	}
	return g, nil
}

func (g *Graph) node(id, shapeTok string) *gNode {
	n := g.byID[id]
	if n == nil {
		n = &gNode{ID: id, Label: id, Shape: "rectangle"}
		g.byID[id] = n
		g.Nodes = append(g.Nodes, n)
	}
	if shapeTok != "" {
		label, shape := shapeTok, "rectangle"
		switch {
		case strings.HasPrefix(shapeTok, "(("):
			label, shape = shapeTok[2:len(shapeTok)-2], "ellipse"
		case strings.HasPrefix(shapeTok, "(["), strings.HasPrefix(shapeTok, "[("), strings.HasPrefix(shapeTok, "[["):
			label = shapeTok[2 : len(shapeTok)-2]
		case strings.HasPrefix(shapeTok, "{"):
			label, shape = shapeTok[1:len(shapeTok)-1], "diamond"
		case strings.HasPrefix(shapeTok, "("):
			label, shape = shapeTok[1:len(shapeTok)-1], "ellipse"
		default:
			label = shapeTok[1 : len(shapeTok)-1]
		}
		label = strings.Trim(strings.TrimSpace(label), `"`)
		label = strings.ReplaceAll(strings.ReplaceAll(label, "<br>", "\n"), "<br/>", "\n")
		n.Label, n.Shape = label, shape
	}
	return n
}

func (g *Graph) parseStatement(line string) {
	rest := line
	var prev *gNode
	var pending *gEdge
	for rest != "" {
		m := reNode.FindStringSubmatchIndex(rest)
		if m == nil || m[0] != 0 {
			return
		}
		id := rest[m[2]:m[3]]
		tok := ""
		if m[4] >= 0 {
			tok = rest[m[4]:m[5]]
		}
		n := g.node(id, tok)
		if prev != nil && pending != nil {
			pending.From, pending.To = prev.ID, n.ID
			g.Edges = append(g.Edges, *pending)
		}
		prev, pending = n, nil
		rest = strings.TrimSpace(rest[m[1]:])
		if strings.HasPrefix(rest, "&") { // A & B --> C：简化为只连第一个
			return
		}
		lm := reLink.FindStringSubmatchIndex(rest)
		if lm == nil || lm[0] != 0 {
			return
		}
		op := rest[lm[2]:lm[3]]
		e := gEdge{Arrow: strings.HasSuffix(op, ">"), Dashed: strings.Contains(op, ".")}
		if lm[4] >= 0 {
			e.Label = strings.Trim(rest[lm[4]:lm[5]], "|")
		} else if inner := strings.TrimSpace(strings.Trim(op, "-.=>ox")); inner != "" {
			e.Label = inner
		}
		e.Label = strings.Trim(strings.TrimSpace(e.Label), `"`)
		pending = &e
		rest = strings.TrimSpace(rest[lm[1]:])
	}
}

// Layout 分层布局：按最长路径分层（忽略回边），同层按出现顺序排列。返回可直接 Apply 的新增元素。
func (g *Graph) Layout(s *Scene) []AddItem {
	out := map[string][]string{}
	in := map[string]int{}
	for _, e := range g.Edges {
		out[e.From] = append(out[e.From], e.To)
	}
	// 用 DFS 找回边，分层时忽略
	state := map[string]int{}
	back := map[[2]string]bool{}
	var dfs func(string)
	dfs = func(u string) {
		state[u] = 1
		for _, v := range out[u] {
			if state[v] == 1 {
				back[[2]string{u, v}] = true
			} else if state[v] == 0 {
				dfs(v)
			}
		}
		state[u] = 2
	}
	for _, n := range g.Nodes {
		if state[n.ID] == 0 {
			dfs(n.ID)
		}
	}
	for _, e := range g.Edges {
		if !back[[2]string{e.From, e.To}] && e.From != e.To {
			in[e.To]++
		}
	}
	// 拓扑序求最长路径层号
	queue := []string{}
	for _, n := range g.Nodes {
		if in[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range out[u] {
			if back[[2]string{u, v}] || u == v {
				continue
			}
			if g.byID[u].rank+1 > g.byID[v].rank {
				g.byID[v].rank = g.byID[u].rank + 1
			}
			in[v]--
			if in[v] == 0 {
				queue = append(queue, v)
			}
		}
	}
	ranks := map[int][]*gNode{}
	maxRank := 0
	for _, n := range g.Nodes {
		ranks[n.rank] = append(ranks[n.rank], n)
		if n.rank > maxRank {
			maxRank = n.rank
		}
	}
	// 尺寸
	size := map[string][2]float64{}
	maxW, maxH := 0.0, 0.0
	for _, n := range g.Nodes {
		tw, th := textSize(n.Label, fontSize)
		w, h := maxF(160, tw+48), maxF(72, th+36)
		if n.Shape == "diamond" {
			w, h = maxF(w, tw*1.6+40), maxF(h, th*1.6+40)
		}
		size[n.ID] = [2]float64{w, h}
		maxW, maxH = maxF(maxW, w), maxF(maxH, h)
	}
	ox, oy := 0.0, 0.0
	if _, minY, maxX, _, ok := bounds(s); ok {
		ox, oy = maxX+160, minY
	}
	gapMain, gapCross := 110.0, 60.0
	horizontal := g.Dir == "LR" || g.Dir == "RL"
	items := []AddItem{}
	keys := make([]int, 0, len(ranks))
	for r := range ranks {
		keys = append(keys, r)
	}
	sort.Ints(keys)
	for _, r := range keys {
		pos := r
		if g.Dir == "RL" || g.Dir == "BT" {
			pos = maxRank - r
		}
		for i, n := range ranks[r] {
			sz := size[n.ID]
			var x, y float64
			if horizontal {
				x = ox + float64(pos)*(maxW+gapMain) + (maxW-sz[0])/2
				y = oy + float64(i)*(maxH+gapCross) + (maxH-sz[1])/2
			} else {
				x = ox + float64(i)*(maxW+gapCross) + (maxW-sz[0])/2
				y = oy + float64(pos)*(maxH+gapMain) + (maxH-sz[1])/2
			}
			xx, yy := x, y
			items = append(items, AddItem{Key: "mm:" + n.ID, Type: n.Shape, Text: n.Label, X: &xx, Y: &yy, W: sz[0], H: sz[1]})
		}
	}
	// 同层内的序号，用来判断跨层连线会不会穿过中间的图形
	for _, list := range ranks {
		for i, n := range list {
			n.order = i
		}
	}
	bendSeen := map[string]int{}
	for _, e := range g.Edges {
		typ := "arrow"
		if !e.Arrow {
			typ = "line"
		}
		it := AddItem{Type: typ, From: "mm:" + e.From, To: "mm:" + e.To, Text: e.Label, Dashed: e.Dashed}
		a, b := g.byID[e.From], g.byID[e.To]
		span := b.rank - a.rank
		if span < 0 {
			span = -span
		}
		if a.order == b.order && (span > 1 || back[[2]string{e.From, e.To}]) {
			// 同一排跨层（或回头）的连线向外弯，多条依次加大
			key := fmt.Sprint(a.order)
			bendSeen[key]++
			d := (maxH/2 + 50) * float64(bendSeen[key])
			if !horizontal {
				d = (maxW/2 + 50) * float64(bendSeen[key])
			}
			if back[[2]string{e.From, e.To}] {
				d = -d
			}
			it.Bend = d
		}
		items = append(items, it)
	}
	return items
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
