package importer

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
)

// lnimport: 是导入时内部使用的伪协议，后面跟导入包里的路径（已转义），转换正文时换成附件。
const importScheme = "lnimport:"

func importRef(p string) string { return importScheme + escapePath(p) }

// escapePath 按路径段转义（空格、括号等都转义，Markdown 里可以直接用作链接地址）。
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = strings.NewReplacer("(", "%28", ")", "%29").Replace(url.PathEscape(s))
	}
	return strings.Join(segs, "/")
}

// linkCtx 是正文里链接与附件的解析上下文。
type linkCtx struct {
	b   *builder
	v   *vfs
	cur *pnode
	dir string // 当前文件所在目录（导入包里的路径）
	// pageAt 按导入包里的路径找页面
	pageAt func(p string) *pnode
	// pageByURL 按绝对地址找页面（例如 notion.so 链接），可为 nil
	pageByURL func(u *url.URL) *pnode
	// fileByName 按文件名在整个导入包里找附件（Obsidian 的最短路径写法），可为 nil
	fileByName func(name string) *vfile
	err        error
}

// rewrite 改写解析出的块：页面链接改成站内链接或提及、附件上传后改成 lemon://file/<id>。
func (c *linkCtx) rewrite(ns []lnmd.Node) ([]lnmd.Node, error) {
	out := c.blocks(ns)
	return out, c.err
}

func (c *linkCtx) blocks(ns []lnmd.Node) []lnmd.Node {
	out := make([]lnmd.Node, 0, len(ns))
	for _, n := range ns {
		out = append(out, c.block(n)...)
	}
	return out
}

func isInline(t string) bool {
	switch t {
	case "text", "hardBreak", "mention", "mathInline":
		return true
	}
	return false
}

func (c *linkCtx) block(n lnmd.Node) []lnmd.Node {
	if c.err != nil {
		return []lnmd.Node{n}
	}
	switch n.Type {
	case "paragraph":
		if href, text, ok := soleLink(n); ok {
			if t := c.resolvePage(href); t != nil {
				return []lnmd.Node{c.pageBlock(t)}
			}
			if f := c.resolveFile(href); f != nil {
				if m, err := c.b.uploadVFS(c.v, f, c.cur.id); err != nil {
					c.err = err
				} else if m != nil {
					return []lnmd.Node{fileNode(m, text)}
				}
			}
		}
	case "image":
		return []lnmd.Node{c.media(n, "alt")}
	case "fileBlock", "video", "audio":
		return []lnmd.Node{c.media(n, "name")}
	}
	if len(n.Content) == 0 {
		return []lnmd.Node{n}
	}
	content := make([]lnmd.Node, 0, len(n.Content))
	for _, ch := range n.Content {
		if isInline(ch.Type) {
			content = append(content, c.inline(ch))
		} else {
			content = append(content, c.block(ch)...)
		}
	}
	n.Content = content
	return []lnmd.Node{n}
}

// soleLink 判断段落是不是只有一个链接（整段文字都在同一个链接里）。
func soleLink(n lnmd.Node) (href, text string, ok bool) {
	if len(n.Content) == 0 {
		return "", "", false
	}
	var b strings.Builder
	for _, ch := range n.Content {
		if ch.Type != "text" {
			return "", "", false
		}
		h := linkHref(ch)
		if h == "" {
			if strings.TrimSpace(ch.Text) == "" {
				continue
			}
			return "", "", false
		}
		if href != "" && h != href {
			return "", "", false
		}
		href = h
		b.WriteString(ch.Text)
	}
	return href, strings.TrimSpace(b.String()), href != ""
}

func linkHref(n lnmd.Node) string {
	for _, m := range n.Marks {
		if m.Type == "link" {
			h, _ := m.Attrs["href"].(string)
			return h
		}
	}
	return ""
}

// pageBlock 生成指向页面的块：当前页面的子页面用子页面块，其余用页面链接块。
func (c *linkCtx) pageBlock(t *pnode) lnmd.Node {
	if t.parent == c.cur && t.kind != nRow {
		if t.kind == nDB {
			return lnmd.Node{Type: "childDatabase", Attrs: map[string]any{"pageId": t.id}}
		}
		return lnmd.Node{Type: "childPage", Attrs: map[string]any{"pageId": t.id}}
	}
	return lnmd.Node{Type: "linkToPage", Attrs: map[string]any{"pageId": t.id}}
}

func mentionNode(id string) lnmd.Node {
	return lnmd.Node{Type: "mention", Attrs: map[string]any{"kind": "page", "id": id}}
}

func (c *linkCtx) inline(n lnmd.Node) lnmd.Node {
	if n.Type != "text" {
		return n
	}
	href := linkHref(n)
	if href == "" || c.err != nil {
		return n
	}
	if t := c.resolvePage(href); t != nil {
		if sameTitle(n.Text, t.title) || looksLikePath(n.Text) {
			return mentionNode(t.id)
		}
		setHref(&n, "/p/"+t.id)
		return n
	}
	if f := c.resolveFile(href); f != nil {
		m, err := c.b.uploadVFS(c.v, f, c.cur.id)
		if err != nil {
			c.err = err
			return n
		}
		if m != nil {
			setHref(&n, "/api/files/"+m.ID)
			return n
		}
		dropLink(&n)
		return n
	}
	if isLocalRef(href) {
		// 指向导入包里不存在的文件：去掉链接保留文字
		dropLink(&n)
	}
	return n
}

func setHref(n *lnmd.Node, href string) {
	for i, m := range n.Marks {
		if m.Type == "link" {
			n.Marks[i] = lnmd.Mark{Type: "link", Attrs: map[string]any{"href": href}}
		}
	}
}

