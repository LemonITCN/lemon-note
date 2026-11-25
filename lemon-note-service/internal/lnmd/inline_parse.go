package lnmd

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/util"
)

// 行内转换分两步：先把 goldmark 行内 AST 摊平成带结构标记（粗体、斜体、删除线、链接、代码）的
// 序列，原始 HTML 作为事件保留；再按出现顺序配对 HTML 标签，叠加下划线、颜色等标记、生成提及。

var tokRe = regexp.MustCompile("([0-9]+)|([0-9]+)")

type inlineCtx struct {
	inTable bool
}

func cloneMarks(ms []Mark) []Mark {
	if len(ms) == 0 {
		return nil
	}
	out := make([]Mark, len(ms))
	copy(out, ms)
	return out
}

func withMark(ms []Mark, m Mark) []Mark {
	out := make([]Mark, 0, len(ms)+1)
	out = append(out, ms...)
	return append(out, m)
}

func textItem(s string, marks []Mark) Node {
	return Node{Type: tText, Text: s, Marks: cloneMarks(marks)}
}

// resolveText 处理反斜杠转义与 HTML 实体（单遍，与 CommonMark 一致）。
func resolveText(b []byte) string {
	if bytes.IndexByte(b, '\\') < 0 && bytes.IndexByte(b, '&') < 0 {
		return string(b)
	}
	var sb strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c == '\\' && i+1 < len(b) && util.IsPunct(b[i+1]) {
			sb.WriteByte(b[i+1])
			i += 2
			continue
		}
		if c == '&' {
			if r, n := matchEntity(b[i:]); n > 0 {
				sb.WriteString(r)
				i += n
				continue
			}
		}
		sb.WriteByte(c)
		i++
	}
	return sb.String()
}

// matchEntity 识别 b 开头的实体引用，返回解码结果与长度。
func matchEntity(b []byte) (string, int) {
	if len(b) < 3 || b[0] != '&' {
		return "", 0
	}
	if b[1] == '#' {
		i := 2
		hex := false
		if i < len(b) && (b[i] == 'x' || b[i] == 'X') {
			hex = true
			i++
		}
		s := i
		for i < len(b) && i-s < 8 && (isASCIIDigit(b[i]) || hex && util.IsHexDecimal(b[i])) {
			i++
		}
		if i == s || i >= len(b) || b[i] != ';' {
			return "", 0
		}
		if (!hex && i-s > 7) || (hex && i-s > 6) {
			return "", 0
		}
		base := 10
		if hex {
			base = 16
		}
		v, err := strconv.ParseInt(string(b[s:i]), base, 32)
		r := rune(v)
		if err != nil || v == 0 || !utf8.ValidRune(r) {
			r = '�'
		}
		return string(r), i + 1
	}
	i := 1
	for i < len(b) && i < 40 && isASCIIAlnum(b[i]) {
		i++
	}
	if i == 1 || i >= len(b) || b[i] != ';' || !isASCIILetter(b[1]) {
		return "", 0
	}
	ent, ok := util.LookUpHTML5EntityByName(string(b[1:i]))
	if !ok {
		return "", 0
	}
	return string(ent.Characters), i + 1
}

// emitText 输出一段文字，切出其中的占位符。
func (c *conv) emitText(raw []byte, isRaw bool, marks []Mark, out *[]Node) {
	if !bytes.ContainsRune(raw, tokBlkOpen) && !bytes.ContainsRune(raw, tokTrlOpen) {
		s := string(raw)
		if !isRaw {
			s = resolveText(raw)
		}
		if s != "" {
			*out = append(*out, textItem(s, marks))
		}
		return
	}
	prev := 0
	for _, loc := range tokRe.FindAllSubmatchIndex(raw, -1) {
		if loc[0] > prev {
			*out = append(*out, textItem(resolveText(raw[prev:loc[0]]), marks))
		}
		if loc[2] >= 0 {
			idx, _ := strconv.Atoi(string(raw[loc[2]:loc[3]]))
			*out = append(*out, Node{Type: pBlkTok, Attrs: map[string]any{"i": idx}, Marks: cloneMarks(marks)})
		} else {
			idx, _ := strconv.Atoi(string(raw[loc[4]:loc[5]]))
			*out = append(*out, Node{Type: pTrlTok, Attrs: map[string]any{"i": idx}, Marks: cloneMarks(marks)})
		}
		prev = loc[1]
	}
	if prev < len(raw) {
		*out = append(*out, textItem(resolveText(raw[prev:]), marks))
	}
}

