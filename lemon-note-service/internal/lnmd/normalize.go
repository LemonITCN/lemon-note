package lnmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 规范化预处理：把自定义块级标签切到独立行并替换成占位符、把 $$ 公式块改写成 ```math 围栏、
// 把行尾块属性尾注替换成占位符，再交给 goldmark。围栏代码块与行内代码里的内容不动。
//
// 占位符使用私用区字符，原文里出现的同类字符会先转成实体，保证占位符唯一。

const (
	tokBlkOpen  = ''
	tokBlkClose = ''
	tokTrlOpen  = ''
	tokTrlClose = ''
)

func isPrivateTok(r rune) bool { return r >= tokBlkOpen && r <= tokTrlClose }

type normLine struct {
	text      string
	synthetic bool // 规范化插入的空行
	tag       int  // 标签占位行对应的 tags 下标；-1 表示不是
	wsPrefix  bool // 行前缀只有空白（可以做缩进收拢）
}

type trailerTok struct {
	raw   string
	attrs []attrKV
}

type normResult struct {
	src       []byte
	lines     []normLine
	lineStart []int
	tags      []*tagTok
	trailers  []trailerTok
}

func blockToken(i int) string {
	return string(tokBlkOpen) + strconv.Itoa(i) + string(tokBlkClose)
}

func trailerToken(i int) string {
	return string(tokTrlOpen) + strconv.Itoa(i) + string(tokTrlClose)
}

// normalize 执行规范化预处理。
func normalize(md string) *normResult {
	md = strings.ToValidUTF8(md, "�")
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")
	md = strings.ReplaceAll(md, "\x00", "�")
	src := strings.Split(md, "\n")
	nz := &normResult{}

	inFence := false
	var fenceCh byte
	fenceN := 0
	for i := 0; i < len(src); i++ {
		line := src[i]
		if inFence {
			nz.emit(line, false, -1, false)
			if fenceCloses(line, fenceCh, fenceN) {
				inFence = false
			}
			continue
		}
		prefix, cont, rest := splitPrefix(line)
		if ch, n, ok := fenceOpens(rest); ok {
			inFence, fenceCh, fenceN = true, ch, n
			nz.emit(line, false, -1, false)
			continue
		}
		if nz.tryMath(src, &i, prefix, cont, rest) {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(rest, " \t"), "|") {
			// 表格行：不切标签、不认尾注，只转义私用区字符
			nz.emit(prefix+escapePrivate(rest), false, -1, false)
			continue
		}
		nz.processLine(prefix, cont, rest)
	}
	nz.dedent(0, len(nz.lines))

	var b strings.Builder
	nz.lineStart = make([]int, len(nz.lines))
	for i, l := range nz.lines {
		nz.lineStart[i] = b.Len()
		b.WriteString(l.text)
		b.WriteByte('\n')
	}
	nz.src = []byte(b.String())
	return nz
}

func (nz *normResult) emit(text string, synthetic bool, tag int, ws bool) {
	nz.lines = append(nz.lines, normLine{text: text, synthetic: synthetic, tag: tag, wsPrefix: ws})
}

// lineOf 返回源码偏移所在的行号。
func (nz *normResult) lineOf(pos int) int {
	i := sort.Search(len(nz.lineStart), func(i int) bool { return nz.lineStart[i] > pos })
	return i - 1
}

func isBlankish(s string) bool { return strings.Trim(s, " \t>") == "" }

func isWSOnly(s string) bool { return strings.Trim(s, " \t") == "" }

// splitPrefix 切出行首的容器前缀（缩进、引用 >、列表标记与任务框），返回原前缀、续行前缀与余下内容。
// 续行前缀把列表标记换成等宽空格，任务框不计入续行缩进。
func splitPrefix(line string) (prefix, cont, rest string) {
	var cb strings.Builder
	i := 0
	for {
		j := i
		for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
			j++
		}
		if j < len(line) && line[j] == '>' {
			cb.WriteString(line[i:j])
			cb.WriteByte('>')
			j++
			if j < len(line) && line[j] == ' ' {
				cb.WriteByte(' ')
				j++
			}
			i = j
			continue
		}
		if ml := listMarkerAt(line, j); ml > 0 {
			k := j + ml
			sp := 0
			for k+sp < len(line) && line[k+sp] == ' ' {
				sp++
			}
			if k+sp >= len(line) {
				cb.WriteString(line[i:j])
				cb.WriteString(strings.Repeat(" ", ml+1))
				i = len(line)
				break
			}
			if sp == 0 {
				cb.WriteString(line[i:j])
				i = j
				break
			}
			if sp > 4 {
				sp = 1
			}
			cb.WriteString(line[i:j])
			cb.WriteString(strings.Repeat(" ", ml+sp))
			i = k + sp
			if m := taskBoxAt(line, i); m > 0 {
				i += m
			}
			continue
		}
		cb.WriteString(line[i:j])
		i = j
		break
	}
	return line[:i], cb.String(), line[i:]
}

// listMarkerAt 返回 line[j:] 处列表标记的长度（不含其后空格），不是标记返回 0。
func listMarkerAt(line string, j int) int {
	if j >= len(line) {
		return 0
	}
	switch line[j] {
	case '-', '+', '*':
		return 1
	}
	k := j
	for k < len(line) && k-j < 9 && isASCIIDigit(line[k]) {
		k++
	}
	if k > j && k < len(line) && (line[k] == '.' || line[k] == ')') {
		return k - j + 1
	}
	return 0
}

// taskBoxAt 识别任务框 "[ ] " / "[x] "，返回含其后空格的长度。
func taskBoxAt(line string, i int) int {
	if i+3 > len(line) || line[i] != '[' || line[i+2] != ']' {
		return 0
	}
	switch line[i+1] {
	case ' ', 'x', 'X':
	default:
		return 0
	}
	k := i + 3
	if k < len(line) && line[k] != ' ' && line[k] != '\t' {
		return 0
	}
	for k < len(line) && (line[k] == ' ' || line[k] == '\t') {
		k++
	}
	return k - i
}

// fenceOpens 判断是否为围栏代码块开头。
func fenceOpens(rest string) (byte, int, bool) {
	r := strings.TrimLeft(rest, " ")
	if len(r) < 3 || (r[0] != '`' && r[0] != '~') {
		return 0, 0, false
	}
	ch := r[0]
	n := 0
	for n < len(r) && r[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	if ch == '`' && strings.IndexByte(r[n:], '`') >= 0 {
		return 0, 0, false
	}
	return ch, n, true
}

