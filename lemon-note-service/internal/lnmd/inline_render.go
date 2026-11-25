package lnmd

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/util"
)

// 行内渲染流程：先规范化（过滤表达不了的标记、合并相邻同标记文字、把粗斜体删除线两端的空白移到标记外、
// 去掉块首尾空白），再按标记栈输出。用 ** * ~~ 输出后会立即重新解析核对，结果不一致（多半是
// 定界符贴着标点、相邻定界符粘连）就整段改用 <strong> <em> <s> 标签输出，保证往返稳定。

var markRank = map[string]int{
	mLink: 0, mTextColor: 1, mHighlight: 2, mBold: 3, mItalic: 4, mStrike: 5,
	mUnderline: 6, mSubscript: 7, mSuperscript: 8, mCode: 9,
}

var markAlias = map[string]string{
	"strikethrough": mStrike, "textStyle": mTextColor, "color": mTextColor,
	"strong": mBold, "em": mItalic, "sub": mSubscript, "sup": mSuperscript,
}

func isDelimMark(t string) bool { return t == mBold || t == mItalic || t == mStrike }

// canonMarks 过滤并排序标记，只保留渲染得出来的属性。
func canonMarks(ms []Mark) []Mark {
	if len(ms) == 0 {
		return nil
	}
	var out []Mark
	for _, m := range ms {
		t := m.Type
		if a, ok := markAlias[t]; ok {
			t = a
		}
		if _, ok := markRank[t]; !ok {
			continue
		}
		if hasMarkType(out, t) {
			continue
		}
		var nm Mark
		switch t {
		case mLink:
			nm = linkMark(markAttrStr(m, "href"), markAttrStr(m, "title"))
		case mTextColor:
			col := strings.TrimSpace(markAttrStr(m, "color"))
			if col == "" {
				continue
			}
			nm = Mark{Type: t, Attrs: map[string]any{"color": col}}
		case mHighlight:
			nm = Mark{Type: t}
			if col := strings.TrimSpace(markAttrStr(m, "color")); col != "" {
				nm.Attrs = map[string]any{"color": col}
			}
		default:
			nm = Mark{Type: t}
		}
		out = append(out, nm)
	}
	sort.SliceStable(out, func(i, j int) bool { return markRank[out[i].Type] < markRank[out[j].Type] })
	return out
}

func hasMarkType(ms []Mark, t string) bool {
	for _, m := range ms {
		if m.Type == t {
			return true
		}
	}
	return false
}

func withoutType(ms []Mark, t string) []Mark {
	if !hasMarkType(ms, t) {
		return ms
	}
	var out []Mark
	for _, m := range ms {
		if m.Type != t {
			out = append(out, m)
		}
	}
	return out
}

func markEq(a, b Mark) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case mLink:
		return markAttrStr(a, "href") == markAttrStr(b, "href") && markAttrStr(a, "title") == markAttrStr(b, "title")
	case mTextColor, mHighlight:
		return markAttrStr(a, "color") == markAttrStr(b, "color")
	}
	return true
}

func marksEqual(a, b []Mark) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !markEq(a[i], b[i]) {
			return false
		}
	}
	return true
}

func containsMark(ms []Mark, m Mark) bool {
	for _, x := range ms {
		if markEq(x, m) {
			return true
		}
	}
	return false
}

// canonLatex 规范行内公式：去换行与首尾空白，补转义裸 $，去掉会吞掉闭合 $ 的结尾反斜杠。
func canonLatex(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	if strings.ContainsFunc(s, isPrivateTok) {
		// 占位符专用字符在公式里没有意义，换成替换字符，避免与规范化占位符冲突
		s = strings.Map(func(r rune) rune {
			if isPrivateTok(r) {
				return '�'
			}
			return r
		}, s)
	}
	s = trimSpaceTab(s)
	if strings.IndexByte(s, '$') >= 0 {
		var b strings.Builder
		bs := 0
		for _, r := range s {
			if r == '$' && bs%2 == 0 {
				b.WriteByte('\\')
			}
			if r == '\\' {
				bs++
			} else {
				bs = 0
			}
			b.WriteRune(r)
		}
		s = b.String()
	}
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	if n%2 == 1 {
		s = s[:len(s)-1]
	}
	return trimSpaceTab(s)
}

func canonMention(it Node) Node {
	n := Node{Type: tMention, Attrs: map[string]any{}, Marks: withoutType(canonMarks(it.Marks), mCode)}
	switch kind := attrStr(it, "kind"); kind {
	case "date":
		n.Attrs["kind"] = "date"
		n.Attrs["date"] = attrStr(it, "date")
		if e := attrStr(it, "endDate"); e != "" {
			n.Attrs["endDate"] = e
		}
	case "user":
		n.Attrs["kind"] = "user"
		n.Attrs["id"] = attrStr(it, "id")
	default:
		n.Attrs["kind"] = "page"
		n.Attrs["id"] = attrStr(it, "id")
	}
	return n
}

