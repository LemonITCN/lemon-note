package lnmd

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Parse 把增强 Markdown 解析成顶层块。新块的 attrs.id 留空由调用方分配；白板与同步块原件的 id
// 取自标签上的 id 属性。
func Parse(md string) ([]Node, error) {
	nz := normalize(md)
	doc := mdParser().Parse(text.NewReader(nz.src))
	c := &conv{src: nz.src, nz: nz}
	return c.convChildren(doc), nil
}

type conv struct {
	src []byte
	nz  *normResult
}

// piece 是块转换的中间结果：普通节点，或一个自定义标签事件。
type piece struct {
	nodes []Node
	tag   *tagTok
}

func nodesPiece(ns ...Node) []piece { return []piece{{nodes: ns}} }

func (c *conv) convChildren(parent ast.Node) []Node {
	var ps []piece
	for ch := parent.FirstChild(); ch != nil; ch = ch.NextSibling() {
		ps = append(ps, c.convBlock(ch)...)
	}
	return c.build(ps)
}

func (c *conv) convBlock(n ast.Node) []piece {
	switch v := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return c.convParagraph(n).pieces
	case *ast.Heading:
		return nodesPiece(c.convHeading(v))
	case *ast.ThematicBreak:
		return nodesPiece(Node{Type: tHorizontalRule})
	case *ast.FencedCodeBlock:
		return nodesPiece(c.convFence(v))
	case *ast.CodeBlock:
		return nodesPiece(codeNode("", c.linesText(v)))
	case *ast.Blockquote:
		return nodesPiece(Node{Type: tBlockquote, Content: ensurePlus(c.convChildren(v))})
	case *ast.List:
		return nodesPiece(c.convList(v)...)
	case *extast.Table:
		return nodesPiece(c.convTable(v))
	}
	if n.HasChildren() {
		return nodesPiece(c.convChildren(n)...)
	}
	if s := strings.TrimSpace(c.linesText(n)); s != "" {
		return nodesPiece(paragraphOf(s))
	}
	return nil
}

func paragraphOf(s string) Node {
	return Node{Type: tParagraph, Content: canonInline([]Node{{Type: tText, Text: s}}, true)}
}

func ensurePlus(ns []Node) []Node {
	if len(ns) == 0 {
		return []Node{emptyParagraph()}
	}
	return ns
}

// linesText 拼接块的原始行。
func (c *conv) linesText(n ast.Node) string {
	var b bytes.Buffer
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(c.src))
	}
	return b.String()
}

func codeNode(lang, content string) Node {
	n := Node{Type: tCodeBlock}
	if lang != "" {
		n.Attrs = map[string]any{"language": lang}
	}
	if content != "" {
		n.Content = []Node{{Type: tText, Text: content}}
	}
	return n
}

func (c *conv) convFence(v *ast.FencedCodeBlock) Node {
	info := ""
	if v.Info != nil {
		info = strings.TrimSpace(resolveText(v.Info.Segment.Value(c.src)))
	}
	content := strings.TrimSuffix(c.linesText(v), "\n")
	fields := strings.Fields(info)
	lang := ""
	if len(fields) > 0 {
		lang = strings.ToLower(fields[0])
	}
	switch lang {
	case "mermaid":
		return Node{Type: tMermaid, Attrs: map[string]any{"code": content}}
	case "dot", "graphviz":
		engine := "dot"
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.ToLower(f), "engine="); ok && v != "" {
				engine = strings.Trim(v, `"'`)
			}
		}
		return Node{Type: tGraphviz, Attrs: map[string]any{"code": content, "engine": engine}}
	case "mindmap":
		return Node{Type: tMindmap, Attrs: map[string]any{"data": parseMindmap(content)}}
	case "chart":
		dec := json.NewDecoder(strings.NewReader(content))
		dec.UseNumber()
		var data any
		if err := dec.Decode(&data); err == nil && !dec.More() {
			var extra any
			if dec.Decode(&extra) != nil {
				return Node{Type: tChart, Attrs: map[string]any{"data": data}}
			}
		}
		return codeNode(info, content)
	case "math":
		return Node{Type: tMathBlock, Attrs: map[string]any{"latex": content}}
	}
	return codeNode(info, content)
}

// ---- 段落与行内 ----

type paraResult struct {
	pieces []piece
	check  *bool
}