func dropLink(n *lnmd.Node) {
	var ms []lnmd.Mark
	for _, m := range n.Marks {
		if m.Type != "link" {
			ms = append(ms, m)
		}
	}
	n.Marks = ms
}

// media 处理图片 / 附件块的地址。
func (c *linkCtx) media(n lnmd.Node, labelKey string) lnmd.Node {
	src, _ := n.Attrs["src"].(string)
	if src == "" || strings.HasPrefix(src, "lemon://") {
		return n
	}
	f := c.resolveFile(src)
	if f == nil {
		if isLocalRef(src) {
			name := path.Base(decodeRef(src))
			c.b.warn("file_missing", c.cur.src, name, "引用的附件在导入包里找不到")
			label, _ := n.Attrs[labelKey].(string)
			if label == "" {
				label = name
			}
			return lnmd.Node{Type: "paragraph", Content: []lnmd.Node{{Type: "text", Text: "[" + label + "]"}}}
		}
		return n
	}
	m, err := c.b.uploadVFS(c.v, f, c.cur.id)
	if err != nil {
		c.err = err
		return n
	}
	if m == nil {
		label, _ := n.Attrs[labelKey].(string)
		if label == "" {
			label = path.Base(f.path)
		}
		return lnmd.Node{Type: "paragraph", Content: []lnmd.Node{{Type: "text", Text: "[" + label + "]"}}}
	}
	if n.Type == "image" && strings.HasPrefix(m.Mime, "image/") {
		attrs := map[string]any{}
		for k, v := range n.Attrs {
			attrs[k] = v
		}
		attrs["src"] = "lemon://file/" + m.ID
		n.Attrs = attrs
		return n
	}
	alt, _ := n.Attrs["alt"].(string)
	out := fileNode(m, alt)
	if w, ok := n.Attrs["width"]; ok && out.Type == "image" {
		out.Attrs["width"] = w
	}
	return out
}

// isLocalRef 判断是不是相对路径（不是网址、锚点、邮件等）。
func isLocalRef(href string) bool {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "/") {
		return false
	}
	if strings.HasPrefix(href, importScheme) {
		return true
	}
	if u, err := url.Parse(href); err == nil && u.Scheme != "" {
		return false
	}
	if i := strings.IndexByte(href, ':'); i > 0 && !strings.ContainsAny(href[:i], "/.") {
		return false // mailto:、tel: 等
	}
	return true
}

// decodeRef 把链接地址转成导入包里的相对路径（去掉锚点与查询串、解码）。
func decodeRef(href string) string {
	h := strings.TrimPrefix(href, importScheme)
	if i := strings.IndexAny(h, "#?"); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimSpace(strings.Trim(h, "<>"))
	if d, err := url.PathUnescape(h); err == nil {
		h = d
	}
	return h
}

// refPath 把相对地址解析成导入包里的路径；lnimport: 开头的地址相对导入包根目录。
func (c *linkCtx) refPath(href string) (string, bool) {
	if !isLocalRef(href) {
		return "", false
	}
	rel := decodeRef(href)
	if rel == "" {
		return "", false
	}
	var p string
	if strings.HasPrefix(href, importScheme) {
		p = path.Clean(rel)
	} else {
		p = path.Clean(path.Join(c.dir, rel))
	}
	if strings.HasPrefix(p, "../") || p == ".." || p == "." {
		return "", false
	}
	return p, true
}

func (c *linkCtx) resolvePage(href string) *pnode {
	if p, ok := c.refPath(href); ok {
		if c.pageAt != nil {
			if t := c.pageAt(p); t != nil {
				return t
			}
		}
		return nil
	}
	if c.pageByURL != nil {
		if u, err := url.Parse(href); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			return c.pageByURL(u)
		}
	}
	return nil
}

func (c *linkCtx) resolveFile(href string) *vfile {
	p, ok := c.refPath(href)
	if !ok {
		return nil
	}
	if f := c.v.get(p); f != nil && !isPageFile(f.path) {
		return f
	}
	if c.fileByName != nil {
		if f := c.fileByName(decodeRef(href)); f != nil && !isPageFile(f.path) {
			return f
		}
	}
	return nil
}

func isPageFile(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".csv", ".canvas":
		return true
	}
	return false
}

// sameTitle 判断链接文字是不是就是目标页面的标题。
func sameTitle(text, title string) bool {
	a := normTitle(strings.TrimPrefix(strings.TrimSpace(text), "@"))
	b := normTitle(title)
	return a != "" && a == b
}

// normTitle 只保留字母数字并转小写，用于标题与文件名的宽松比较。
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func looksLikePath(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasSuffix(strings.ToLower(s), ".md") || strings.Contains(s, "%20")
}

// ---------- 通用 Markdown 预处理 ----------

var h1Re = regexp.MustCompile(`^#\s+(.+?)\s*#*\s*$`)

// splitTitle 取出开头的一级标题（作为页面标题），返回标题与剩下的正文。
func splitTitle(md string) (string, string) {
	s := strings.TrimLeft(md, "\n\r\t ")
	line, rest, _ := strings.Cut(s, "\n")
	if m := h1Re.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
		return strings.TrimSpace(m[1]), rest
	}
	return "", md
}

// stripExt 去掉扩展名。
func stripExt(name string) string {
	return strings.TrimSuffix(name, path.Ext(name))
}

// mdParse 解析增强 Markdown；解析器本身不会失败，这里统一收口。
func mdParse(md string) []lnmd.Node {
	ns, err := lnmd.Parse(md)
	if err != nil {
		return []lnmd.Node{{Type: "paragraph", Content: []lnmd.Node{{Type: "text", Text: md}}}}
	}
	return ns
}

// attrEsc 转义自定义标签的属性值。
func attrEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;", "\n", " ").Replace(s)
}
