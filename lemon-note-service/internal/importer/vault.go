package importer

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
)

// vaultPlan 是 Obsidian 库、通用 Markdown 文件夹与 CSV 的导入计划：文件夹也变成页面，
// .md 是笔记，.canvas 变成白板，.csv 变成数据库，其余文件是附件（被引用到才导入）。
type vaultPlan struct {
	b        *builder
	v        *vfs
	obsidian bool
	byPath   map[string]*pnode   // 文件路径 → 节点
	dirs     map[string]*pnode   // 目录 → 节点
	byStem   map[string][]string // 小写文件名（不含扩展名）→ 笔记路径
	nodePath map[*pnode]string
}

func (s *Service) vaultWork(v *vfs, kind, uploadName string) work {
	return func(ctx context.Context, b *builder) error {
		pl, roots, err := planVault(ctx, b, v, kind, uploadName)
		if err != nil {
			return err
		}
		if err := b.build(roots); err != nil {
			return err
		}
		pl.reportUnreferenced()
		return nil
	}
}

func hiddenPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func pageKind(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown":
		return "md"
	case ".canvas":
		return "canvas"
	case ".csv":
		return "csv"
	}
	return ""
}

func planVault(ctx context.Context, b *builder, v *vfs, kind, uploadName string) (*vaultPlan, []*pnode, error) {
	rootTitle := stripExt(files.CleanName(uploadName))
	if r := v.singleRoot(); r != "" {
		v.stripRoot(r)
		rootTitle = r
	}
	pl := &vaultPlan{b: b, v: v, obsidian: kind == KindObsidian, byPath: map[string]*pnode{}, dirs: map[string]*pnode{},
		byStem: map[string][]string{}, nodePath: map[*pnode]string{}}
	var items []string
	for _, p := range v.paths() {
		if hiddenPath(p) {
			continue
		}
		k := pageKind(p)
		if kind == KindCSV && k != "csv" {
			continue
		}
		if k != "" {
			items = append(items, p)
		}
	}
	if len(items) == 0 {
		switch kind {
		case KindCSV:
			return nil, nil, fail("没有找到 CSV 文件")
		default:
			return nil, nil, fail("没有找到 Markdown 笔记")
		}
	}
	root := newNode(nPage, rootTitle, "")
	pl.dirs["."] = root
	for _, p := range items {
		var n *pnode
		stem := stripExt(path.Base(p))
		switch pageKind(p) {
		case "md":
			n = newNode(nPage, stem, p)
			n.prepare = pl.prepareNote(p)
			pl.byStem[strings.ToLower(stem)] = append(pl.byStem[strings.ToLower(stem)], p)
		case "canvas":
			n = newNode(nPage, stem, p)
			n.prepare = pl.prepareCanvas(p)
		case "csv":
			n = newNode(nDB, stem, p)
		}
		pl.byPath[p] = n
		pl.nodePath[n] = p
		pl.dir(path.Dir(p)).add(n)
	}
	// 文件夹笔记：目录 X 里的 X.md 作为目录页面的正文
	for d, dn := range pl.dirs {
		if d == "." {
			continue
		}
		base := strings.ToLower(path.Base(d))
		for i, c := range dn.children {
			p := pl.nodePath[c]
			if pageKind(p) == "md" && strings.ToLower(stripExt(path.Base(p))) == base {
				dn.prepare = pl.prepareNote(p)
				dn.src = p
				pl.byPath[p] = dn
				dn.children = append(dn.children[:i], dn.children[i+1:]...)
				break
			}
		}
	}
	pl.mergeSiblingDirs()
	// 数据库
	for _, p := range items {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if n := pl.byPath[p]; n != nil && n.kind == nDB {
			if err := pl.planCSV(n, p); err != nil {
				return nil, nil, err
			}
		}
	}
	sortTree(root)
	// 顶层只有一个笔记（或数据库）时不建外层页面，例如单个文件、或本程序导出的「标题.md + 标题/」
	if len(root.children) == 1 && !strings.HasSuffix(root.children[0].src, "/") {
		c := root.children[0]
		c.parent = nil
		return pl, []*pnode{c}, nil
	}
	if kind == KindCSV && len(pl.dirs) == 1 {
		out := root.children
		for _, c := range out {
			c.parent = nil
		}
		return pl, out, nil
	}
	return pl, []*pnode{root}, nil
}