func (c *conv) inlineItems(n ast.Node, ctx inlineCtx) []Node {
	var raw []Node
	c.walkInline(n, nil, ctx, &raw)
	return pairHTML(raw)
}

// extractTrailers 取出尾注占位符；allowed 为假时还原成原文。
func (c *conv) extractTrailers(items []Node, allowed bool) ([]Node, []attrKV, []string) {
	var attrs []attrKV
	var raws []string
	out := make([]Node, 0, len(items))
	for _, it := range items {
		if it.Type != pTrlTok {
			out = append(out, it)
			continue
		}
		tk := c.nz.trailers[attrInt(it, "i", 0)]
		if !allowed {
			out = append(out, Node{Type: tText, Text: tk.raw, Marks: it.Marks})
			continue
		}
		attrs = append(attrs, tk.attrs...)
		raws = append(raws, tk.raw)
		if n := len(out); n > 0 && out[n-1].Type == tText {
			out[n-1].Text = strings.TrimRight(out[n-1].Text, " \t")
		} else if n > 0 && out[n-1].Type == tHardBreak {
			// 尾注单独占一行：去掉它前面的换行
			out = out[:n-1]
		}
	}
	return out, attrs, raws
}

// restoreBlockTokens 把出现在意外位置的块标签占位符还原成原文。
func (c *conv) restoreBlockTokens(items []Node) []Node {
	for i, it := range items {
		if it.Type == pBlkTok {
			items[i] = Node{Type: tText, Text: c.nz.tags[attrInt(it, "i", 0)].raw, Marks: it.Marks}
		}
	}
	return items
}

func isBlankText(n Node) bool {
	return n.Type == tText && strings.Trim(n.Text, " \t") == ""
}

// soleBlockToken 判断行内内容是否只有一个块标签占位符。
func soleBlockToken(items []Node) (int, bool) {
	idx := -1
	for _, it := range items {
		switch {
		case it.Type == pBlkTok:
			if idx >= 0 {
				return 0, false
			}
			idx = attrInt(it, "i", 0)
		case isBlankText(it):
		default:
			return 0, false
		}
	}
	return idx, idx >= 0
}

func (c *conv) convParagraph(n ast.Node) paraResult {
	items := c.inlineItems(n, inlineCtx{})
	var res paraResult
	if len(items) > 0 && items[0].Type == pCheck {
		b := attrBool(items[0], "checked")
		res.check = &b
		items = items[1:]
	}
	for i, it := range items {
		if it.Type == pCheck {
			items[i] = Node{Type: tText, Text: "[ ] "}
			if attrBool(it, "checked") {
				items[i].Text = "[x] "
			}
		}
	}
	items, tattrs, traws := c.extractTrailers(items, true)
	if idx, ok := soleBlockToken(items); ok {
		res.pieces = []piece{{tag: c.nz.tags[idx]}}
		return res
	}
	items = c.restoreBlockTokens(items)

	var nodes []Node
	var cur []Node
	flush := func() {
		content := canonInline(cur, true)
		if len(content) > 0 {
			nodes = append(nodes, Node{Type: tParagraph, Content: content})
		}
		cur = nil
	}
	for _, it := range items {
		if it.Type == pImage {
			flush()
			nodes = append(nodes, Node{Type: tImage, Attrs: it.Attrs})
			continue
		}
		cur = append(cur, it)
	}
	flush()
	if len(nodes) == 0 {
		if len(traws) == 0 {
			if res.check != nil {
				res.pieces = nodesPiece(emptyParagraph())
			}
			return res
		}
		nodes = []Node{paragraphOf(strings.Join(traws, " "))}
		tattrs = nil
	}
	if len(tattrs) > 0 {
		last := &nodes[len(nodes)-1]
		applyTrailer(last, tattrs)
	}
	res.pieces = nodesPiece(nodes...)
	return res
}

// applyTrailer 把块属性尾注应用到节点。
func applyTrailer(n *Node, attrs []attrKV) {
	for _, a := range attrs {
		v := trailerValue(a)
		switch n.Type {
		case tImage:
			switch a.key {
			case "width", "height":
				setAttr(n, a.key, parseNumberValue(a.val))
			case "align":
				setAttr(n, "align", a.val)
			}
		default:
			switch a.key {
			case "color":
				setAttr(n, "color", scalarString(v))
			case "align":
				setAttr(n, "textAlign", scalarString(v))
			}
		}
	}
}

