package lnmd

import (
	"strings"
)

// PlainText 返回节点的纯文本（全文索引用）。块与块之间换行，表格单元格之间用制表符。
func PlainText(n Node) string {
	var b strings.Builder
	plainInto(&b, n)
	return strings.TrimRight(b.String(), "\n")
}

func plainInto(b *strings.Builder, n Node) {
	switch n.Type {
	case tText:
		b.WriteString(n.Text)
		return
	case tHardBreak:
		b.WriteByte('\n')
		return
	case tMention:
		switch attrStr(n, "kind") {
		case "date":
			b.WriteString(attrStr(n, "date"))
			if e := attrStr(n, "endDate"); e != "" {
				b.WriteString(" ~ " + e)
			}
		default:
			b.WriteString(attrStr(n, "title"))
		}
		return
	case tMathInline:
		b.WriteString(attrStr(n, "latex"))
		return
	case tCodeBlock:
		line(b, codeText(n))
		return
	case tMathBlock:
		line(b, attrStr(n, "latex"))
		return
	case tMermaid, tGraphviz:
		line(b, attrStr(n, "code"))
		return
	case tMindmap:
		var parts []string
		var walk func(v any)
		walk = func(v any) {
			if t := mindmapText(v); t != "" {
				parts = append(parts, t)
			}
			for _, c := range mindmapChildren(v) {
				walk(c)
			}
		}
		walk(mindmapRoot(n.Attrs["data"]))
		line(b, strings.Join(parts, "\n"))
		return
	case tChart:
		if m, ok := n.Attrs["data"].(map[string]any); ok {
			line(b, scalarString(m["title"]))
		}
		return
	case tWhiteboard:
		parts := []string{attrStr(n, "title")}
		if m, ok := n.Attrs["data"].(map[string]any); ok {
			if els, ok := m["elements"].([]any); ok {
				for _, e := range els {
					if em, ok := e.(map[string]any); ok {
						if t := scalarString(em["text"]); t != "" {
							parts = append(parts, t)
						}
					}
				}
			}
		}
		line(b, strings.Join(nonEmpty(parts), "\n"))
		return
	case tImage:
		line(b, strings.Join(nonEmpty([]string{attrStr(n, "alt"), attrStr(n, "title")}), " "))
		return
	case tFileBlock:
		line(b, attrStr(n, "name"))
		return
	case tVideo, tAudio:
		line(b, attrStr(n, "title"))
		return
	case tBookmark:
		line(b, strings.Join(nonEmpty([]string{attrStr(n, "title"), attrStr(n, "description"), attrStr(n, "url")}), " "))
		return
	case tEmbed:
		line(b, attrStr(n, "url"))
		return
	case tChildPage, tChildDatabase, tLinkToPage:
		line(b, attrStr(n, "title"))
		return
	case tTable:
		for _, row := range n.Content {
			var cells []string
			for _, cell := range row.Content {
				cells = append(cells, strings.ReplaceAll(PlainText(cell), "\n", " "))
			}
			line(b, strings.Join(cells, "\t"))
		}
		return
	case tParagraph, tHeading, tToggleTitle:
		for _, c := range n.Content {
			plainInto(b, c)
		}
		b.WriteByte('\n')
		return
	}
	if n.Text != "" {
		b.WriteString(n.Text)
	}
	for _, c := range n.Content {
		plainInto(b, c)
		if s := b.String(); len(s) > 0 && !strings.HasSuffix(s, "\n") && isBlockType(c.Type) {
			b.WriteByte('\n')
		}
	}
}

func isBlockType(t string) bool {
	switch t {
	case tText, tHardBreak, tMention, tMathInline:
		return false
	}
	return true
}

func line(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	b.WriteString(s)
	b.WriteByte('\n')
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
