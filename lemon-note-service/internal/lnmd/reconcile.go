package lnmd

// Reconcile 把智能体改过的整页 Markdown 与原块对齐（docs/spec.md §6「局部修改」）：
//
//  1. 解析 newMD，逐块渲染，与原块渲染文本做最长公共子序列对齐；对齐上的块原样返回原节点；
//  2. 文本完全相同但位置变了的块（移动）同样原样返回原节点；
//  3. 白板、同步块原件在 Markdown 里写明了 id，与原块 id 相同即认领该原块（白板带回原 data），
//     认不出的 id 清空交给调用方分配；
//  4. 两段对齐之间的缺口里，变化的新块按位置继承原块 id（思维导图同时按文本路径继承节点 id、
//     样式、备注与布局主题）；多出来的新块 id 留空，少了的原块不再出现。
func Reconcile(original []Node, newMD string, o RenderOptions) ([]Node, error) {
	parsed, err := Parse(newMD)
	if err != nil {
		return nil, err
	}
	r := &renderer{o: o}
	oldT := make([]string, len(original))
	for i, n := range original {
		oldT[i] = r.block(n)
	}
	newT := make([]string, len(parsed))
	for j, n := range parsed {
		newT[j] = r.block(n)
	}

	pairs := alignBlocks(oldT, newT)
	result := make([]Node, len(parsed))
	verbatim := make([]bool, len(parsed))
	usedOld := make([]bool, len(original))
	for _, p := range pairs {
		result[p[1]] = original[p[0]]
		verbatim[p[1]] = true
		usedOld[p[0]] = true
	}

	// 移动：文本相同的未对齐块直接复用原节点
	byText := map[string][]int{}
	for i := range original {
		if !usedOld[i] {
			byText[oldT[i]] = append(byText[oldT[i]], i)
		}
	}
	for j := range parsed {
		if verbatim[j] {
			continue
		}
		if q := byText[newT[j]]; len(q) > 0 {
			i := q[0]
			byText[newT[j]] = q[1:]
			result[j] = original[i]
			verbatim[j] = true
			usedOld[i] = true
		}
	}

	oldByID := map[string]int{}
	for i, n := range original {
		if id := nodeID(n); id != "" {
			if _, dup := oldByID[id]; !dup {
				oldByID[id] = i
			}
		}
	}
	oldWhiteboards := map[string]Node{}
	collectWhiteboards(original, oldWhiteboards)

	// 写明 id 的块（白板、同步块原件）认领原块
	explicit := make([]bool, len(parsed))
	for j := range parsed {
		if verbatim[j] {
			continue
		}
		n := parsed[j]
		if !carriesID(n) {
			continue
		}
		id := nodeID(n)
		i, ok := oldByID[id]
		if !ok || usedOld[i] || original[i].Type != n.Type {
			delete(parsed[j].Attrs, "id")
			continue
		}
		usedOld[i] = true
		explicit[j] = true
		if n.Type == tWhiteboard {
			restoreWhiteboard(&parsed[j], original[i])
		}
	}

	// 缺口内按位置继承 id
	prevI, prevJ := -1, -1
	bounds := append(append([][2]int{}, pairs...), [2]int{len(original), len(parsed)})
	for _, p := range bounds {
		var olds, news []int
		for i := prevI + 1; i < p[0]; i++ {
			if !usedOld[i] {
				olds = append(olds, i)
			}
		}
		for j := prevJ + 1; j < p[1]; j++ {
			if !verbatim[j] && !explicit[j] {
				news = append(news, j)
			}
		}
		for t := 0; t < len(olds) && t < len(news); t++ {
			inherit(&parsed[news[t]], original[olds[t]])
			usedOld[olds[t]] = true
		}
		prevI, prevJ = p[0], p[1]
	}

	for j := range parsed {
		if !verbatim[j] {
			result[j] = parsed[j]
		}
	}
	dedupeIDs(result, verbatim, oldWhiteboards)
	return result, nil
}

// carriesID 报告块的 id 是否写在 Markdown 里。
func carriesID(n Node) bool {
	switch n.Type {
	case tWhiteboard:
		return nodeID(n) != ""
	case tSyncedBlock:
		return attrStr(n, "sourceId") == "" && nodeID(n) != ""
	}
	return false
}

func inherit(n *Node, old Node) {
	if id := nodeID(old); id != "" {
		setAttr(n, "id", id)
	}
	switch {
	case n.Type == tMindmap && old.Type == tMindmap:
		setAttr(n, "data", inheritMindmap(old.Attrs["data"], n.Attrs["data"]))
	case n.Type == tWhiteboard && old.Type == tWhiteboard:
		restoreWhiteboard(n, old)
	}
}

func restoreWhiteboard(n *Node, old Node) {
	if v, ok := attrRaw(old, "data"); ok {
		if _, has := attrRaw(*n, "data"); !has {
			setAttr(n, "data", deepCopyValue(v))
		}
	}
}

func collectWhiteboards(ns []Node, into map[string]Node) {
	for _, n := range ns {
		if n.Type == tWhiteboard {
			if id := nodeID(n); id != "" {
				if _, ok := into[id]; !ok {
					into[id] = n
				}
			}
		}
		collectWhiteboards(n.Content, into)
	}
}

func collectIDs(ns []Node, into map[string]bool) {
	for _, n := range ns {
		if id := nodeID(n); id != "" {
			into[id] = true
		}
		collectIDs(n.Content, into)
	}
}