// mergeSiblingDirs 处理同名的文件与目录（Notion 与本程序导出的布局）：P/X.md 旁边的 P/X/ 里的内容挂到笔记 X 下；
// P/X.csv 旁边的 P/X/ 里的笔记是数据库 X 的行。从深到浅处理，行自己的子目录先并进行里。
func (pl *vaultPlan) mergeSiblingDirs() {
	var ds []string
	for d := range pl.dirs {
		if d != "." {
			ds = append(ds, d)
		}
	}
	sort.Slice(ds, func(i, j int) bool { return strings.Count(ds[i], "/") > strings.Count(ds[j], "/") })
	for _, d := range ds {
		dn := pl.dirs[d]
		if dn == nil || dn.prepare != nil || dn.parent == nil {
			continue
		}
		var owner *pnode
		for _, ext := range []string{".md", ".markdown", ".csv"} {
			if o := pl.byPath[d+ext]; o != nil && o != dn && o.parent == dn.parent {
				owner = o
				break
			}
		}
		if owner == nil {
			continue
		}
		parent := dn.parent
		for i, c := range parent.children {
			if c == dn {
				parent.children = append(parent.children[:i], parent.children[i+1:]...)
				break
			}
		}
		for _, c := range dn.children {
			target := owner
			if owner.kind == nDB {
				if pageKind(pl.nodePath[c]) == "md" {
					c.kind = nRow
				} else {
					target = parent // 数据库下只能放行，其他内容挂到数据库的上级
				}
			}
			target.add(c)
		}
		dn.children = nil
		pl.dirs[d] = owner
	}
}

// dir 返回目录对应的页面（没有就逐级建）。
func (pl *vaultPlan) dir(d string) *pnode {
	if d == "" {
		d = "."
	}
	if n := pl.dirs[d]; n != nil {
		return n
	}
	n := newNode(nPage, path.Base(d), d+"/")
	pl.dirs[d] = n
	pl.dir(path.Dir(d)).add(n)
	return n
}

// sortTree 子页面排序：目录在前，同类按标题。
func sortTree(n *pnode) {
	sort.SliceStable(n.children, func(i, j int) bool {
		a, b := n.children[i], n.children[j]
		da, db := strings.HasSuffix(a.src, "/"), strings.HasSuffix(b.src, "/")
		if da != db {
			return da
		}
		return strings.ToLower(a.title) < strings.ToLower(b.title)
	})
	for _, c := range n.children {
		sortTree(c)
	}
}

// pageAt 按路径找页面（相对链接解析用）。
func (pl *vaultPlan) pageAt(p string) *pnode {
	if n := pl.byPath[p]; n != nil {
		return n
	}
	if f := pl.v.get(p); f != nil {
		return pl.byPath[f.path]
	}
	if path.Ext(p) == "" || pageKind(p) == "" {
		if n := pl.byPath[p+".md"]; n != nil {
			return n
		}
		if f := pl.v.get(p + ".md"); f != nil {
			return pl.byPath[f.path]
		}
	}
	if n := pl.dirs[strings.TrimSuffix(p, "/")]; n != nil && p != "." {
		return n
	}
	// 相对路径找不到时按文件名在整个库里找（Obsidian 默认只写文件名）
	if pageKind(p) == "md" || path.Ext(p) == "" {
		return pl.resolveNote(stripExt(path.Base(p)), "")
	}
	return nil
}

