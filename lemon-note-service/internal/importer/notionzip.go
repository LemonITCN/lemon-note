package importer

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Notion「Markdown & CSV」导出包：
//
//	标题 <32 位 hex>.md          页面
//	标题 <32 位 hex>/            它的子页面、附件
//	库名 <32 位 hex>.csv         数据库（当前视图的行）
//	库名 <32 位 hex>_all.csv     数据库（全部行，优先用它）
//	库名 <32 位 hex>/行 <hex>.md 数据库行的正文
//
// 大导出会分成多个 zip 再打进一个 zip，这里统一展开。

var (
	stemHashRe = regexp.MustCompile(`^(.*?)\s+([0-9a-fA-F]{32})$`)
	anyHashRe  = regexp.MustCompile(`[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}`)
)

// splitStem 把「标题 hash」拆成标题与 hash。
func splitStem(stem string) (string, string) {
	if m := stemHashRe.FindStringSubmatch(stem); m != nil {
		return strings.TrimSpace(m[1]), strings.ToLower(m[2])
	}
	return stem, ""
}

// hashIn 找出字符串里的 Notion id（32 位 hex，可带横线）。
func hashIn(s string) string {
	all := anyHashRe.FindAllString(s, -1)
	if len(all) == 0 {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(all[len(all)-1], "-", ""))
}

type notionPlan struct {
	b      *builder
	v      *vfs
	byPath map[string]*pnode // 导入包路径（.md / .csv）→ 节点
	owner  map[string]*pnode // 目录 → 拥有它的节点
	byHash map[string]*pnode
	csvOf  map[*pnode]string
	roots  []*pnode
}

func (s *Service) notionZipWork(v *vfs) work {
	return func(ctx context.Context, b *builder) error {
		roots, err := planNotion(ctx, b, v)
		if err != nil {
			return err
		}
		return b.build(roots)
	}
}