// inlineToText 把图片等不能出现在此处的伪节点换成文字。
func inlineToText(items []Node) []Node {
	for i, it := range items {
		switch it.Type {
		case pImage:
			items[i] = Node{Type: tText, Text: attrStr(it, "alt"), Marks: it.Marks}
		case pCheck:
			items[i] = Node{Type: tText, Text: "[ ] "}
		}
	}
	return items
}

func (c *conv) convHeading(v *ast.Heading) Node {
	items := c.inlineItems(v, inlineCtx{})
	items, tattrs, _ := c.extractTrailers(items, true)
	items = inlineToText(c.restoreBlockTokens(items))
	lv := v.Level
	if lv > 3 {
		lv = 3
	}
	if lv < 1 {
		lv = 1
	}
	n := Node{Type: tHeading, Attrs: map[string]any{"level": lv}, Content: canonInline(items, true)}
	applyTrailer(&n, tattrs)
	return n
}

func (c *conv) cellInline(cell ast.Node) []Node {
	items := c.inlineItems(cell, inlineCtx{inTable: true})
	items, _, _ = c.extractTrailers(items, false)
	items = inlineToText(c.restoreBlockTokens(items))
	return canonInline(items, true)
}

func (c *conv) convTable(t *extast.Table) Node {
	tbl := Node{Type: tTable}
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		typ := tTableCell
		if row.Kind() == extast.KindTableHeader {
			typ = tTableHeader
		}
		r := Node{Type: tTableRow}
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			p := Node{Type: tParagraph, Content: c.cellInline(cell)}
			r.Content = append(r.Content, Node{Type: typ, Content: []Node{p}})
		}
		tbl.Content = append(tbl.Content, r)
	}
	return tbl
}

// ---- 列表 ----

