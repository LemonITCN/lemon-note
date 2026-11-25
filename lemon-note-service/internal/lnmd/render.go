package lnmd

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

type renderer struct {
	o RenderOptions
}

// RenderBlock 把一个顶层块渲染成增强 Markdown（末尾不带空行）。
func RenderBlock(n Node, o RenderOptions) string {
	return (&renderer{o: o}).block(n)
}

// Render 渲染整页，顶层块之间空一行。
func Render(blocks []Node, o RenderOptions) string {
	r := &renderer{o: o}
	parts := make([]string, len(blocks))
	for i, b := range blocks {
		parts[i] = r.block(b)
	}
	return strings.Join(parts, "\n\n")
}

func (r *renderer) blocks(ns []Node) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = r.block(n)
	}
	return strings.Join(parts, "\n\n")
}

func (r *renderer) block(n Node) string {
	switch n.Type {
	case tParagraph:
		return r.paragraphLine(n)
	case tHeading:
		return r.heading(n)
	case tBulletList, tOrderedList, tTaskList:
		return r.list(n)
	case tListItem:
		return r.list(Node{Type: tBulletList, Content: []Node{n}})
	case tTaskItem:
		return r.list(Node{Type: tTaskList, Content: []Node{n}})
	case tToggle:
		return r.toggle(n)
	case tBlockquote:
		return prefixLines(r.blocks(childBlocksPlus(n)), "> ", ">")
	case tCallout:
		open := newTag("callout").attr("icon", attrStr(n, "icon")).attr("color", attrStr(n, "color")).open()
		return open + "\n" + r.blocks(childBlocksPlus(n)) + "\n</callout>"
	case tCodeBlock:
		return fenced(sanitizeInfo(attrStr(n, "language")), codeText(n))
	case tHorizontalRule:
		return "---"
	case tImage:
		return r.image(n)
	case tFileBlock:
		return newTag("file").attr("src", attrStr(n, "src")).attr("name", attrStr(n, "name")).
			attr("size", attrStr(n, "size")).attr("mime", attrStr(n, "mime")).self()
	case tVideo:
		return newTag("video").attr("src", attrStr(n, "src")).attr("title", attrStr(n, "title")).self()
	case tAudio:
		return newTag("audio").attr("src", attrStr(n, "src")).attr("title", attrStr(n, "title")).self()
	case tBookmark:
		return newTag("bookmark").attr("url", attrStr(n, "url")).attr("title", attrStr(n, "title")).
			attr("description", attrStr(n, "description")).attr("image", attrStr(n, "image")).self()
	case tEmbed:
		return newTag("embed").attr("url", attrStr(n, "url")).attr("height", attrStr(n, "height")).self()
	case tTable:
		return r.table(n)
	case tColumns:
		return r.columns(n)
	case tColumn:
		return r.columns(Node{Type: tColumns, Content: []Node{n}})
	case tMathBlock:
		return mathBlock(normNL(attrStr(n, "latex")))
	case tMermaid:
		return fenced("mermaid", normNL(attrStr(n, "code")))
	case tGraphviz:
		info := "dot"
		if e := strings.TrimSpace(attrStr(n, "engine")); e != "" && e != "dot" {
			info += " engine=" + strings.Join(strings.Fields(sanitizeInfo(e)), "")
		}
		return fenced(info, normNL(attrStr(n, "code")))
	case tMindmap:
		return fenced("mindmap", renderMindmap(n.Attrs["data"]))
	case tChart:
		return fenced("chart", renderChart(n.Attrs["data"]))
	case tWhiteboard:
		return newTag("whiteboard").attr("id", nodeID(n)).attr("title", oneLine(attrStr(n, "title"))).self()
	case tChildPage:
		return r.pageRef("page", n, "")
	case tChildDatabase:
		return r.pageRef("database", n, attrStr(n, "viewId"))
	case tLinkToPage:
		return r.pageRef("link-to-page", n, "")
	case tSyncedBlock:
		if src := attrStr(n, "sourceId"); src != "" {
			return newTag("synced-block").attr("source", src).self()
		}
		open := newTag("synced-block").attr("id", nodeID(n)).open()
		if len(n.Content) == 0 {
			return open + "\n</synced-block>"
		}
		return open + "\n" + r.blocks(n.Content) + "\n</synced-block>"
	case tTOC:
		return "<toc/>"
	case tBreadcrumb:
		return "<breadcrumb/>"
	}
	return r.unknown(n)
}