// walkInline 把 goldmark 行内 AST 摊平。
func (c *conv) walkInline(n ast.Node, marks []Mark, ctx inlineCtx, out *[]Node) {
	for ch := n.FirstChild(); ch != nil; ch = ch.NextSibling() {
		switch v := ch.(type) {
		case *ast.Text:
			c.emitText(v.Segment.Value(c.src), v.IsRaw(), marks, out)
			if v.SoftLineBreak() || v.HardLineBreak() {
				*out = append(*out, Node{Type: tHardBreak, Marks: cloneMarks(marks)})
			}
		case *ast.String:
			s := string(v.Value)
			if !v.IsRaw() && !v.IsCode() {
				s = resolveText(v.Value)
			}
			if s != "" {
				*out = append(*out, textItem(s, marks))
			}
		case *ast.CodeSpan:
			var b []byte
			for t := v.FirstChild(); t != nil; t = t.NextSibling() {
				switch tt := t.(type) {
				case *ast.Text:
					b = append(b, tt.Segment.Value(c.src)...)
				case *ast.String:
					b = append(b, tt.Value...)
				}
			}
			s := strings.ReplaceAll(string(b), "\n", " ")
			if s != "" {
				*out = append(*out, textItem(s, withMark(marks, Mark{Type: mCode})))
			}
		case *ast.Emphasis:
			t := mItalic
			if v.Level >= 2 {
				t = mBold
			}
			c.walkInline(v, withMark(marks, Mark{Type: t}), ctx, out)
		case *ast.Link:
			c.walkInline(v, withMark(marks, linkMark(resolveText(v.Destination), resolveText(v.Title))), ctx, out)
		case *ast.AutoLink:
			url := string(v.URL(c.src))
			label := string(v.Label(c.src))
			if v.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(url), "mailto:") {
				url = "mailto:" + url
			}
			*out = append(*out, textItem(label, withMark(marks, linkMark(url, ""))))
		case *ast.Image:
			img := Node{Type: pImage, Attrs: map[string]any{"src": resolveText(v.Destination)}}
			if alt := c.plainInline(v); alt != "" {
				img.Attrs["alt"] = alt
			}
			if title := resolveText(v.Title); title != "" {
				img.Attrs["title"] = title
			}
			img.Marks = cloneMarks(marks)
			*out = append(*out, img)
		case *ast.RawHTML:
			var b []byte
			for i := 0; i < v.Segments.Len(); i++ {
				seg := v.Segments.At(i)
				b = append(b, seg.Value(c.src)...)
			}
			*out = append(*out, Node{Type: pHTML, Text: string(b), Marks: cloneMarks(marks)})
		case *extast.Strikethrough:
			c.walkInline(v, withMark(marks, Mark{Type: mStrike}), ctx, out)
		case *extast.TaskCheckBox:
			*out = append(*out, Node{Type: pCheck, Attrs: map[string]any{"checked": v.IsChecked}})
		case *mathNode:
			latex := v.Latex
			if ctx.inTable {
				latex = strings.ReplaceAll(latex, `\|`, "|")
			}
			*out = append(*out, Node{Type: tMathInline, Attrs: map[string]any{"latex": latex}, Marks: cloneMarks(marks)})
		default:
			if ch.HasChildren() {
				c.walkInline(ch, marks, ctx, out)
			}
		}
	}
}

func linkMark(href, title string) Mark {
	m := Mark{Type: mLink, Attrs: map[string]any{"href": href}}
	if title != "" {
		m.Attrs["title"] = title
	}
	return m
}

// plainInline 取行内子树的纯文本（图片说明用）。
func (c *conv) plainInline(n ast.Node) string {
	var items []Node
	c.walkInline(n, nil, inlineCtx{}, &items)
	var b strings.Builder
	for _, it := range items {
		switch it.Type {
		case tText:
			b.WriteString(it.Text)
		case tHardBreak:
			b.WriteByte(' ')
		case pHTML:
			b.WriteString(it.Text)
		case tMathInline:
			b.WriteString(attrStr(it, "latex"))
		case pImage:
			b.WriteString(attrStr(it, "alt"))
		}
	}
	return b.String()
}

