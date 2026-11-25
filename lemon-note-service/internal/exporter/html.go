package exporter

import (
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
)

// htmlRenderer 把块渲染成自包含的 HTML（内联样式，不引用外部资源）。
type htmlRenderer struct {
	r        *run
	from     *enode
	b        strings.Builder
	headings int
	toc      []tocItem
}

type tocItem struct {
	level int
	id    string
	text  string
}

const pageCSS = `:root{color-scheme:light dark}
body{margin:0;background:#fff;color:#37352f;font:16px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif}
article{max-width:760px;margin:0 auto;padding:48px 24px 96px}
h1.title{font-size:2.1em;margin:0 0 .6em}
h1,h2,h3{line-height:1.3;margin:1.4em 0 .4em}
p{margin:.35em 0;min-height:1em}
a{color:#2f6fb3}
pre{background:#f6f6f4;border-radius:6px;padding:12px 14px;overflow:auto;font:13px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
code{background:#f2f1ee;border-radius:4px;padding:.1em .35em;font:.88em ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#c4554d}
pre code{background:none;padding:0;color:inherit}
blockquote{margin:.6em 0;padding:0 0 0 14px;border-left:3px solid currentColor}
table{border-collapse:collapse;margin:.8em 0;width:100%}
th,td{border:1px solid #e3e2de;padding:6px 9px;text-align:left;vertical-align:top}
th{background:#f7f6f3;font-weight:600}
img{max-width:100%;border-radius:4px}
figure{margin:.8em 0}
figcaption{color:#787774;font-size:.88em}
hr{border:0;border-top:1px solid #e3e2de;margin:1.2em 0}
ul.task{list-style:none;padding-left:4px}
ul.task li{display:flex;gap:8px;align-items:flex-start}
ul.task input{margin-top:.45em}
.callout{display:flex;gap:10px;padding:14px 16px;border-radius:6px;margin:.6em 0;background:#f1f1ef}
.callout .icon{flex:none;width:1.4em;text-align:center}
.callout .body{flex:1;min-width:0}
.columns{display:flex;gap:24px;flex-wrap:wrap}
.columns>div{flex:1 1 0;min-width:200px}
details{margin:.4em 0}
summary{cursor:pointer}
.bookmark{display:block;border:1px solid #e3e2de;border-radius:6px;padding:10px 14px;margin:.6em 0;text-decoration:none;color:inherit}
.bookmark small{display:block;color:#787774}
.muted{color:#9b9a97}
.props{margin:0 0 1.6em;border:0}
.props td{border:0;padding:3px 12px 3px 0}
.props td:first-child{color:#787774;width:160px}
.toc a{color:inherit}
@media (prefers-color-scheme:dark){body{background:#191919;color:#e6e6e4}pre,th{background:#252525}code{background:#2c2c2c}th,td,hr,.bookmark{border-color:#373737}.callout{background:#2a2a2a}}`

var textColors = map[string]string{"gray": "#787774", "brown": "#9f6b53", "orange": "#d9730d", "yellow": "#cb912f", "green": "#448361",
	"blue": "#337ea9", "purple": "#9065b0", "pink": "#c14c8a", "red": "#d44c47"}

var bgColors = map[string]string{"gray": "#f1f1ef", "brown": "#f4eeee", "orange": "#fbecdd", "yellow": "#fbf3db", "green": "#edf3ec",
	"blue": "#e7f3f8", "purple": "#f4f0f7", "pink": "#f9eef3", "red": "#fdebec"}

func esc(s string) string { return html.EscapeString(s) }

// safeHref 只放行 http(s)、mailto、tel 与相对地址。
func safeHref(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if u, err := url.Parse(h); err == nil && u.Scheme != "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "mailto", "tel":
			return h
		}
		return ""
	}
	return h
}

// colorStyle 块颜色：文字色或 *_background 背景色。
func colorStyle(c string) string {
	if c == "" || c == "default" {
		return ""
	}
	if strings.HasSuffix(c, "_background") {
		if v, ok := bgColors[strings.TrimSuffix(c, "_background")]; ok {
			return "background:" + v + ";padding:2px 6px;border-radius:4px;"
		}
		return ""
	}
	if v, ok := textColors[c]; ok {
		return "color:" + v + ";"
	}
	return ""
}