// canonInline 规范化行内内容；trim 为真时去掉块首尾空白（与 CommonMark 段落规则一致）。
func canonInline(items []Node, trim bool) []Node {
	var out []Node
	for _, it := range items {
		switch it.Type {
		case tText:
			ms := canonMarks(it.Marks)
			s := it.Text
			if strings.ContainsAny(s, "\r\x00") {
				s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\x00", "�").Replace(s)
			}
			if strings.IndexByte(s, '\n') < 0 {
				if s != "" {
					out = append(out, Node{Type: tText, Text: s, Marks: ms})
				}
				continue
			}
			for k, p := range strings.Split(s, "\n") {
				if k > 0 {
					out = append(out, Node{Type: tHardBreak, Marks: withoutType(ms, mCode)})
				}
				if p != "" {
					out = append(out, Node{Type: tText, Text: p, Marks: ms})
				}
			}
		case tHardBreak:
			out = append(out, Node{Type: tHardBreak, Marks: withoutType(canonMarks(it.Marks), mCode)})
		case tMention:
			out = append(out, canonMention(it))
		case tMathInline:
			latex := canonLatex(attrStr(it, "latex"))
			if latex == "" {
				continue
			}
			out = append(out, Node{Type: tMathInline, Attrs: map[string]any{"latex": latex},
				Marks: withoutType(canonMarks(it.Marks), mCode)})
		default:
			if it.Text != "" {
				out = append(out, canonInline([]Node{{Type: tText, Text: it.Text, Marks: it.Marks}}, false)...)
			} else if len(it.Content) > 0 {
				out = append(out, canonInline(it.Content, false)...)
			}
		}
	}
	out = mergeText(out)
	for k := 0; k < 6; k++ {
		next := mergeText(shiftDelimSpace(out))
		if inlineEqual(next, out) {
			break
		}
		out = next
	}
	if trim {
		out = trimEdges(out)
	}
	return mergeText(out)
}

func mergeText(items []Node) []Node {
	if len(items) < 2 {
		return items
	}
	out := make([]Node, 0, len(items))
	for _, it := range items {
		if it.Type == tText && it.Text == "" {
			continue
		}
		if n := len(out); n > 0 && it.Type == tText && out[n-1].Type == tText && marksEqual(out[n-1].Marks, it.Marks) {
			out[n-1].Text += it.Text
			continue
		}
		out = append(out, it)
	}
	return out
}

func isMDSpace(r rune) bool { return util.IsSpaceRune(r) || unicode.IsSpace(r) }

// shiftDelimSpace 把粗体、斜体、删除线区段两端的空白与换行挪到标记外。
func shiftDelimSpace(items []Node) []Node {
	for _, t := range []string{mBold, mItalic, mStrike} {
		var out []Node
		for i := 0; i < len(items); {
			if !hasMarkType(items[i].Marks, t) {
				out = append(out, items[i])
				i++
				continue
			}
			j := i
			for j < len(items) && hasMarkType(items[j].Marks, t) {
				j++
			}
			run := make([]Node, j-i)
			copy(run, items[i:j])
			var pre, post []Node
			for len(run) > 0 {
				f := run[0]
				if f.Type == tHardBreak {
					f.Marks = withoutType(f.Marks, t)
					pre = append(pre, f)
					run = run[1:]
					continue
				}
				if f.Type == tText && !hasMarkType(f.Marks, mCode) {
					tr := strings.TrimLeftFunc(f.Text, isMDSpace)
					if tr != f.Text {
						pre = append(pre, Node{Type: tText, Text: f.Text[:len(f.Text)-len(tr)], Marks: withoutType(f.Marks, t)})
						if tr == "" {
							run = run[1:]
							continue
						}
						run[0].Text = tr
					}
				}
				break
			}
			for len(run) > 0 {
				l := run[len(run)-1]
				if l.Type == tHardBreak {
					l.Marks = withoutType(l.Marks, t)
					post = append([]Node{l}, post...)
					run = run[:len(run)-1]
					continue
				}
				if l.Type == tText && !hasMarkType(l.Marks, mCode) {
					tr := strings.TrimRightFunc(l.Text, isMDSpace)
					if tr != l.Text {
						post = append([]Node{{Type: tText, Text: l.Text[len(tr):], Marks: withoutType(l.Marks, t)}}, post...)
						if tr == "" {
							run = run[:len(run)-1]
							continue
						}
						run[len(run)-1].Text = tr
					}
				}
				break
			}
			out = append(out, pre...)
			out = append(out, run...)
			out = append(out, post...)
			i = j
		}
		items = out
	}
	return items
}