// resolveNote 按 Obsidian 规则解析双链目标：先按完整路径，再按文件名（重名时优先同目录、再取路径最短的）。
func (pl *vaultPlan) resolveNote(target, from string) *pnode {
	t := strings.TrimSpace(target)
	if i := strings.IndexByte(t, '#'); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSuffix(strings.TrimSpace(t), "/")
	if t == "" {
		return nil
	}
	if k := pageKind(t); k == "canvas" || k == "csv" {
		if f := pl.v.get(t); f != nil {
			return pl.byPath[f.path]
		}
		for _, p := range pl.v.byBase[strings.ToLower(path.Base(t))] {
			if n := pl.byPath[p]; n != nil {
				return n
			}
		}
		return nil
	}
	t = strings.TrimSuffix(strings.TrimSuffix(t, ".md"), ".markdown")
	if n := pl.byPath[t+".md"]; n != nil {
		return n
	}
	cands := pl.byStem[strings.ToLower(path.Base(t))]
	if strings.Contains(t, "/") {
		var f []string
		lt := strings.ToLower(t)
		for _, c := range cands {
			lc := strings.ToLower(stripExt(c))
			if lc == lt || strings.HasSuffix(lc, "/"+lt) {
				f = append(f, c)
			}
		}
		cands = f
	}
	if len(cands) == 0 {
		return nil
	}
	best := cands[0]
	fromDir := path.Dir(from)
	for _, c := range cands {
		if path.Dir(c) == fromDir {
			best = c
			break
		}
		if len(c) < len(best) {
			best = c
		}
	}
	return pl.byPath[best]
}

// resolveFile 按 Obsidian 规则找附件：相对当前笔记、相对库根、再按文件名。
func (pl *vaultPlan) resolveFile(target, from string) *vfile {
	t := strings.TrimSpace(target)
	if i := strings.IndexByte(t, '#'); i >= 0 {
		t = t[:i]
	}
	if t == "" {
		return nil
	}
	for _, p := range []string{path.Join(path.Dir(from), t), t} {
		if f := pl.v.get(path.Clean(p)); f != nil && !hiddenPath(f.path) {
			return f
		}
	}
	cands := pl.v.byBase[strings.ToLower(path.Base(t))]
	var best *vfile
	for _, p := range cands {
		if hiddenPath(p) {
			continue
		}
		if strings.Contains(t, "/") && !strings.HasSuffix(strings.ToLower(p), strings.ToLower(t)) {
			continue
		}
		if best == nil || len(p) < len(best.path) {
			best = pl.v.files[p]
		}
	}
	return best
}

func (pl *vaultPlan) linkCtx(n *pnode, file string) *linkCtx {
	return &linkCtx{b: pl.b, v: pl.v, cur: n, dir: path.Dir(file), pageAt: pl.pageAt,
		fileByName: func(name string) *vfile { return pl.resolveFile(name, file) }}
}

// prepareNote 读笔记生成正文。
func (pl *vaultPlan) prepareNote(p string) func(ctx context.Context, b *builder, n *pnode) error {
	return pl.prepareNoteRow(p, nil)
}

// prepareNoteRow 读笔记生成正文；rowKeys 非空时是数据库行，去掉开头的属性行（属性在 CSV 里）。
func (pl *vaultPlan) prepareNoteRow(p string, rowKeys map[string]bool) func(ctx context.Context, b *builder, n *pnode) error {
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
		props, body := splitFrontmatter(string(raw))
		if title, rest := splitTitle(body); title != "" {
			switch {
			case !pl.obsidian && n.kind != nDB && !strings.HasSuffix(n.src, "/"):
				n.title, body = title, rest // 通用 Markdown：开头的一级标题就是页面标题
			case normTitle(title) == normTitle(n.title):
				body = rest
			}
		}
		if rowKeys != nil {
			body = stripRowProps(body, rowKeys)
		}
		md := frontmatterCallout(props) + pl.convertSyntax(body, n, p)
		n.blocks, err = pl.linkCtx(n, p).rewrite(mdParse(md))
		return err
	}
}