// dedupeIDs 检查新解析块内部写明的白板、同步块 id：重复的清空，认得的白板带回原 data。
func dedupeIDs(result []Node, verbatim []bool, oldWB map[string]Node) {
	seen := map[string]bool{}
	for j, n := range result {
		if verbatim[j] {
			collectIDs([]Node{n}, seen)
			continue
		}
		if id := nodeID(n); id != "" {
			if seen[id] {
				delete(result[j].Attrs, "id")
			} else {
				seen[id] = true
			}
		}
	}
	for j := range result {
		if verbatim[j] {
			continue
		}
		fixNested(result[j].Content, seen, oldWB)
	}
}

func fixNested(ns []Node, seen map[string]bool, oldWB map[string]Node) {
	for i := range ns {
		n := &ns[i]
		if carriesID(*n) {
			id := nodeID(*n)
			if seen[id] {
				delete(n.Attrs, "id")
			} else {
				seen[id] = true
				if n.Type == tWhiteboard {
					if old, ok := oldWB[id]; ok {
						restoreWhiteboard(n, old)
					}
				}
			}
		}
		fixNested(n.Content, seen, oldWB)
	}
}

// ---- 对齐 ----

// alignBlocks 求两串文本的公共子序列对齐（原下标, 新下标），按原下标递增。
// 先去掉公共前后缀；中间部分规模不大时用动态规划求精确最长公共子序列，
// 过大时先用双方都只出现一次的块作锚点（patience 对齐）切分再递归。
func alignBlocks(a, b []string) [][2]int {
	ids := map[string]int{}
	intern := func(ss []string) []int {
		out := make([]int, len(ss))
		for i, s := range ss {
			id, ok := ids[s]
			if !ok {
				id = len(ids)
				ids[s] = id
			}
			out[i] = id
		}
		return out
	}
	x, y := intern(a), intern(b)
	var out [][2]int
	alignRange(x, y, 0, 0, &out)
	return out
}

const dpLimit = 2_000_000

func alignRange(x, y []int, xo, yo int, out *[][2]int) {
	p := 0
	for p < len(x) && p < len(y) && x[p] == y[p] {
		*out = append(*out, [2]int{xo + p, yo + p})
		p++
	}
	x, y, xo, yo = x[p:], y[p:], xo+p, yo+p
	s := 0
	for s < len(x) && s < len(y) && x[len(x)-1-s] == y[len(y)-1-s] {
		s++
	}
	mx, my := x[:len(x)-s], y[:len(y)-s]
	if len(mx) > 0 && len(my) > 0 {
		if len(mx)*len(my) <= dpLimit {
			lcsDP(mx, my, xo, yo, out)
		} else {
			patience(mx, my, xo, yo, out)
		}
	}
	for k := 0; k < s; k++ {
		*out = append(*out, [2]int{xo + len(mx) + k, yo + len(my) + k})
	}
}

func lcsDP(x, y []int, xo, yo int, out *[][2]int) {
	n, m := len(x), len(y)
	w := m + 1
	dp := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			} else if a, b := dp[(i+1)*w+j], dp[i*w+j+1]; a >= b {
				dp[i*w+j] = a
			} else {
				dp[i*w+j] = b
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			*out = append(*out, [2]int{xo + i, yo + j})
			i++
			j++
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			i++
		default:
			j++
		}
	}
}

func patience(x, y []int, xo, yo int, out *[][2]int) {
	cx, cy := map[int]int{}, map[int]int{}
	py := map[int]int{}
	for _, v := range x {
		cx[v]++
	}
	for j, v := range y {
		cy[v]++
		py[v] = j
	}
	type anchor struct{ i, j int }
	var anchors []anchor
	for i, v := range x {
		if cx[v] == 1 && cy[v] == 1 {
			anchors = append(anchors, anchor{i, py[v]})
		}
	}
	if len(anchors) == 0 {
		// 没有唯一锚点：退化为按位置配对相同块的贪心对齐
		j := 0
		for i := 0; i < len(x) && j < len(y); i++ {
			for k := j; k < len(y) && k < j+64; k++ {
				if x[i] == y[k] {
					*out = append(*out, [2]int{xo + i, yo + k})
					j = k + 1
					break
				}
			}
		}
		return
	}
	// 在锚点的新下标上求最长递增子序列
	tails := []int{}
	prev := make([]int, len(anchors))
	for k, a := range anchors {
		lo, hi := 0, len(tails)
		for lo < hi {
			mid := (lo + hi) / 2
			if anchors[tails[mid]].j < a.j {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			prev[k] = tails[lo-1]
		} else {
			prev[k] = -1
		}
		if lo == len(tails) {
			tails = append(tails, k)
		} else {
			tails[lo] = k
		}
	}
	chain := make([]anchor, len(tails))
	for k, t := len(tails)-1, tails[len(tails)-1]; k >= 0; k-- {
		chain[k] = anchors[t]
		t = prev[t]
	}
	li, lj := 0, 0
	for _, a := range chain {
		alignRange(x[li:a.i], y[lj:a.j], xo+li, yo+lj, out)
		*out = append(*out, [2]int{xo + a.i, yo + a.j})
		li, lj = a.i+1, a.j+1
	}
	alignRange(x[li:], y[lj:], xo+li, yo+lj, out)
}