const edgeSpace = " \t\v\f"

func trimEdges(items []Node) []Node {
	for len(items) > 0 && items[0].Type == tText && len(items[0].Marks) == 0 {
		t := strings.TrimLeft(items[0].Text, edgeSpace)
		if t == "" {
			items = items[1:]
			continue
		}
		if t != items[0].Text {
			items = append([]Node{{Type: tText, Text: t}}, items[1:]...)
		}
		break
	}
	for len(items) > 0 && items[len(items)-1].Type == tText && len(items[len(items)-1].Marks) == 0 {
		last := items[len(items)-1]
		t := strings.TrimRight(last.Text, edgeSpace)
		if t == "" {
			items = items[:len(items)-1]
			continue
		}
		if t != last.Text {
			cp := make([]Node, len(items))
			copy(cp, items)
			cp[len(cp)-1] = Node{Type: tText, Text: t}
			items = cp
		}
		break
	}
	return items
}

// inlineEqual 比较两段规范化后的行内内容。
func inlineEqual(a, b []Node) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Type != y.Type || x.Text != y.Text || !marksEqual(x.Marks, y.Marks) {
			return false
		}
		switch x.Type {
		case tMention:
			for _, k := range []string{"kind", "id", "date", "endDate"} {
				if attrStr(x, k) != attrStr(y, k) {
					return false
				}
			}
		case tMathInline:
			if attrStr(x, "latex") != attrStr(y, "latex") {
				return false
			}
		}
	}
	return true
}

// ---- 转义 ----

type escFlags struct {
	atStart   bool // 文字位于行首
	afterMath bool // 紧跟在行内公式的闭合 $ 后面
	inTable   bool
	heading   bool
}

func isWordRune(r rune) bool { return !isMDSpace(r) && !util.IsPunctRune(r) }

// entityLike 判断 & 开头是否形似实体引用。
func entityLike(rs []rune) bool {
	if len(rs) < 3 || rs[0] != '&' {
		return false
	}
	i := 1
	if rs[i] == '#' {
		i++
		if i < len(rs) && (rs[i] == 'x' || rs[i] == 'X') {
			i++
		}
	}
	s := i
	for i < len(rs) && i-s < 40 && (rs[i] < 0x80 && isASCIIAlnum(byte(rs[i]))) {
		i++
	}
	return i > s && i < len(rs) && rs[i] == ';'
}