func fenceCloses(line string, ch byte, n int) bool {
	r := strings.TrimLeft(line, " \t>")
	k := 0
	for k < len(r) && r[k] == ch {
		k++
	}
	return k >= n && strings.Trim(r[k:], " \t") == ""
}

func maxRun(s string, ch byte) int {
	best, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == ch {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}

// fenceFor 返回能包住内容的反引号围栏。
func fenceFor(content string) string {
	n := maxRun(content, '`') + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}

// tryMath 识别 $$ 公式块并改写为 ```math 围栏。
func (nz *normResult) tryMath(src []string, i *int, prefix, cont, rest string) bool {
	t := strings.Trim(rest, " \t")
	if !strings.HasPrefix(t, "$$") {
		return false
	}
	inner := t[2:]
	if len(inner) >= 2 && strings.HasSuffix(inner, "$$") {
		latex := strings.Trim(inner[:len(inner)-2], " \t")
		if latex == "" || strings.Contains(latex, "$$") {
			return false
		}
		f := fenceFor(latex)
		nz.emit(prefix+f+"math", false, -1, false)
		nz.emit(cont+latex, false, -1, false)
		nz.emit(cont+f, false, -1, false)
		return true
	}
	inner = strings.Trim(inner, " \t")
	if strings.Contains(inner, "$$") {
		return false
	}
	end := -1
	for k := *i + 1; k < len(src); k++ {
		tt := strings.Trim(strings.TrimLeft(src[k], " \t>"), " \t")
		if tt == "" {
			break
		}
		if strings.HasSuffix(tt, "$$") {
			end = k
			break
		}
	}
	if end < 0 {
		return false
	}
	var body []string
	if inner != "" {
		body = append(body, cont+inner)
	}
	body = append(body, src[*i+1:end]...)
	last := strings.TrimRight(src[end], " \t")
	last = strings.TrimRight(last[:len(last)-2], " \t")
	if !isBlankish(last) {
		body = append(body, last)
	}
	f := fenceFor(strings.Join(body, "\n"))
	nz.emit(prefix+f+"math", false, -1, false)
	for _, l := range body {
		nz.emit(l, false, -1, false)
	}
	nz.emit(cont+f, false, -1, false)
	*i = end
	return true
}

type rpiece struct {
	text string
	tag  int // -1 表示文字
}

// escapePrivate 把私用区占位字符转成实体（行内代码里的保持原样）。
func escapePrivate(s string) string {
	if !strings.ContainsFunc(s, isPrivateTok) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]) {
			b.WriteString(s[i : i+2])
			i += 2
			continue
		}
		if c == '`' {
			n := runLen(s, i, '`')
			if k := findBacktickClose(s, i+n, n); k >= 0 {
				b.WriteString(s[i : k+n])
				i = k + n
				continue
			}
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		if c >= 0x80 {
			r, size := utf8.DecodeRuneInString(s[i:])
			if isPrivateTok(r) {
				fmt.Fprintf(&b, "&#x%X;", r)
			} else {
				b.WriteString(s[i : i+size])
			}
			i += size
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func runLen(s string, i int, ch byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == ch {
		n++
	}
	return n
}

// findBacktickClose 找长度恰为 n 的反引号串，返回其起点。
func findBacktickClose(s string, from, n int) int {
	for k := from; k < len(s); {
		if s[k] == '`' {
			m := runLen(s, k, '`')
			if m == n {
				return k
			}
			k += m
			continue
		}
		k++
	}
	return -1
}

// scanPieces 把一行内容切成文字段与块级标签段（跳过行内代码与转义）。
func (nz *normResult) scanPieces(rest string) ([]rpiece, bool) {
	var pieces []rpiece
	var cur strings.Builder
	hasTag := false
	for i := 0; i < len(rest); {
		c := rest[i]
		switch {
		case c == '\\' && i+1 < len(rest) && rest[i+1] < 0x80 && isASCIIPunct(rest[i+1]):
			cur.WriteString(rest[i : i+2])
			i += 2
		case c == '`':
			n := runLen(rest, i, '`')
			if k := findBacktickClose(rest, i+n, n); k >= 0 {
				cur.WriteString(rest[i : k+n])
				i = k + n
			} else {
				cur.WriteString(rest[i : i+n])
				i += n
			}
		case c == '<':
			if t, end, ok := parseTagAt(rest, i); ok && blockTagNames[t.name] {
				pieces = append(pieces, rpiece{text: cur.String(), tag: -1})
				cur.Reset()
				tt := t
				nz.tags = append(nz.tags, &tt)
				pieces = append(pieces, rpiece{tag: len(nz.tags) - 1})
				hasTag = true
				i = end
			} else {
				cur.WriteByte(c)
				i++
			}
		case c >= 0x80:
			r, size := utf8.DecodeRuneInString(rest[i:])
			if isPrivateTok(r) {
				fmt.Fprintf(&cur, "&#x%X;", r)
			} else {
				cur.WriteString(rest[i : i+size])
			}
			i += size
		default:
			cur.WriteByte(c)
			i++
		}
	}
	pieces = append(pieces, rpiece{text: cur.String(), tag: -1})
	return pieces, hasTag
}

func isASCIIPunct(c byte) bool {
	return c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}

// withTrailer 把行尾尾注替换成占位符。
func (nz *normResult) withTrailer(s string) string {
	pos, attrs, ok := findTrailer(s)
	if !ok {
		return s
	}
	raw := strings.TrimRight(s[pos:], " \t")
	nz.trailers = append(nz.trailers, trailerTok{raw: raw, attrs: attrs})
	head := strings.TrimRight(s[:pos], " \t")
	if head == "" {
		return trailerToken(len(nz.trailers) - 1)
	}
	return head + " " + trailerToken(len(nz.trailers)-1)
}

func (nz *normResult) processLine(prefix, cont, rest string) {
	pieces, hasTag := nz.scanPieces(rest)
	if !hasTag {
		nz.emit(prefix+nz.withTrailer(pieces[0].text), false, -1, isWSOnly(prefix))
		return
	}
	blank := strings.TrimRight(cont, " \t")
	first := true
	for _, p := range pieces {
		pre := cont
		if first {
			pre = prefix
		}
		if p.tag >= 0 {
			if n := len(nz.lines); n > 0 && !isBlankish(nz.lines[n-1].text) {
				nz.emit(blank, true, -1, false)
			}
			nz.emit(pre+blockToken(p.tag), false, p.tag, isWSOnly(pre))
			nz.emit(blank, true, -1, false)
			first = false
			continue
		}
		t := strings.Trim(p.text, " \t")
		if t == "" {
			continue
		}
		nz.emit(pre+nz.withTrailer(t), false, -1, isWSOnly(pre))
		first = false
	}
	if first {
		// 只有空白：保留原前缀（例如空列表项）
		nz.emit(prefix, false, -1, isWSOnly(prefix))
	}
}

func indentOf(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// dedent 收拢大模型常见的「标签内部缩进」写法：容器标签独占一行时，若内部所有非空行都比标签行
// 缩进更深，去掉多出的公共缩进，避免被当成缩进代码块。
func (nz *normResult) dedent(lo, hi int) {
	i := lo
	for i < hi {
		ln := nz.lines[i]
		if ln.tag < 0 || !ln.wsPrefix {
			i++
			continue
		}
		t := nz.tags[ln.tag]
		if t.kind != tagOpen || !containerTag(t) {
			i++
			continue
		}
		depth, end := 0, -1
		for j := i + 1; j < hi; j++ {
			lj := nz.lines[j]
			if lj.tag < 0 {
				continue
			}
			tj := nz.tags[lj.tag]
			if tj.name != t.name {
				continue
			}
			if tj.kind == tagOpen {
				depth++
			} else if tj.kind == tagClose {
				if depth == 0 {
					end = j
					break
				}
				depth--
			}
		}
		if end < 0 {
			i++
			continue
		}
		base := indentOf(ln.text)
		m := -1
		for j := i + 1; j < end; j++ {
			if isWSOnly(nz.lines[j].text) {
				continue
			}
			d := indentOf(nz.lines[j].text)
			if m < 0 || d < m {
				m = d
			}
		}
		if m > base {
			k := m - base
			for j := i + 1; j < end; j++ {
				if isWSOnly(nz.lines[j].text) {
					continue
				}
				nz.lines[j].text = nz.lines[j].text[k:]
			}
		}
		nz.dedent(i+1, end)
		i = end + 1
	}
}