func attrString(n lnmd.Node, k string) string {
	switch v := n.Attrs[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (h *htmlRenderer) document(title string, props [][2]string, nodes []lnmd.Node) string {
	h.collectTOC(nodes)
	h.b.WriteString("<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	h.b.WriteString(esc(title))
	h.b.WriteString("</title>\n<style>")
	h.b.WriteString(pageCSS)
	h.b.WriteString("</style>\n</head>\n<body>\n<article>\n<h1 class=\"title\">" + esc(title) + "</h1>\n")
	if len(props) > 0 {
		h.b.WriteString("<table class=\"props\">")
		for _, p := range props {
			h.b.WriteString("<tr><td>" + esc(p[0]) + "</td><td>" + h.propValue(p[1]) + "</td></tr>")
		}
		h.b.WriteString("</table>\n")
	}
	h.headings = 0
	h.blocks(nodes)
	if h.from != nil && len(h.from.children) > 0 && h.from.db == nil && h.from.meta.Kind != "database" {
		// 正文里没有引用到的子页面列在最后，方便浏览
		seen := map[string]bool{}
		walk(nodes, func(n lnmd.Node) {
			if n.Type == "childPage" || n.Type == "childDatabase" {
				id, _ := n.Attrs["pageId"].(string)
				seen[id] = true
			}
		})
		for _, c := range h.from.children {
			if !seen[c.meta.ID] {
				h.pageRef(c.meta.ID)
			}
		}
	}
	h.b.WriteString("</article>\n</body>\n</html>\n")
	return h.b.String()
}

// propValue 属性值里的相对文件路径做成链接。
func (h *htmlRenderer) propValue(v string) string {
	if strings.Contains(v, "files/") {
		var parts []string
		for _, p := range strings.Split(v, ", ") {
			if strings.Contains(p, "files/") {
				parts = append(parts, "<a href=\""+esc(p)+"\">"+esc(p[strings.LastIndex(p, "/")+1:])+"</a>")
			} else {
				parts = append(parts, esc(p))
			}
		}
		return strings.Join(parts, ", ")
	}
	return esc(v)
}

func walk(ns []lnmd.Node, fn func(n lnmd.Node)) {
	for _, n := range ns {
		fn(n)
		walk(n.Content, fn)
	}
}

func (h *htmlRenderer) collectTOC(ns []lnmd.Node) {
	i := 0
	walk(ns, func(n lnmd.Node) {
		if n.Type == "heading" {
			i++
			lv, _ := n.Attrs["level"].(float64)
			h.toc = append(h.toc, tocItem{level: int(lv), id: "h-" + strconv.Itoa(i), text: lnmd.PlainText(n)})
		}
	})
}

func (h *htmlRenderer) blocks(ns []lnmd.Node) {
	for _, n := range ns {
		h.block(n)
	}
}

func (h *htmlRenderer) block(n lnmd.Node) {
	b := &h.b
	switch n.Type {
	case "paragraph":
		st := colorStyle(attrString(n, "color"))
		if a := attrString(n, "textAlign"); a == "center" || a == "right" {
			st += "text-align:" + a + ";"
		}
		b.WriteString(openTag("p", st))
		if len(n.Content) == 0 {
			b.WriteString("<br>")
		}
		h.inline(n.Content)
		b.WriteString("</p>\n")
	case "heading":
		lv := 1
		if v, ok := n.Attrs["level"].(float64); ok && v >= 1 && v <= 3 {
			lv = int(v)
		}
		h.headings++
		tag := "h" + strconv.Itoa(lv+1)
		b.WriteString("<" + tag + " id=\"h-" + strconv.Itoa(h.headings) + "\"")
		if st := colorStyle(attrString(n, "color")); st != "" {
			b.WriteString(" style=\"" + st + "\"")
		}
		b.WriteString(">")
		h.inline(n.Content)
		b.WriteString("</" + tag + ">\n")
	case "bulletList":
		b.WriteString("<ul>\n")
		h.items(n.Content)
		b.WriteString("</ul>\n")
	case "orderedList":
		start := attrString(n, "start")
		if start != "" && start != "1" {
			b.WriteString("<ol start=\"" + esc(start) + "\">\n")
		} else {
			b.WriteString("<ol>\n")
		}
		h.items(n.Content)
		b.WriteString("</ol>\n")
	case "taskList":
		b.WriteString("<ul class=\"task\">\n")
		for _, it := range n.Content {
			checked := ""
			if c, _ := it.Attrs["checked"].(bool); c {
				checked = " checked"
			}
			b.WriteString("<li><input type=\"checkbox\" disabled" + checked + "><div>")
			h.blocks(it.Content)
			b.WriteString("</div></li>\n")
		}
		b.WriteString("</ul>\n")
	case "listItem", "taskItem":
		b.WriteString("<ul>")
		h.items([]lnmd.Node{n})
		b.WriteString("</ul>\n")
	case "toggle":
		open := ""
		if o, _ := n.Attrs["open"].(bool); o {
			open = " open"
		}
		b.WriteString("<details" + open + "><summary>")
		for _, c := range n.Content {
			if c.Type == "toggleTitle" {
				lv, _ := n.Attrs["level"].(float64)
				if lv >= 1 && lv <= 3 {
					tag := "h" + strconv.Itoa(int(lv)+1)
					b.WriteString("<" + tag + " style=\"display:inline;margin:0\">")
					h.inline(c.Content)
					b.WriteString("</" + tag + ">")
				} else {
					h.inline(c.Content)
				}
			}
		}
		b.WriteString("</summary>\n<div style=\"padding-left:22px\">\n")
		for _, c := range n.Content {
			if c.Type == "toggleBody" {
				h.blocks(c.Content)
			}
		}
		b.WriteString("</div></details>\n")
	case "blockquote":
		b.WriteString("<blockquote>\n")
		h.blocks(n.Content)
		b.WriteString("</blockquote>\n")
	case "callout":
		color := strings.TrimSuffix(attrString(n, "color"), "_background")
		st := ""
		if v, ok := bgColors[color]; ok {
			st = "background:" + v
		}
		b.WriteString(openTag("div class=\"callout\"", st))
		icon := attrString(n, "icon")
		if icon != "" && !strings.HasPrefix(icon, "icon:") && !strings.HasPrefix(icon, "file:") && !strings.HasPrefix(icon, "http") {
			b.WriteString("<span class=\"icon\">" + esc(icon) + "</span>")
		}
		b.WriteString("<div class=\"body\">\n")
		h.blocks(n.Content)
		b.WriteString("</div></div>\n")
	case "codeBlock":
		lang := attrString(n, "language")
		b.WriteString("<pre><code")
		if lang != "" {
			b.WriteString(" class=\"language-" + esc(lang) + "\"")
		}
		b.WriteString(">" + esc(codeText(n)) + "</code></pre>\n")
	case "horizontalRule":
		b.WriteString("<hr>\n")
	case "image":
		src := h.fileSrc(attrString(n, "src"))
		if src == "" {
			b.WriteString("<p class=\"muted\">[" + esc(firstNonEmpty(attrString(n, "alt"), "image")) + "]</p>\n")
			return
		}
		st := ""
		if w := attrString(n, "width"); w != "" {
			if _, err := strconv.ParseFloat(w, 64); err == nil {
				st = "width:" + w + "px"
			}
		}
		align := attrString(n, "align")
		fst := ""
		if align == "center" || align == "right" {
			fst = "text-align:" + align
		}
		b.WriteString(openTag("figure", fst) + "<img src=\"" + esc(src) + "\" alt=\"" + esc(attrString(n, "alt")) + "\"")
		if st != "" {
			b.WriteString(" style=\"" + st + "\"")
		}
		b.WriteString(">")
		if t := attrString(n, "title"); t != "" {
			b.WriteString("<figcaption>" + esc(t) + "</figcaption>")
		}
		b.WriteString("</figure>\n")
	case "video", "audio":
		src := h.fileSrc(attrString(n, "src"))
		if src == "" {
			b.WriteString("<p class=\"muted\">[" + esc(firstNonEmpty(attrString(n, "title"), n.Type)) + "]</p>\n")
			return
		}
		b.WriteString("<" + n.Type + " controls src=\"" + esc(src) + "\" style=\"max-width:100%\"></" + n.Type + ">\n")
	case "fileBlock":
		name := firstNonEmpty(attrString(n, "name"), "file")
		src := h.fileSrc(attrString(n, "src"))
		if src == "" {
			b.WriteString("<p class=\"muted\">[" + esc(name) + "]</p>\n")
			return
		}
		b.WriteString("<p><a href=\"" + esc(src) + "\" download>" + esc(name) + "</a></p>\n")
	case "bookmark", "embed":
		u := safeHref(attrString(n, "url"))
		t := firstNonEmpty(attrString(n, "title"), attrString(n, "url"))
		if u == "" {
			b.WriteString("<p>" + esc(t) + "</p>\n")
			return
		}
		b.WriteString("<a class=\"bookmark\" href=\"" + esc(u) + "\">" + esc(t))
		if d := attrString(n, "description"); d != "" {
			b.WriteString("<small>" + esc(d) + "</small>")
		}
		if t != attrString(n, "url") {
			b.WriteString("<small>" + esc(attrString(n, "url")) + "</small>")
		}
		b.WriteString("</a>\n")
	case "table":
		b.WriteString("<table>\n")
		for _, row := range n.Content {
			b.WriteString("<tr>")
			for _, cell := range row.Content {
				tag := "td"
				if cell.Type == "tableHeader" {
					tag = "th"
				}
				b.WriteString("<" + tag)
				for _, k := range []string{"colspan", "rowspan"} {
					if v := attrString(cell, k); v != "" && v != "1" {
						b.WriteString(" " + k + "=\"" + esc(v) + "\"")
					}
				}
				b.WriteString(">")
				for i, p := range cell.Content {
					if i > 0 {
						b.WriteString("<br>")
					}
					h.inline(p.Content)
				}
				b.WriteString("</" + tag + ">")
			}
			b.WriteString("</tr>\n")
		}
		b.WriteString("</table>\n")
	case "columns":
		b.WriteString("<div class=\"columns\">\n")
		for _, c := range n.Content {
			st := ""
			if w, ok := c.Attrs["width"].(float64); ok && w > 0 {
				st = "flex:" + strconv.FormatFloat(w, 'f', 3, 64) + " 1 0"
			}
			b.WriteString(openTag("div", st))
			h.blocks(c.Content)
			b.WriteString("</div>\n")
		}
		b.WriteString("</div>\n")
	case "mathBlock":
		b.WriteString("<pre class=\"math\">" + esc(attrString(n, "latex")) + "</pre>\n")
	case "mermaid", "graphviz", "mindmap", "chart":
		b.WriteString("<pre>" + esc(lnmd.RenderBlock(n, h.r.acc.Render)) + "</pre>\n")
	case "whiteboard":
		b.WriteString("<p class=\"muted\">[白板：" + esc(firstNonEmpty(attrString(n, "title"), "未命名")) + "]</p>\n")
	case "childPage", "linkToPage", "childDatabase":
		h.pageRef(attrString(n, "pageId"))
	case "syncedBlock":
		if attrString(n, "sourceId") != "" {
			b.WriteString("<p class=\"muted\">[同步块]</p>\n")
			return
		}
		h.blocks(n.Content)
	case "tableOfContents":
		if len(h.toc) == 0 {
			return
		}
		b.WriteString("<nav class=\"toc\"><ul>\n")
		for _, it := range h.toc {
			b.WriteString("<li style=\"margin-left:" + strconv.Itoa((it.level-1)*18) + "px\"><a href=\"#" + it.id + "\">" + esc(it.text) + "</a></li>\n")
		}
		b.WriteString("</ul></nav>\n")
	case "breadcrumb":
	default:
		if t := strings.TrimSpace(lnmd.PlainText(n)); t != "" {
			b.WriteString("<p>" + esc(t) + "</p>\n")
		}
	}
}

func openTag(tag, style string) string {
	if style == "" {
		return "<" + tag + ">"
	}
	return "<" + tag + " style=\"" + esc(style) + "\">"
}

func codeText(n lnmd.Node) string {
	var b strings.Builder
	for _, c := range n.Content {
		if c.Type == "hardBreak" {
			b.WriteByte('\n')
		} else {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func (h *htmlRenderer) items(ns []lnmd.Node) {
	for _, it := range ns {
		h.b.WriteString("<li>")
		for i, c := range it.Content {
			if i == 0 && c.Type == "paragraph" {
				h.inline(c.Content)
				continue
			}
			h.block(c)
		}
		h.b.WriteString("</li>\n")
	}
}

// pageRef 子页面 / 页面链接：在导出包里就链过去；没有权限的不显示标题。
func (h *htmlRenderer) pageRef(id string) {
	t, ok := h.r.title(id)
	if !ok {
		h.b.WriteString("<p class=\"muted\">[无权访问的页面]</p>\n")
		return
	}
	t = firstNonEmpty(t, "Untitled")
	if link := h.r.pageLink(h.from, id); link != "" {
		h.b.WriteString("<p><a href=\"" + esc(link) + "\">" + esc(t) + "</a></p>\n")
		return
	}
	h.b.WriteString("<p class=\"muted\">" + esc(t) + "</p>\n")
}

func (h *htmlRenderer) fileSrc(src string) string {
	if id := fileIDOf(src); id != "" {
		link, _ := h.r.fileLink(h.from, id)
		return link
	}
	return safeHref(src)
}

func (h *htmlRenderer) inline(ns []lnmd.Node) {
	b := &h.b
	for _, n := range ns {
		switch n.Type {
		case "hardBreak":
			b.WriteString("<br>")
			continue
		case "mathInline":
			b.WriteString("<code>" + esc(attrString(n, "latex")) + "</code>")
			continue
		case "mention":
			switch attrString(n, "kind") {
			case "date":
				s := attrString(n, "date")
				if e := attrString(n, "endDate"); e != "" {
					s += " → " + e
				}
				b.WriteString("<span class=\"muted\">" + esc(s) + "</span>")
			case "user":
				name := "用户"
				if h.r.acc.Render.UserName != nil {
					if v, ok := h.r.acc.Render.UserName(attrString(n, "id")); ok {
						name = v
					}
				}
				b.WriteString("<span class=\"muted\">@" + esc(name) + "</span>")
			default:
				id := attrString(n, "id")
				t, ok := h.r.title(id)
				if !ok {
					b.WriteString("<span class=\"muted\">无权访问</span>")
				} else if link := h.r.pageLink(h.from, id); link != "" {
					b.WriteString("<a href=\"" + esc(link) + "\">" + esc(firstNonEmpty(t, "Untitled")) + "</a>")
				} else {
					b.WriteString("<span>" + esc(firstNonEmpty(t, "Untitled")) + "</span>")
				}
			}
			continue
		case "text":
		default:
			continue
		}
		var open, close []string
		for _, m := range n.Marks {
			var o, c string
			switch m.Type {
			case "bold":
				o, c = "<strong>", "</strong>"
			case "italic":
				o, c = "<em>", "</em>"
			case "underline":
				o, c = "<u>", "</u>"
			case "strike":
				o, c = "<s>", "</s>"
			case "code":
				o, c = "<code>", "</code>"
			case "subscript":
				o, c = "<sub>", "</sub>"
			case "superscript":
				o, c = "<sup>", "</sup>"
			case "textColor":
				col, _ := m.Attrs["color"].(string)
				if v, ok := textColors[col]; ok {
					o, c = "<span style=\"color:"+v+"\">", "</span>"
				}
			case "highlight":
				col, _ := m.Attrs["color"].(string)
				v, ok := bgColors[col]
				if !ok {
					v = bgColors["yellow"]
				}
				o, c = "<mark style=\"background:"+v+";color:inherit\">", "</mark>"
			case "link":
				href, _ := m.Attrs["href"].(string)
				if id := pageIDOf(href); id != "" {
					href = h.r.pageLink(h.from, id)
				} else if id := fileIDOf(href); id != "" {
					href, _ = h.r.fileLink(h.from, id)
				} else {
					href = safeHref(href)
				}
				if href != "" {
					o, c = "<a href=\""+esc(href)+"\">", "</a>"
				}
			}
			if o != "" {
				open = append(open, o)
				close = append([]string{c}, close...)
			}
		}
		b.WriteString(strings.Join(open, ""))
		b.WriteString(esc(n.Text))
		b.WriteString(strings.Join(close, ""))
	}
}