// escapeText 转义文字中的 Markdown 特殊字符，保证解析回来一字不差。
func escapeText(s string, f escFlags) string {
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s) + 8)
	olEnd := -1
	if f.atStart {
		k := 0
		for k < len(rs) && k < 9 && rs[k] >= '0' && rs[k] <= '9' {
			k++
		}
		if k > 0 && k < len(rs) && (rs[k] == '.' || rs[k] == ')') {
			olEnd = k
		}
	}
	for i, r := range rs {
		prev, next := rune(-1), rune(-1)
		if i > 0 {
			prev = rs[i-1]
		}
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		if i == 0 && f.afterMath && r >= '0' && r <= '9' {
			fmt.Fprintf(&b, "&#%d;", r)
			continue
		}
		if i == olEnd {
			b.WriteByte('\\')
			b.WriteRune(r)
			continue
		}
		esc := false
		switch r {
		case '\\':
			// 下一个字符本身是标点，或会被写成以 & 开头的实体时，反斜杠必须转义
			esc = next == -1 || (next < 0x80 && isASCIIPunct(byte(next))) || isPrivateTok(next)
		case '*', '`', '[', ']', '~', '$':
			esc = true
		case '_':
			esc = !(prev != -1 && next != -1 && isWordRune(prev) && isWordRune(next))
		case '<':
			esc = next == -1 || (next < 0x80 && (isASCIIAlnum(byte(next)) || next == '/' || next == '!' || next == '?'))
		case '#':
			esc = (i == 0 && f.atStart) || f.heading
		case '>', '-', '+', '=':
			esc = i == 0 && f.atStart
		case '|':
			esc = f.inTable || (i == 0 && f.atStart)
		case '!':
			esc = next == -1
		case '&':
			esc = next == -1 || entityLike(rs[i:])
		}
		if isPrivateTok(r) {
			fmt.Fprintf(&b, "&#x%X;", r)
			continue
		}
		if esc {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// codeSpan 生成行内代码，反引号数量与首尾补空格按 CommonMark 规则计算。
func codeSpan(s string, inTable bool) string {
	if inTable {
		s = strings.ReplaceAll(s, "|", `\|`)
	}
	f := strings.Repeat("`", maxRun(s, '`')+1)
	pad := s[0] == '`' || s[len(s)-1] == '`' ||
		(s[0] == ' ' && s[len(s)-1] == ' ' && strings.Trim(s, " \t\n\v\f\r") != "")
	if pad {
		return f + " " + s + " " + f
	}
	return f + s + f
}

// linkDest 生成链接目标（含可选标题）。
func linkDest(href, title string, inTable bool) string {
	href = strings.NewReplacer("\r\n", "%0A", "\n", "%0A", "\r", "%0D").Replace(href)
	pointy := href == "" && title != "" || strings.ContainsAny(href, " \t<>")
	rs := []rune(href)
	var b strings.Builder
	if pointy {
		b.WriteByte('<')
	}
	for i, r := range rs {
		next := rune(-1)
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		switch {
		case r == '\\':
			if next == -1 || (next < 0x80 && isASCIIPunct(byte(next))) {
				b.WriteByte('\\')
			}
		case r == '(' || r == ')':
			if !pointy {
				b.WriteByte('\\')
			}
		case r == '<' || r == '>':
			b.WriteByte('\\')
		case r == '&':
			if entityLike(rs[i:]) {
				b.WriteByte('\\')
			}
		case r == '|':
			if inTable {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	if pointy {
		b.WriteByte('>')
	}
	if title != "" {
		title = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(title)
		trs := []rune(title)
		b.WriteString(` "`)
		for i, r := range trs {
			next := rune(-1)
			if i+1 < len(trs) {
				next = trs[i+1]
			}
			switch {
			case r == '"':
				b.WriteByte('\\')
			case r == '\\':
				if next == -1 || (next < 0x80 && isASCIIPunct(byte(next))) {
					b.WriteByte('\\')
				}
			case r == '&':
				if entityLike(trs[i:]) {
					b.WriteByte('\\')
				}
			case r == '|':
				if inTable {
					b.WriteByte('\\')
				}
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	return b.String()
}

// ---- 行内渲染 ----

type inlineFlags struct {
	atStart bool
	inTable bool
	heading bool
}

func (r *renderer) inline(items []Node, f inlineFlags) string {
	items = canonInline(items, true)
	if len(items) == 0 {
		return ""
	}
	s, risky := r.inlineMode(items, f, false)
	if risky && !verifyInline(s, items, f) {
		s, _ = r.inlineMode(items, f, true)
	}
	return s
}

func runEnd(items []Node, i int, m Mark) int {
	j := i + 1
	for j < len(items) && containsMark(items[j].Marks, m) {
		j++
	}
	return j
}

func (r *renderer) inlineMode(items []Node, f inlineFlags, htmlMode bool) (string, bool) {
	var b strings.Builder
	var stack []Mark
	risky := false
	afterMath := false
	closeTo := func(k int) {
		for j := len(stack) - 1; j >= k; j-- {
			b.WriteString(r.closeMark(stack[j], htmlMode, f.inTable))
			afterMath = false
		}
		stack = stack[:k]
	}
	for i, it := range items {
		want := withoutType(it.Marks, mCode)
		k := 0
		for k < len(stack) && containsMark(want, stack[k]) {
			k++
		}
		closeTo(k)
		var toOpen []Mark
		for _, m := range want {
			if !containsMark(stack, m) {
				toOpen = append(toOpen, m)
			}
		}
		if len(toOpen) > 1 {
			ends := make([]int, len(toOpen))
			for x, m := range toOpen {
				ends[x] = runEnd(items, i, m)
			}
			idx := make([]int, len(toOpen))
			for x := range idx {
				idx[x] = x
			}
			sort.SliceStable(idx, func(a, c int) bool {
				ea, ec := ends[idx[a]], ends[idx[c]]
				if ea != ec {
					return ea > ec
				}
				return markRank[toOpen[idx[a]].Type] < markRank[toOpen[idx[c]].Type]
			})
			sorted := make([]Mark, len(toOpen))
			for x, id := range idx {
				sorted[x] = toOpen[id]
			}
			toOpen = sorted
		}
		for _, m := range toOpen {
			b.WriteString(r.openMark(m, htmlMode, f.inTable))
			stack = append(stack, m)
			afterMath = false
			if !htmlMode && isDelimMark(m.Type) {
				risky = true
			}
		}
		atStart := f.atStart && b.Len() == 0
		switch it.Type {
		case tText:
			if hasMarkType(it.Marks, mCode) {
				if htmlMode {
					b.WriteString("<code>" + escapeText(it.Text, escFlags{inTable: f.inTable}) + "</code>")
				} else {
					b.WriteString(codeSpan(it.Text, f.inTable))
				}
			} else {
				b.WriteString(escapeText(it.Text, escFlags{atStart: atStart, afterMath: afterMath, inTable: f.inTable, heading: f.heading}))
			}
			afterMath = false
		case tHardBreak:
			b.WriteString("<br>")
			afterMath = false
		case tMention:
			b.WriteString(r.mention(it, f.inTable))
			afterMath = false
		case tMathInline:
			latex := attrStr(it, "latex")
			if f.inTable {
				latex = strings.ReplaceAll(latex, "|", `\|`)
			}
			b.WriteString("$" + latex + "$")
			afterMath = true
		}
	}
	closeTo(0)
	s := b.String()
	if pos, _, ok := findTrailer(s); ok {
		s = s[:pos] + `\` + s[pos:]
	}
	return s, risky
}

func (r *renderer) openMark(m Mark, htmlMode, inTable bool) string {
	switch m.Type {
	case mBold:
		if htmlMode {
			return "<strong>"
		}
		return "**"
	case mItalic:
		if htmlMode {
			return "<em>"
		}
		return "*"
	case mStrike:
		if htmlMode {
			return "<s>"
		}
		return "~~"
	case mUnderline:
		return "<u>"
	case mSubscript:
		return "<sub>"
	case mSuperscript:
		return "<sup>"
	case mTextColor:
		return newTagT("span", inTable).attr("color", markAttrStr(m, "color")).open()
	case mHighlight:
		return newTagT("mark", inTable).attr("color", markAttrStr(m, "color")).open()
	case mLink:
		return "["
	}
	return ""
}

func (r *renderer) closeMark(m Mark, htmlMode, inTable bool) string {
	switch m.Type {
	case mBold:
		if htmlMode {
			return "</strong>"
		}
		return "**"
	case mItalic:
		if htmlMode {
			return "</em>"
		}
		return "*"
	case mStrike:
		if htmlMode {
			return "</s>"
		}
		return "~~"
	case mUnderline:
		return "</u>"
	case mSubscript:
		return "</sub>"
	case mSuperscript:
		return "</sup>"
	case mTextColor:
		return "</span>"
	case mHighlight:
		return "</mark>"
	case mLink:
		return "](" + linkDest(markAttrStr(m, "href"), markAttrStr(m, "title"), inTable) + ")"
	}
	return ""
}

func newTagT(name string, inTable bool) *tagBuilder {
	t := newTag(name)
	t.inTable = inTable
	return t
}

func lookup(fn func(string) (string, bool), id string) string {
	if fn == nil || id == "" {
		return ""
	}
	if s, ok := fn(id); ok {
		return s
	}
	return ""
}

func (r *renderer) mention(n Node, inTable bool) string {
	switch attrStr(n, "kind") {
	case "date":
		return newTagT("mention-date", inTable).attr("date", attrStr(n, "date")).attr("end", attrStr(n, "endDate")).self()
	case "user":
		id := attrStr(n, "id")
		tb := newTagT("mention-user", inTable).attr("id", id)
		if name := lookup(r.o.UserName, id); name != "" {
			return tb.open() + escapeText(oneLine(name), escFlags{inTable: inTable}) + "</mention-user>"
		}
		return tb.self()
	default:
		id := attrStr(n, "id")
		tb := newTagT("mention-page", inTable).attr("id", id)
		if title := lookup(r.o.PageTitle, id); title != "" {
			return tb.open() + escapeText(oneLine(title), escFlags{inTable: inTable}) + "</mention-page>"
		}
		return tb.self()
	}
}

func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// verifyInline 把渲染结果重新解析，核对与预期一致。
func verifyInline(s string, want []Node, f inlineFlags) bool {
	var got []Node
	if f.inTable {
		blocks, _ := Parse("| " + s + " |\n| --- |")
		if len(blocks) != 1 || blocks[0].Type != tTable || len(blocks[0].Content) == 0 {
			return false
		}
		row := blocks[0].Content[0]
		if len(row.Content) != 1 || len(row.Content[0].Content) != 1 {
			return false
		}
		got = row.Content[0].Content[0].Content
	} else {
		blocks, _ := Parse(s)
		if len(blocks) != 1 || blocks[0].Type != tParagraph || len(blocks[0].Attrs) > 0 {
			return false
		}
		got = blocks[0].Content
	}
	return inlineEqual(canonInline(got, true), want)
}