// inlineMarkTags 把行内 HTML 标签映射到标记分组。
var inlineMarkTags = map[string]string{
	"u": mUnderline, "ins": mUnderline,
	"s": mStrike, "del": mStrike, "strike": mStrike,
	"b": mBold, "strong": mBold,
	"i": mItalic, "em": mItalic,
	"code": mCode,
	"sub":  mSubscript, "sup": mSuperscript,
	"mark": mHighlight,
	"span": "span", "font": "font", "a": "a",
}

var styleColorRe = regexp.MustCompile(`(?i)(?:^|;)\s*color\s*:\s*([^;]+)`)

func markFromTag(group string, t tagTok) *Mark {
	switch group {
	case mHighlight:
		m := Mark{Type: mHighlight}
		if c := strings.TrimSpace(t.get("color")); c != "" {
			m.Attrs = map[string]any{"color": c}
		}
		return &m
	case "span", "font":
		col := strings.TrimSpace(t.get("color"))
		if col == "" {
			if mm := styleColorRe.FindStringSubmatch(t.get("style")); mm != nil {
				col = strings.TrimSpace(mm[1])
			}
		}
		if col == "" {
			return nil
		}
		return &Mark{Type: mTextColor, Attrs: map[string]any{"color": col}}
	case "a":
		href := t.get("href")
		if href == "" {
			return nil
		}
		m := linkMark(href, t.get("title"))
		return &m
	default:
		return &Mark{Type: group}
	}
}

func parseWholeTag(raw string) (tagTok, bool) {
	t, end, ok := parseTagAt(raw, 0)
	if !ok || end != len(raw) {
		return t, false
	}
	return t, true
}

func mentionFromTag(t tagTok) Node {
	n := Node{Type: tMention, Attrs: map[string]any{}}
	switch t.name {
	case "mention-user":
		n.Attrs["kind"] = "user"
		n.Attrs["id"] = t.get("id")
	case "mention-date":
		n.Attrs["kind"] = "date"
		n.Attrs["date"] = t.get("date")
		if e := t.get("end"); e != "" {
			n.Attrs["endDate"] = e
		}
	default:
		n.Attrs["kind"] = "page"
		n.Attrs["id"] = t.get("id")
	}
	return n
}

type activeTag struct {
	group string
	mark  *Mark
}

// pairHTML 配对行内 HTML 标签：认识的标签变成标记、换行或提及，不认识的按原文保留。
func pairHTML(items []Node) []Node {
	var act []activeTag
	extra := func(base []Mark) []Mark {
		if len(act) == 0 {
			return cloneMarks(base)
		}
		out := cloneMarks(base)
		for _, a := range act {
			if a.mark != nil {
				out = append(out, *a.mark)
			}
		}
		return out
	}
	out := make([]Node, 0, len(items))
	for i := 0; i < len(items); i++ {
		it := items[i]
		if it.Type != pHTML {
			switch it.Type {
			case pBlkTok, pTrlTok, pCheck:
			default:
				it.Marks = extra(it.Marks)
			}
			out = append(out, it)
			continue
		}
		t, ok := parseWholeTag(it.Text)
		if !ok {
			out = append(out, Node{Type: tText, Text: it.Text, Marks: extra(it.Marks)})
			continue
		}
		switch t.name {
		case "br":
			if t.kind != tagClose {
				out = append(out, Node{Type: tHardBreak, Marks: extra(it.Marks)})
			}
			continue
		case "mention-page", "mention-user", "mention-date":
			if t.kind == tagClose {
				continue
			}
			m := mentionFromTag(t)
			m.Marks = extra(it.Marks)
			if t.kind == tagOpen {
				for j := i + 1; j < len(items); j++ {
					if items[j].Type != pHTML {
						continue
					}
					tj, ok := parseWholeTag(items[j].Text)
					if !ok || tj.name != t.name {
						continue
					}
					if tj.kind == tagClose {
						i = j
					}
					break
				}
			}
			out = append(out, m)
			continue
		}
		g, known := inlineMarkTags[t.name]
		if !known {
			out = append(out, Node{Type: tText, Text: it.Text, Marks: extra(it.Marks)})
			continue
		}
		switch t.kind {
		case tagSelf:
		case tagClose:
			for k := len(act) - 1; k >= 0; k-- {
				if act[k].group == g {
					act = append(act[:k], act[k+1:]...)
					break
				}
			}
		default:
			act = append(act, activeTag{group: g, mark: markFromTag(g, t)})
		}
	}
	return out
}