// planCSV 把库里的 CSV 变成数据库。
func (pl *vaultPlan) planCSV(n *pnode, p string) error {
	f := pl.v.get(p)
	raw, err := pl.v.readText(f)
	if err != nil {
		if fatal(err) {
			return err
		}
		pl.b.warn("csv_failed", p, "", "CSV 读取失败，数据库按空表导入")
		raw = []byte("Name\n")
	}
	t, truncated, err := parseCSV(raw, pl.b.s.Limits.MaxCSVRows)
	if err != nil {
		pl.b.warn("csv_failed", p, "", userMessage(err))
		t = &csvTable{header: []string{"Name"}}
	}
	if truncated {
		pl.b.warn("csv_truncated", p, "", "行数超过上限，多出的行未导入")
	}
	o := inferOpts{file: func(item string) *vfile {
		if !isLocalRef(item) {
			return nil
		}
		f := pl.v.get(path.Clean(path.Join(path.Dir(p), decodeRef(item))))
		if f == nil || isPageFile(f.path) {
			return nil
		}
		return f
	}}
	keys := map[string]bool{}
	for _, h := range t.header {
		keys[strings.TrimSpace(h)] = true
	}
	pool := map[string][]*pnode{}
	for _, c := range n.children {
		if c.kind == nRow {
			c.prepare = pl.prepareNoteRow(pl.nodePath[c], keys)
			k := normTitle(c.title)
			pool[k] = append(pool[k], c)
		}
	}
	csvPlan(pl.b, pl.v, n, t, o, func(title string) *pnode {
		k := normTitle(title)
		if l := pool[k]; len(l) > 0 {
			pool[k] = l[1:]
			return l[0]
		}
		return nil
	})
	return nil
}

// reportUnreferenced 报告没有被任何笔记引用、因此没有导入的附件。
func (pl *vaultPlan) reportUnreferenced() {
	n := 0
	for p := range pl.v.files {
		if hiddenPath(p) || pageKind(p) != "" {
			continue
		}
		if _, ok := pl.b.uploaded["vfs:"+p]; !ok {
			n++
		}
	}
	if n > 0 {
		pl.b.warn("unreferenced_files", "", fmt.Sprint(n), fmt.Sprintf("有 %d 个附件没有被任何笔记引用，未导入", n))
	}
}

// ---------- Obsidian 语法 ----------

var (
	wikiRe        = regexp.MustCompile(`(!?)\[\[([^\[\]\n]+?)\]\]`)
	soleEmbedRe   = regexp.MustCompile(`^\s*!\[\[([^\[\]\n]+?)\]\]\s*$`)
	highlightRe   = regexp.MustCompile(`==([^=\n]+?)==`)
	blockIDRe     = regexp.MustCompile(`\s\^[A-Za-z0-9-]+\s*$`)
	calloutRe     = regexp.MustCompile(`^\s*>\s*\[!([A-Za-z-]+)\]([+-]?)\s*(.*)$`)
	sizeAliasRe   = regexp.MustCompile(`^\d+(x\d+)?$`)
	fenceOpenRe   = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})")
	imageExts     = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".bmp": true, ".avif": true}
	audioExts     = map[string]bool{".mp3": true, ".wav": true, ".m4a": true, ".ogg": true, ".flac": true, ".3gp": true}
	videoExts     = map[string]bool{".mp4": true, ".webm": true, ".mov": true, ".ogv": true, ".mkv": true}
	calloutStyles = map[string][2]string{
		"note": {"icon:info", "blue_background"}, "info": {"icon:info", "blue_background"}, "todo": {"icon:checkbox", "blue_background"},
		"abstract": {"icon:list", "blue_background"}, "summary": {"icon:list", "blue_background"}, "tldr": {"icon:list", "blue_background"},
		"tip": {"icon:sparkle", "green_background"}, "hint": {"icon:sparkle", "green_background"}, "important": {"icon:sparkle", "green_background"},
		"success": {"icon:check", "green_background"}, "check": {"icon:check", "green_background"}, "done": {"icon:check", "green_background"},
		"question": {"icon:info", "purple_background"}, "help": {"icon:info", "purple_background"}, "faq": {"icon:info", "purple_background"},
		"warning": {"icon:warning", "yellow_background"}, "caution": {"icon:warning", "yellow_background"}, "attention": {"icon:warning", "yellow_background"},
		"failure": {"icon:warning", "red_background"}, "fail": {"icon:warning", "red_background"}, "missing": {"icon:warning", "red_background"},
		"danger": {"icon:warning", "red_background"}, "error": {"icon:warning", "red_background"}, "bug": {"icon:warning", "red_background"},
		"example": {"icon:list", "purple_background"}, "quote": {"icon:quote", "gray_background"}, "cite": {"icon:quote", "gray_background"},
	}
)