// unknown 渲染不认识的块：按纯文本段落输出，没有文字时输出空行块，保证解析回来仍是一个块。
func (r *renderer) unknown(n Node) string {
	s := strings.TrimSpace(PlainText(n))
	if s == "" {
		return "<empty-block/>"
	}
	return r.paragraphLine(Node{Type: tParagraph, Content: []Node{{Type: tText, Text: s}}})
}

func normNL(s string) string {
	if strings.IndexByte(s, '\r') < 0 {
		return s
	}
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

// prefixLines 给每行加前缀。只含空白的行按空行处理：CommonMark 在列表项、引用里把它们当空行，
// 其中的空白（例如代码块里的空白行）本来就保留不下来，这里统一去掉以保证渲染稳定。
func prefixLines(s, prefix, blank string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.Trim(l, " \t") == "" {
			lines[i] = blank
		} else {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

func indentLines(s string, pad string) string {
	return prefixLines(s, pad, "")
}

func sanitizeInfo(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "`", "").Replace(s)
	return strings.TrimSpace(s)
}

func codeText(n Node) string {
	var b strings.Builder
	for _, c := range n.Content {
		if c.Type == tText {
			b.WriteString(c.Text)
		} else if c.Type == tHardBreak {
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 && n.Text != "" {
		b.WriteString(n.Text)
	}
	return normNL(b.String())
}

// fenced 生成围栏代码块，内容含反引号串时自动加长围栏。
func fenced(info, content string) string {
	if strings.Trim(content, " \t") == "" {
		// 只有空白的单行内容在容器里会变成空行，统一按空内容输出
		content = ""
	}
	f := fenceFor(content)
	if content == "" {
		return f + info + "\n" + f
	}
	return f + info + "\n" + content + "\n" + f
}

func mathBlock(latex string) string {
	if strings.TrimSpace(latex) == "" {
		latex = ""
	}
	needFence := strings.Contains(latex, "$$")
	if !needFence {
		for _, l := range strings.Split(latex, "\n") {
			if latex != "" && strings.TrimSpace(l) == "" {
				needFence = true
				break
			}
		}
	}
	if needFence {
		return fenced("math", latex)
	}
	if latex == "" {
		return "$$\n$$"
	}
	return "$$\n" + latex + "\n$$"
}

func blockColor(n Node) string {
	c := strings.TrimSpace(attrStr(n, "color"))
	if c == "default" {
		return ""
	}
	return c
}

func blockAlign(n Node) string {
	a := strings.TrimSpace(attrStr(n, "textAlign"))
	if a == "left" {
		return ""
	}
	return a
}

func textTrailer(n Node) string {
	return formatTrailer([][2]string{{"color", blockColor(n)}, {"align", blockAlign(n)}}, nil)
}

func emptyBlockTag(n Node) string {
	return newTag("empty-block").attr("color", blockColor(n)).attr("align", blockAlign(n)).self()
}

// paragraphLine 渲染段落：空段落写 <empty-block/>，块颜色与对齐写成行尾尾注。
func (r *renderer) paragraphLine(n Node) string {
	s := r.inline(n.Content, inlineFlags{atStart: true})
	if s == "" {
		return emptyBlockTag(n)
	}
	if tr := textTrailer(n); tr != "" {
		s += " " + tr
	}
	return s
}

func (r *renderer) heading(n Node) string {
	lv := attrInt(n, "level", 1)
	if lv < 1 {
		lv = 1
	}
	if lv > 3 {
		lv = 3
	}
	out := strings.Repeat("#", lv)
	if s := r.inline(n.Content, inlineFlags{atStart: true, heading: true}); s != "" {
		out += " " + s
	}
	if tr := textTrailer(n); tr != "" {
		out += " " + tr
	}
	return out
}

func (r *renderer) list(n Node) string {
	items := n.Content
	if len(items) == 0 {
		items = []Node{{Type: tListItem}}
	}
	start := 1
	if n.Type == tOrderedList {
		start = attrInt(n, "start", 1)
		if start < 0 {
			start = 0
		}
		if maxStart := 999999999 - len(items) + 1; start > maxStart {
			start = maxStart
		}
	}
	lines := make([]string, len(items))
	for i, it := range items {
		marker, indent := "- ", 2
		switch n.Type {
		case tOrderedList:
			marker = strconv.Itoa(start+i) + ". "
			indent = len(marker)
		case tTaskList:
			if attrBool(it, "checked") {
				marker = "- [x] "
			} else {
				marker = "- [ ] "
			}
		}
		lines[i] = r.listItem(it, marker, indent)
	}
	// 只有标记的空列表项后面若紧跟空行（列表末项、下一项以标签开头时规范化会插空行），
	// goldmark 会连外层列表项一起结束，所以这些位置改写成 <empty-block/>。
	for i := len(lines) - 1; i >= 0; i-- {
		if !bareItem(lines[i]) {
			continue
		}
		if i+1 < len(lines) && !strings.HasPrefix(itemContent(lines[i+1]), "<") {
			continue
		}
		lines[i] += " <empty-block/>"
	}
	return strings.Join(lines, "\n")
}

// bareItem 判断渲染出的列表项是否只有标记（"-"、"1."、无子块）。
func bareItem(s string) bool {
	return !strings.Contains(s, "\n") && bareMarker(s)
}

// itemContent 去掉列表项首行的标记，返回其后的内容。
func itemContent(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	if strings.HasPrefix(first, "- ") {
		first = first[2:]
		if strings.HasPrefix(first, "[ ] ") || strings.HasPrefix(first, "[x] ") {
			first = first[4:]
		}
		return first
	}
	if _, rest, ok := strings.Cut(first, ". "); ok {
		return rest
	}
	return ""
}

// bareMarker 判断一行是否只有列表标记（空列表项）。
func bareMarker(line string) bool {
	if line == "-" {
		return true
	}
	if strings.HasSuffix(line, ".") {
		d := line[:len(line)-1]
		if d == "" {
			return false
		}
		for i := 0; i < len(d); i++ {
			if !isASCIIDigit(d[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// canInterrupt 判断嵌套列表能否紧跟在段落下一行（不空行）而不被当成段落续行。
func canInterrupt(c Node, rendered string) bool {
	first, _, _ := strings.Cut(rendered, "\n")
	if bareMarker(first) {
		return false
	}
	switch c.Type {
	case tBulletList, tTaskList:
		return true
	case tOrderedList:
		return strings.HasPrefix(first, "1. ")
	}
	return false
}

func (r *renderer) listItem(it Node, marker string, indent int) string {
	children := it.Content
	var first string
	var rest []Node
	if len(children) > 0 && children[0].Type == tParagraph {
		rest = children[1:]
		line := r.paragraphLine(children[0])
		if line == "<empty-block/>" && len(rest) == 0 {
			first = strings.TrimRight(marker, " ")
		} else {
			first = marker + line
		}
	} else {
		rest = children
		if len(rest) == 0 {
			first = strings.TrimRight(marker, " ")
		} else {
			first = marker + "<empty-block/>"
		}
	}
	var b strings.Builder
	b.WriteString(first)
	pad := strings.Repeat(" ", indent)
	for i, c := range rest {
		s := r.block(c)
		if i == 0 && canInterrupt(c, s) {
			b.WriteString("\n")
		} else {
			b.WriteString("\n\n")
		}
		b.WriteString(indentLines(s, pad))
	}
	return b.String()
}

func (r *renderer) toggle(n Node) string {
	var title, body []Node
	gotTitle := false
	for _, c := range n.Content {
		switch {
		case c.Type == tToggleTitle && !gotTitle:
			title, gotTitle = c.Content, true
		case c.Type == tToggleBody:
			body = append(body, c.Content...)
		case c.Type == tToggleTitle:
			body = append(body, Node{Type: tParagraph, Content: c.Content})
		default:
			body = append(body, c)
		}
	}
	tb := newTag("details")
	if lv := attrInt(n, "level", 0); lv >= 1 && lv <= 3 {
		tb.attr("heading", strconv.Itoa(lv))
	}
	tb.flag("open", attrBool(n, "open"))
	tb.attr("color", blockColor(n))
	s := tb.open() + "\n<summary>" + r.inline(title, inlineFlags{atStart: true}) + "</summary>"
	if len(body) > 0 {
		s += "\n" + r.blocks(body)
	}
	return s + "\n</details>"
}

func (r *renderer) image(n Node) string {
	alt := oneLine(attrStr(n, "alt"))
	s := "![" + escapeText(alt, escFlags{}) + "](" + linkDest(attrStr(n, "src"), attrStr(n, "title"), false) + ")"
	tr := formatTrailer([][2]string{
		{"width", attrStr(n, "width")}, {"height", attrStr(n, "height")}, {"align", attrStr(n, "align")},
	}, map[string]bool{"width": true, "height": true})
	return s + tr
}

func (r *renderer) pageTitle(n Node) string {
	if t := lookup(r.o.PageTitle, attrStr(n, "pageId")); t != "" {
		return t
	}
	return attrStr(n, "title")
}

func (r *renderer) pageRef(tag string, n Node, view string) string {
	tb := newTag(tag).attr("id", attrStr(n, "pageId")).attr("view", view)
	title := r.pageTitle(n)
	if strings.TrimSpace(title) == "" {
		return tb.self()
	}
	return tb.open() + r.inline([]Node{{Type: tText, Text: oneLine(title)}}, inlineFlags{atStart: true}) + closeTag(tag)
}

func (r *renderer) columns(n Node) string {
	cols := n.Content
	if len(cols) == 0 {
		cols = []Node{{Type: tColumn}}
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		if c.Type != tColumn {
			c = Node{Type: tColumn, Content: []Node{c}}
		}
		tb := newTag("column")
		if w, ok := attrNum(c, "width"); ok {
			tb.attr("width", formatFloat(w))
		} else {
			tb.attr("width", attrStr(c, "width"))
		}
		parts[i] = tb.open() + "\n" + r.blocks(childBlocksPlus(c)) + "\n</column>"
	}
	return "<columns>\n" + strings.Join(parts, "\n") + "\n</columns>"
}

// ---- 表格 ----

// tableGrid 把含合并单元格的表格展开成规整网格，被合并覆盖的位置为空单元格。
func tableGrid(n Node) [][]*Node {
	var grid [][]*Node
	occupied := map[[2]int]bool{}
	for ri, row := range n.Content {
		for len(grid) <= ri {
			grid = append(grid, nil)
		}
		cells := row.Content
		if row.Type != tTableRow {
			cells = []Node{row}
		}
		col := 0
		for ci := range cells {
			cell := &cells[ci]
			for occupied[[2]int{ri, col}] {
				col++
			}
			cs := attrInt(*cell, "colspan", 1)
			rs := attrInt(*cell, "rowspan", 1)
			if cs < 1 || cs > 1000 {
				cs = 1
			}
			if rs < 1 || rs > 1000 {
				rs = 1
			}
			for dr := 0; dr < rs; dr++ {
				for dc := 0; dc < cs; dc++ {
					occupied[[2]int{ri + dr, col + dc}] = true
				}
			}
			for len(grid[ri]) < col+1 {
				grid[ri] = append(grid[ri], nil)
			}
			grid[ri][col] = cell
			col += cs
			for len(grid[ri]) < col {
				grid[ri] = append(grid[ri], nil)
			}
		}
	}
	for pos := range occupied {
		if pos[0] < len(grid) {
			for len(grid[pos[0]]) <= pos[1] {
				grid[pos[0]] = append(grid[pos[0]], nil)
			}
		}
	}
	return grid
}

func (r *renderer) cellText(cell *Node) string {
	if cell == nil {
		return ""
	}
	var parts []string
	allPara := true
	for _, c := range cell.Content {
		if c.Type != tParagraph {
			allPara = false
		}
	}
	if !allPara || (len(cell.Content) == 0 && (cell.Text != "" || cell.Type == tText)) {
		s := strings.TrimSpace(PlainText(*cell))
		return r.inline([]Node{{Type: tText, Text: s}}, inlineFlags{inTable: true})
	}
	for _, p := range cell.Content {
		parts = append(parts, r.inline(p.Content, inlineFlags{inTable: true}))
	}
	return strings.Join(parts, "<br>")
}

func (r *renderer) table(n Node) string {
	grid := tableGrid(n)
	cols := 1
	for _, row := range grid {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if len(grid) == 0 {
		grid = [][]*Node{{nil}}
	}
	var b strings.Builder
	for ri, row := range grid {
		b.WriteString("|")
		for c := 0; c < cols; c++ {
			var cell *Node
			if c < len(row) {
				cell = row[c]
			}
			b.WriteString(" ")
			b.WriteString(r.cellText(cell))
			b.WriteString(" |")
		}
		if ri == 0 {
			b.WriteString("\n|")
			for c := 0; c < cols; c++ {
				b.WriteString(" --- |")
			}
		}
		if ri < len(grid)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ---- 图表 ----

func renderChart(data any) string {
	if data == nil {
		return "{}"
	}
	if s, ok := data.(string); ok {
		var v any
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		if dec.Decode(&v) == nil {
			data = v
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return "{}"
	}
	return strings.TrimRight(buf.String(), "\n")
}