func planNotion(ctx context.Context, b *builder, v *vfs) ([]*pnode, error) {
	// 顶层只有一个目录（例如 Export-xxx、Private & Shared）时剥掉它
	for r := v.singleRoot(); r != ""; r = v.singleRoot() {
		v.stripRoot(r)
	}
	pl := &notionPlan{b: b, v: v, byPath: map[string]*pnode{}, owner: map[string]*pnode{}, byHash: map[string]*pnode{}, csvOf: map[*pnode]string{}}
	paths := v.paths()
	// 数据库
	dbByKey := map[string]*pnode{}
	for _, p := range paths {
		if !strings.EqualFold(path.Ext(p), ".csv") {
			continue
		}
		stem := stripExt(p)
		isAll := strings.HasSuffix(stem, "_all")
		key := strings.TrimSuffix(stem, "_all")
		n := dbByKey[key]
		if n == nil {
			title, h := splitStem(path.Base(key))
			n = newNode(nDB, title, p)
			dbByKey[key] = n
			pl.owner[key] = n
			if h != "" {
				pl.byHash[h] = n
			}
		}
		if isAll || pl.csvOf[n] == "" {
			pl.csvOf[n] = p
			n.src = p
		}
		pl.byPath[p] = n
	}
	// 页面
	for _, p := range paths {
		if !isMarkdown(p) {
			continue
		}
		stem := stripExt(p)
		title, h := splitStem(path.Base(stem))
		n := newNode(nPage, title, p)
		pl.byPath[p] = n
		if pl.owner[stem] == nil {
			pl.owner[stem] = n
		}
		if h != "" && pl.byHash[h] == nil {
			pl.byHash[h] = n
		}
		n.prepare = pl.preparePage(p, nil)
	}
	if len(pl.byPath) == 0 {
		return nil, fail("压缩包里没有找到 Notion 导出的页面（.md）或数据库（.csv）")
	}
	// 挂上级：文件所在目录的拥有者；目录没有对应页面时建一个目录页面
	var keys []string
	for p := range pl.byPath {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	attached := map[*pnode]bool{}
	for _, p := range keys {
		n := pl.byPath[p]
		if attached[n] {
			continue
		}
		attached[n] = true
		pl.attach(n, path.Dir(p))
	}
	// 数据库：目录里的 .md 是行
	for _, p := range keys {
		db := pl.byPath[p]
		if db.kind != nDB || pl.csvOf[db] != p {
			continue
		}
		for _, c := range db.children {
			if c.kind == nPage {
				c.kind = nRow
			}
		}
	}
	for _, p := range keys {
		db := pl.byPath[p]
		if db.kind != nDB || pl.csvOf[db] != p {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := pl.planDB(db, p); err != nil {
			return nil, err
		}
	}
	return pl.roots, nil
}

func isMarkdown(p string) bool {
	e := strings.ToLower(path.Ext(p))
	return e == ".md" || e == ".markdown"
}

// attach 把节点挂到目录 dir 的拥有者下（没有就建目录页面）。
func (pl *notionPlan) attach(n *pnode, dir string) {
	if dir == "." || dir == "" {
		pl.roots = append(pl.roots, n)
		return
	}
	parent := pl.owner[dir]
	if parent == nil {
		title, h := splitStem(path.Base(dir))
		parent = newNode(nPage, title, dir+"/")
		pl.owner[dir] = parent
		if h != "" && pl.byHash[h] == nil {
			pl.byHash[h] = parent
		}
		pl.attach(parent, path.Dir(dir))
	}
	if parent == n {
		pl.roots = append(pl.roots, n)
		return
	}
	parent.add(n)
}

// pageAt 按导入包路径找页面（容忍大小写、缺扩展名，最后按 hash 找）。
func (pl *notionPlan) pageAt(p string) *pnode {
	if n := pl.byPath[p]; n != nil {
		return n
	}
	if f := pl.v.get(p); f != nil {
		if n := pl.byPath[f.path]; n != nil {
			return n
		}
	}
	if path.Ext(p) == "" {
		if n := pl.byPath[p+".md"]; n != nil {
			return n
		}
	}
	if isMarkdown(p) || strings.EqualFold(path.Ext(p), ".csv") {
		if h := hashIn(path.Base(p)); h != "" {
			return pl.byHash[h]
		}
	}
	return nil
}

// pageByURL 识别 notion.so / notion.site 链接。
func (pl *notionPlan) pageByURL(u *url.URL) *pnode {
	h := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(h, "notion.so") && !strings.HasSuffix(h, "notion.site") {
		return nil
	}
	if id := hashIn(u.Path); id != "" {
		return pl.byHash[id]
	}
	return nil
}

func (pl *notionPlan) linkCtx(n *pnode, file string) *linkCtx {
	return &linkCtx{b: pl.b, v: pl.v, cur: n, dir: path.Dir(file), pageAt: pl.pageAt, pageByURL: pl.pageByURL}
}

// preparePage 读 .md 生成正文。rowKeys 非空时去掉开头的「属性: 值」行（数据库行的属性在 CSV 里）。
func (pl *notionPlan) preparePage(p string, rowKeys map[string]bool) func(ctx context.Context, b *builder, n *pnode) error {
	return func(ctx context.Context, b *builder, n *pnode) error {
		f := pl.v.get(p)
		if f == nil {
			return nil
		}
		raw, err := pl.v.readText(f)
		if err != nil {
			if err == errTextTooBig {
				b.warn("text_too_large", p, "", "文件太大，正文未导入")
				return nil
			}
			return err
		}
		title, body := splitTitle(string(raw))
		if title != "" {
			n.title = title
		}
		if rowKeys != nil {
			body = stripRowProps(body, rowKeys)
		}
		body = notionHTML(body)
		n.blocks, err = pl.linkCtx(n, p).rewrite(mdParse(body))
		return err
	}
}

var propLineRe = regexp.MustCompile(`^([^:\n]{1,100}):\s?(.*)$`)

// stripRowProps 去掉数据库行正文开头的属性行。
func stripRowProps(body string, keys map[string]bool) string {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	start := i
	for i < len(lines) {
		m := propLineRe.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if m == nil || !keys[strings.TrimSpace(m[1])] {
			break
		}
		i++
	}
	if i == start {
		return body
	}
	return strings.Join(lines[i:], "\n")
}

var (
	asideRe  = regexp.MustCompile(`(?s)<aside>\s*(.*?)\s*</aside>`)
	asideImg = regexp.MustCompile(`^<img\s[^>]*>\s*`)
)

// notionHTML 把 Notion 导出里的 HTML 片段改成增强 Markdown：<aside> 标注框。
func notionHTML(md string) string {
	if !strings.Contains(md, "<aside>") {
		return md
	}
	return asideRe.ReplaceAllStringFunc(md, func(s string) string {
		inner := asideRe.FindStringSubmatch(s)[1]
		icon := "icon:info"
		if m := asideImg.FindString(inner); m != "" {
			inner = inner[len(m):]
		} else if e, rest := leadingEmoji(inner); e != "" {
			icon, inner = e, rest
		}
		return "\n<callout icon=\"" + attrEsc(icon) + "\" color=\"gray_background\">\n" + strings.TrimSpace(inner) + "\n</callout>\n"
	})
}

// leadingEmoji 取出开头的表情（含变体选择符、连接符组成的组合表情）。
func leadingEmoji(s string) (string, string) {
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		r := rs[i]
		if unicode.Is(unicode.So, r) || r == 0xFE0F || r == 0x200D || (r >= 0x1F3FB && r <= 0x1F3FF) || (r >= 0x1F1E6 && r <= 0x1F1FF) {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return "", s
	}
	return string(rs[:i]), strings.TrimLeft(string(rs[i:]), " \t")
}

// planDB 解析数据库的 CSV，生成结构与行。
func (pl *notionPlan) planDB(db *pnode, csvPath string) error {
	f := pl.v.get(csvPath)
	raw, err := pl.v.readText(f)
	if err != nil {
		if fatal(err) {
			return err
		}
		pl.b.warn("csv_failed", csvPath, "", "CSV 读取失败，数据库按空表导入")
		raw = []byte("Name\n")
	}
	t, truncated, err := parseCSV(raw, pl.b.s.Limits.MaxCSVRows)
	if err != nil {
		pl.b.warn("csv_failed", csvPath, "", userMessage(err))
		t = &csvTable{header: []string{"Name"}}
	}
	if truncated {
		pl.b.warn("csv_truncated", csvPath, "", "行数超过上限，多出的行未导入")
	}
	keys := map[string]bool{}
	for _, h := range t.header {
		keys[strings.TrimSpace(h)] = true
	}
	// 行的 .md：按标题匹配 CSV 的行
	pool := map[string][]*pnode{}
	for _, c := range db.children {
		if c.kind == nRow {
			c.prepare = pl.preparePage(c.src, keys)
			k := normTitle(c.title)
			pool[k] = append(pool[k], c)
		}
	}
	rowFor := func(title string) *pnode {
		k := normTitle(title)
		if l := pool[k]; len(l) > 0 {
			pool[k] = l[1:]
			return l[0]
		}
		return nil
	}
	dir := path.Dir(csvPath)
	folder := stripExt(csvPath)
	folder = strings.TrimSuffix(folder, "_all")
	o := inferOpts{
		relation: func(item string) *pnode {
			if u, err := url.Parse(item); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
				return pl.pageByURL(u)
			}
			p := path.Clean(path.Join(dir, decodeRef(item)))
			if n := pl.pageAt(p); n != nil {
				return n
			}
			if h := hashIn(item); h != "" {
				return pl.byHash[h]
			}
			return nil
		},
		file: func(item string) *vfile {
			if !isLocalRef(item) {
				return nil
			}
			rel := decodeRef(item)
			for _, p := range []string{path.Join(dir, rel), path.Join(folder, rel)} {
				if f := pl.v.get(path.Clean(p)); f != nil && !isPageFile(f.path) {
					return f
				}
			}
			return nil
		},
	}
	csvPlan(pl.b, pl.v, db, t, o, rowFor)
	return nil
}