// convertSyntax 把 Obsidian 特有写法改成增强 Markdown：双链、嵌入、标注框、高亮、注释、块 id。
// 围栏代码块与行内代码里的内容不动。
func (pl *vaultPlan) convertSyntax(body string, cur *pnode, file string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out []string
	fence := ""
	inComment := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if fence != "" {
			out = append(out, line)
			if strings.HasPrefix(strings.TrimSpace(line), fence) && strings.Trim(strings.TrimSpace(line), fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(line); m != nil && !inComment {
			fence = m[1]
			out = append(out, line)
			continue
		}
		line, inComment = stripComments(line, inComment)
		if inComment && strings.TrimSpace(line) == "" {
			continue
		}
		if m := calloutRe.FindStringSubmatch(line); m != nil {
			var inner []string
			for i+1 < len(lines) && strings.HasPrefix(strings.TrimLeft(lines[i+1], " \t"), ">") {
				i++
				l := strings.TrimLeft(lines[i], " \t")[1:]
				inner = append(inner, strings.TrimPrefix(l, " "))
			}
			st, ok := calloutStyles[strings.ToLower(m[1])]
			if !ok {
				st = [2]string{"icon:info", "gray_background"}
			}
			var b strings.Builder
			b.WriteString("\n<callout icon=\"" + st[0] + "\" color=\"" + st[1] + "\">\n")
			if t := strings.TrimSpace(m[3]); t != "" {
				b.WriteString("**" + pl.inlineSyntax(t, cur, file, false) + "**\n\n")
			}
			b.WriteString(pl.convertSyntax(strings.Join(inner, "\n"), cur, file))
			b.WriteString("\n</callout>\n")
			out = append(out, b.String())
			continue
		}
		if m := soleEmbedRe.FindStringSubmatch(line); m != nil {
			out = append(out, pl.wikilink("!", m[1], cur, file, true))
			continue
		}
		out = append(out, pl.inlineSyntax(line, cur, file, true))
	}
	return strings.Join(out, "\n")
}

// stripComments 去掉 %%注释%%（可跨行）。
func stripComments(line string, in bool) (string, bool) {
	if !in && !strings.Contains(line, "%%") {
		return line, false
	}
	var b strings.Builder
	for {
		i := strings.Index(line, "%%")
		if i < 0 {
			if !in {
				b.WriteString(line)
			}
			return b.String(), in
		}
		if !in {
			b.WriteString(line[:i])
		}
		in = !in
		line = line[i+2:]
	}
}

// inlineSyntax 处理一行里行内代码以外的部分。
func (pl *vaultPlan) inlineSyntax(line string, cur *pnode, file string, lineEnd bool) string {
	if lineEnd {
		line = blockIDRe.ReplaceAllString(line, "")
	}
	var b strings.Builder
	for line != "" {
		i := strings.IndexByte(line, '`')
		if i < 0 {
			b.WriteString(pl.textSyntax(line, cur, file))
			break
		}
		b.WriteString(pl.textSyntax(line[:i], cur, file))
		n := 0
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		tick := strings.Repeat("`", n)
		rest := line[i+n:]
		j := strings.Index(rest, tick)
		if j < 0 {
			b.WriteString(line[i:])
			break
		}
		b.WriteString(line[i : i+n+j+n])
		line = rest[j+n:]
	}
	return b.String()
}

func (pl *vaultPlan) textSyntax(s string, cur *pnode, file string) string {
	if strings.Contains(s, "==") {
		s = highlightRe.ReplaceAllString(s, `<mark color="yellow">$1</mark>`)
	}
	if !strings.Contains(s, "[[") {
		return s
	}
	return wikiRe.ReplaceAllStringFunc(s, func(m string) string {
		sm := wikiRe.FindStringSubmatch(m)
		return pl.wikilink(sm[1], sm[2], cur, file, false)
	})
}

// wikilink 转换一个双链或嵌入。
func (pl *vaultPlan) wikilink(bang, inner string, cur *pnode, file string, standalone bool) string {
	inner = strings.ReplaceAll(inner, `\|`, "|")
	target, alias, _ := strings.Cut(inner, "|")
	target, alias = strings.TrimSpace(target), strings.TrimSpace(alias)
	label := alias
	if label == "" {
		label = target
		if i := strings.IndexByte(label, '#'); i > 0 {
			label = label[:i]
		}
		label = path.Base(label)
	}
	ext := strings.ToLower(path.Ext(strings.SplitN(target, "#", 2)[0]))
	isAttachment := ext != "" && pageKind(target) == "" && ext != ".md"
	if isAttachment {
		f := pl.resolveFile(target, file)
		if f == nil {
			pl.b.warn("file_missing", file, target, "引用的附件在导入包里找不到")
			return label
		}
		if bang == "" {
			return "[" + escapeLinkText(label) + "](" + importRef(f.path) + ")"
		}
		return embedFile(f, ext, alias)
	}
	note := pl.resolveNote(target, file)
	if note == nil {
		if strings.HasPrefix(target, "#") {
			return label
		}
		pl.b.warn("link_unresolved", file, target, "双链指向的笔记不存在，保留为文字")
		return label
	}
	if bang != "" {
		pl.b.warn("embed_note", file, target, "笔记嵌入不支持，已改成页面链接")
		if standalone {
			return "\n<link-to-page id=\"" + note.id + "\"/>\n"
		}
	}
	return "<mention-page id=\"" + note.id + "\"/>"
}

func embedFile(f *vfile, ext, alias string) string {
	ref := importRef(f.path)
	switch {
	case imageExts[ext]:
		alt, trailer := "", ""
		if sizeAliasRe.MatchString(alias) {
			w, _, _ := strings.Cut(alias, "x")
			trailer = "{width=" + w + "}"
		} else {
			alt = alias
		}
		return "![" + escapeLinkText(alt) + "](" + ref + ")" + trailer
	case audioExts[ext]:
		return "<audio src=\"" + attrEsc(ref) + "\"/>"
	case videoExts[ext]:
		return "<video src=\"" + attrEsc(ref) + "\"/>"
	}
	return "<file src=\"" + attrEsc(ref) + "\" name=\"" + attrEsc(path.Base(f.path)) + "\"/>"
}

func escapeLinkText(s string) string {
	return strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`).Replace(s)
}

// ---------- 属性区 ----------

type kv struct {
	key  string
	vals []string
}

// splitFrontmatter 拆出开头的 YAML 属性区（只认简单的「键: 值」与列表）。
func splitFrontmatter(s string) ([]kv, string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, s
	}
	lines := strings.Split(s, "\n")
	end := -1
	for i := 1; i < len(lines); i++ {
		if t := strings.TrimRight(lines[i], " \t"); t == "---" || t == "..." {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, s
	}
	body := strings.Join(lines[end+1:], "\n")
	var out []kv
	for _, l := range lines[1:end] {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "- ") && len(out) > 0 && (l[0] == ' ' || l[0] == '\t' || l[0] == '-') {
			out[len(out)-1].vals = append(out[len(out)-1].vals, unquote(strings.TrimSpace(t[2:])))
			continue
		}
		if l[0] == ' ' || l[0] == '\t' {
			if len(out) > 0 {
				out[len(out)-1].vals = append(out[len(out)-1].vals, unquote(t))
			}
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		item := kv{key: strings.TrimSpace(k)}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
			for _, x := range strings.Split(v[1:len(v)-1], ",") {
				if x = unquote(strings.TrimSpace(x)); x != "" {
					item.vals = append(item.vals, x)
				}
			}
		} else if v != "" && v != "|" && v != ">" {
			item.vals = append(item.vals, unquote(v))
		}
		out = append(out, item)
	}
	return out, body
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// frontmatterCallout 把属性区渲染成页面开头的标注框。
func frontmatterCallout(props []kv) string {
	if len(props) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<callout icon=\"icon:info\" color=\"gray_background\">\n")
	for _, p := range props {
		vals := p.vals
		if k := strings.ToLower(p.key); k == "tags" || k == "tag" {
			vals = make([]string, len(p.vals))
			for i, v := range p.vals {
				vals[i] = "#" + strings.TrimPrefix(v, "#")
			}
		}
		b.WriteString("- **" + escapeLinkText(p.key) + "**：" + strings.Join(vals, "、") + "\n")
	}
	b.WriteString("</callout>\n\n")
	return b.String()
}