func (c *conv) convList(l *ast.List) []Node {
	var out []Node
	var cur *Node
	curType := ""
	idx := 0
	for ch := l.FirstChild(); ch != nil; ch = ch.NextSibling() {
		li, ok := ch.(*ast.ListItem)
		if !ok {
			continue
		}
		item, check := c.convListItem(li)
		typ := tBulletList
		switch {
		case check != nil:
			typ = tTaskList
		case l.IsOrdered():
			typ = tOrderedList
		}
		if cur == nil || typ != curType || (idx > 0 && c.realBlankBefore(li)) {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Node{Type: typ}
			if typ == tOrderedList {
				start := l.Start
				if idx > 0 {
					if num, ok := c.itemNumber(li); ok {
						start = num
					} else {
						start = l.Start + idx
					}
				}
				cur.Attrs = map[string]any{"start": start}
			}
			curType = typ
		}
		if typ == tTaskList {
			item.Type = tTaskItem
			item.Attrs = map[string]any{"checked": *check}
		} else {
			item.Type = tListItem
		}
		cur.Content = append(cur.Content, item)
		idx++
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// realBlankBefore 判断列表项前是否有原文里的空行（规范化插入的空行不算）。
func (c *conv) realBlankBefore(li *ast.ListItem) bool {
	pos := li.Pos()
	if pos < 0 {
		return false
	}
	ln := c.nz.lineOf(pos)
	for k := ln - 1; k >= 0 && isBlankish(c.nz.lines[k].text); k-- {
		if !c.nz.lines[k].synthetic {
			return true
		}
	}
	return false
}

// itemNumber 读取有序列表项的序号。
func (c *conv) itemNumber(li *ast.ListItem) (int, bool) {
	p := li.Pos()
	if p < 0 || p >= len(c.src) {
		return 0, false
	}
	for p < len(c.src) && (c.src[p] == ' ' || c.src[p] == '\t') {
		p++
	}
	n, digits := 0, 0
	for p < len(c.src) && isASCIIDigit(c.src[p]) && digits < 9 {
		n = n*10 + int(c.src[p]-'0')
		p++
		digits++
	}
	return n, digits > 0
}

func (c *conv) convListItem(li *ast.ListItem) (Node, *bool) {
	var ps []piece
	var check *bool
	first := true
	for ch := li.FirstChild(); ch != nil; ch = ch.NextSibling() {
		if first {
			switch ch.(type) {
			case *ast.Paragraph, *ast.TextBlock:
				r := c.convParagraph(ch)
				check = r.check
				ps = append(ps, r.pieces...)
				first = false
				continue
			}
		}
		first = false
		ps = append(ps, c.convBlock(ch)...)
	}
	children := c.build(ps)
	if len(children) == 0 || children[0].Type != tParagraph {
		children = append([]Node{emptyParagraph()}, children...)
	}
	return Node{Content: children}, check
}

// ---- 自定义标签组装 ----

type frame struct {
	tag   *tagTok
	nodes []Node
}

// hasCloseAhead 判断下一个标签事件是否为同名闭合标签。
func hasCloseAhead(ps []piece, i int, name string) bool {
	for j := i + 1; j < len(ps); j++ {
		if ps[j].tag != nil {
			return ps[j].tag.kind == tagClose && ps[j].tag.name == name
		}
	}
	return false
}

// build 按标签事件把一层块组装成容器：容器标签成对入栈，缺闭合的到层末自动闭合，
// 多余的闭合标签忽略；叶子标签有紧随的闭合标签时把中间内容当标题。
func (c *conv) build(ps []piece) []Node {
	stack := []*frame{{}}
	pop := func() {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		parent := stack[len(stack)-1]
		parent.nodes = append(parent.nodes, finalizeTag(f.tag, f.nodes)...)
	}
	for i := 0; i < len(ps); i++ {
		p := ps[i]
		top := stack[len(stack)-1]
		if p.tag == nil {
			top.nodes = append(top.nodes, p.nodes...)
			continue
		}
		t := p.tag
		switch {
		case t.kind == tagClose:
			k := -1
			for j := len(stack) - 1; j >= 1; j-- {
				if stack[j].tag.name == t.name {
					k = j
					break
				}
			}
			if k < 0 {
				continue
			}
			for len(stack)-1 >= k {
				pop()
			}
		case t.kind == tagSelf:
			top.nodes = append(top.nodes, finalizeTag(t, nil)...)
		case containerTag(t):
			stack = append(stack, &frame{tag: t})
		default:
			if hasCloseAhead(ps, i, t.name) {
				stack = append(stack, &frame{tag: t})
			} else {
				top.nodes = append(top.nodes, finalizeTag(t, nil)...)
			}
		}
	}
	for len(stack) > 1 {
		pop()
	}
	return postLevel(stack[0].nodes)
}

// postLevel 处理一层里的残留：孤立的 summary 变段落，孤立的 column 合并成分栏。
func postLevel(nodes []Node) []Node {
	var out []Node
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		switch n.Type {
		case pSummary:
			out = append(out, Node{Type: tParagraph, Content: n.Content})
		case tColumn:
			j := i
			var cols []Node
			for j < len(nodes) && nodes[j].Type == tColumn {
				cols = append(cols, nodes[j])
				j++
			}
			out = append(out, Node{Type: tColumns, Content: cols})
			i = j - 1
		default:
			out = append(out, n)
		}
	}
	return out
}

// plainOfBlocks 取若干块的纯文本（单行）。
func plainOfBlocks(ns []Node) string {
	var parts []string
	for _, n := range ns {
		if s := strings.TrimSpace(PlainText(n)); s != "" {
			parts = append(parts, strings.Join(strings.Fields(s), " "))
		}
	}
	return strings.Join(parts, " ")
}

// summaryInline 把 summary 内的块合成折叠标题的行内内容。
func summaryInline(ns []Node) []Node {
	var out []Node
	for i, n := range ns {
		if i > 0 {
			out = append(out, Node{Type: tHardBreak})
		}
		switch n.Type {
		case tParagraph, tHeading:
			out = append(out, n.Content...)
		default:
			if s := PlainText(n); s != "" {
				out = append(out, Node{Type: tText, Text: s})
			}
		}
	}
	return canonInline(out, true)
}

func finalizeTag(t *tagTok, children []Node) []Node {
	switch t.name {
	case "callout":
		n := Node{Type: tCallout}
		if v := t.get("icon"); v != "" {
			setAttr(&n, "icon", v)
		}
		if v := t.get("color"); v != "" {
			setAttr(&n, "color", v)
		}
		n.Content = ensurePlus(postLevel(children))
		return []Node{n}
	case "details":
		var title []Node
		found := false
		var body []Node
		for _, ch := range children {
			if ch.Type == pSummary && !found {
				title, found = ch.Content, true
				continue
			}
			body = append(body, ch)
		}
		n := Node{Type: tToggle}
		if lv := int(parseFloatOr(t.get("heading"), 0)); lv >= 1 && lv <= 3 {
			setAttr(&n, "level", lv)
		}
		if t.has("open") && t.get("open") != "false" {
			setAttr(&n, "open", true)
		}
		if v := t.get("color"); v != "" {
			setAttr(&n, "color", v)
		}
		n.Content = []Node{{Type: tToggleTitle, Content: title}, {Type: tToggleBody, Content: postLevel(body)}}
		return []Node{n}
	case "summary":
		return []Node{{Type: pSummary, Content: summaryInline(children)}}
	case "columns":
		var cols, stray []Node
		flush := func() {
			if len(stray) > 0 {
				cols = append(cols, Node{Type: tColumn, Content: postLevel(stray)})
				stray = nil
			}
		}
		for _, ch := range children {
			if ch.Type == tColumn {
				flush()
				cols = append(cols, ch)
			} else {
				stray = append(stray, ch)
			}
		}
		flush()
		if len(cols) == 0 {
			cols = []Node{{Type: tColumn, Content: []Node{emptyParagraph()}}}
		}
		return []Node{{Type: tColumns, Content: cols}}
	case "column":
		n := Node{Type: tColumn, Content: ensurePlus(postLevel(children))}
		if v := t.get("width"); v != "" {
			setAttr(&n, "width", parseNumberValue(v))
		}
		return []Node{n}
	case "synced-block":
		if src := t.get("source"); src != "" {
			n := Node{Type: tSyncedBlock, Attrs: map[string]any{"sourceId": src}}
			return append([]Node{n}, postLevel(children)...)
		}
		n := Node{Type: tSyncedBlock, Content: postLevel(children)}
		if id := t.get("id"); id != "" {
			setAttr(&n, "id", id)
		}
		return []Node{n}
	case "page", "database", "link-to-page":
		typ := map[string]string{"page": tChildPage, "database": tChildDatabase, "link-to-page": tLinkToPage}[t.name]
		n := Node{Type: typ, Attrs: map[string]any{"pageId": t.get("id")}}
		if t.name == "database" {
			if v := t.get("view"); v != "" {
				n.Attrs["viewId"] = v
			}
		}
		title := plainOfBlocks(children)
		if title == "" {
			title = t.get("title")
		}
		if t.get("id") == "" && title != "" {
			n.Attrs["title"] = title
		}
		return []Node{n}
	case "file":
		n := leafNode(tFileBlock, t, "src", "name", "mime")
		if v := t.get("size"); v != "" {
			n.Attrs["size"] = parseNumberValue(v)
		}
		return withTitle(n, "name", children)
	case "video", "audio":
		typ := tVideo
		if t.name == "audio" {
			typ = tAudio
		}
		return withTitle(leafNode(typ, t, "src", "title"), "title", children)
	case "bookmark":
		return withTitle(leafNode(tBookmark, t, "url", "title", "description", "image"), "title", children)
	case "embed":
		n := leafNode(tEmbed, t, "url")
		if v := t.get("height"); v != "" {
			n.Attrs["height"] = parseNumberValue(v)
		}
		return append([]Node{n}, postLevel(children)...)
	case "whiteboard":
		return withTitle(leafNode(tWhiteboard, t, "id", "title"), "title", children)
	case "toc":
		return append([]Node{{Type: tTOC}}, postLevel(children)...)
	case "breadcrumb":
		return append([]Node{{Type: tBreadcrumb}}, postLevel(children)...)
	case "empty-block":
		n := emptyParagraph()
		if v := t.get("color"); v != "" {
			setAttr(&n, "color", v)
		}
		if v := t.get("align"); v != "" {
			setAttr(&n, "textAlign", v)
		}
		return append([]Node{n}, postLevel(children)...)
	}
	return postLevel(children)
}

func parseFloatOr(s string, def float64) float64 {
	if f, ok := toFloat(s); ok {
		return f
	}
	return def
}

func leafNode(typ string, t *tagTok, keys ...string) Node {
	n := Node{Type: typ, Attrs: map[string]any{}}
	for _, k := range keys {
		if v := t.get(k); v != "" {
			n.Attrs[k] = v
		}
	}
	return n
}

// withTitle 成对写法时把中间文字当作标题类属性（属性里已有时不覆盖）。
func withTitle(n Node, key string, children []Node) []Node {
	if len(children) == 0 {
		return []Node{n}
	}
	if attrStr(n, key) == "" {
		if s := plainOfBlocks(children); s != "" {
			n.Attrs[key] = s
			return []Node{n}
		}
	}
	return append([]Node{n}, postLevel(children)...)
}
