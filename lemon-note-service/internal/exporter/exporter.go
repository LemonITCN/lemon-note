// Package exporter 导出页面：单页 Markdown / HTML，或含子页面、附件、数据库（CSV + 行页面）的 zip。
// 只导出当前用户能读的页面与附件；zip 边生成边写给浏览器，不把全部内容攒在内存里。
package exporter

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
)

// MaxPages 一次导出最多多少个页面（含数据库行）。
const MaxPages = 50000

// Service 是导出服务。
type Service struct {
	cat   *catalog.Catalog
	pages *pages.Service
	dbs   *dbs.Service
	files *files.Service
	log   *slog.Logger
}

// New 创建导出服务。
func New(cat *catalog.Catalog, pg *pages.Service, db *dbs.Service, fs *files.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{cat: cat, pages: pg, dbs: db, files: fs, log: log}
}

// Access 是当前用户的权限判断与渲染选项（由 web 层按请求提供）。
type Access struct {
	CanRead      func(pageID string) bool
	CanReadSpace func(spaceID string) bool
	Render       lnmd.RenderOptions
}

// Options 导出选项。
type Options struct {
	Format   string // md | html
	Children bool   // 含子页面
}

// Output 是导出结果的元信息（写响应头用）。
type Output struct {
	Filename    string
	ContentType string
}

// ErrTooMany 页面太多。
var ErrTooMany = fmt.Errorf("要导出的页面超过 %d 个，请分开导出", MaxPages)

// enode 是导出树里的一个页面。
type enode struct {
	meta     catalog.PageMeta
	file     string // 页面文件在 zip 里的路径（.md / .html / .csv）
	dir      string // 子页面所在目录
	children []*enode
	db       *enode // 行所属的数据库
}

type efile struct {
	m    *model.File
	path string
}

type run struct {
	s      *Service
	ctx    context.Context
	acc    Access
	format string
	nodes  map[string]*enode
	order  []*enode
	files  map[string]*efile
	fOrder []string
	names  map[string]map[string]bool
	tables map[string]*dbs.Table
}

func (s *Service) newRun(ctx context.Context, acc Access, format string) *run {
	return &run{s: s, ctx: ctx, acc: acc, format: format, nodes: map[string]*enode{}, files: map[string]*efile{},
		names: map[string]map[string]bool{}, tables: map[string]*dbs.Table{}}
}

// Plan 决定导出形式：单页没有子页面、没有附件时输出单个 .md / .html，否则打 zip。
func (s *Service) Plan(ctx context.Context, acc Access, rootID string, o Options) (Output, bool, error) {
	meta, ok := s.cat.Page(rootID)
	if !ok || !acc.CanRead(rootID) {
		return Output{}, false, pages.ErrNotFound
	}
	title := cleanName(meta.Title)
	ext := ".md"
	ct := "text/markdown; charset=utf-8"
	if o.Format == "html" {
		ext, ct = ".html", "text/html; charset=utf-8"
	}
	single := meta.Kind != model.KindDatabase && !o.Children
	if single {
		bs, err := s.pages.Blocks(ctx, rootID)
		if err != nil {
			return Output{}, false, err
		}
		for _, b := range bs {
			if strings.Contains(b.Data, "lemon://file/") || strings.Contains(b.Data, "/api/files/") {
				single = false
				break
			}
		}
	}
	if single {
		return Output{Filename: title + ext, ContentType: ct}, true, nil
	}
	return Output{Filename: title + ".zip", ContentType: "application/zip"}, false, nil
}

// WriteSingle 输出单个页面文件。
func (s *Service) WriteSingle(ctx context.Context, w io.Writer, acc Access, rootID string, o Options) error {
	r := s.newRun(ctx, acc, o.Format)
	meta, _ := s.cat.Page(rootID)
	n := &enode{meta: meta, file: cleanName(meta.Title) + r.ext()}
	r.nodes[rootID] = n
	body, err := r.pageBody(n)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, body)
	return err
}

// WriteZip 把页面（与子页面、数据库、附件）流式写成 zip。
func (s *Service) WriteZip(ctx context.Context, w io.Writer, acc Access, rootID string, o Options) error {
	r := s.newRun(ctx, acc, o.Format)
	meta, ok := s.cat.Page(rootID)
	if !ok {
		return pages.ErrNotFound
	}
	if _, err := r.add(meta, nil, "", o.Children, true); err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	for _, n := range r.order {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		if n.meta.Kind == model.KindDatabase {
			err = r.writeCSV(zw, n)
		} else {
			var body string
			if body, err = r.pageBody(n); err == nil {
				err = writeEntry(zw, n.file, n.meta.UpdatedAt, true, strings.NewReader(body))
			}
		}
		if err != nil {
			return err
		}
	}
	for _, id := range r.fOrder {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := r.files[id]
		fh, err := s.files.Open(f.m)
		if err != nil {
			s.log.Warn("导出附件读取失败", "file", id, "err", err)
			continue
		}
		err = writeEntry(zw, f.path, f.m.CreatedAt, compressible(f.m.Mime), fh)
		fh.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

func compressible(mt string) bool {
	switch {
	case strings.HasPrefix(mt, "image/") && mt != "image/svg+xml" && mt != "image/bmp":
		return false
	case strings.HasPrefix(mt, "video/"), strings.HasPrefix(mt, "audio/"):
		return false
	case strings.Contains(mt, "zip"), strings.Contains(mt, "compressed"):
		return false
	}
	return true
}

func writeEntry(zw *zip.Writer, name string, ms int64, deflate bool, r io.Reader) error {
	h := &zip.FileHeader{Name: name, Method: zip.Store}
	if deflate {
		h.Method = zip.Deflate
	}
	if ms > 0 {
		h.Modified = time.UnixMilli(ms)
	}
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}

func (r *run) ext() string {
	if r.format == "html" {
		return ".html"
	}
	return ".md"
}

// add 把页面（递归子页面）加进导出树，分配文件名。
func (r *run) add(meta catalog.PageMeta, db *enode, dir string, children, root bool) (*enode, error) {
	if len(r.nodes) >= MaxPages {
		return nil, ErrTooMany
	}
	n := &enode{meta: meta, db: db}
	ext := r.ext()
	if meta.Kind == model.KindDatabase {
		ext = ".csv"
	}
	stem := r.uniqueStem(dir, cleanName(meta.Title))
	n.file = path.Join(dir, stem+ext)
	n.dir = path.Join(dir, stem)
	r.nodes[meta.ID] = n
	r.order = append(r.order, n)
	if meta.Kind == model.KindDatabase {
		t, err := r.table(meta.ID)
		if err != nil {
			return nil, err
		}
		if t != nil {
			rows := append([]*dbs.Row{}, t.Rows...)
			dbs.SortRowsByPosition(rows)
			for _, row := range rows {
				rm, ok := r.s.cat.Page(row.ID)
				if !ok || row.Template || rm.TrashedAt != 0 || !r.acc.CanRead(row.ID) {
					continue
				}
				c, err := r.add(rm, n, n.dir, children, false)
				if err != nil {
					return nil, err
				}
				n.children = append(n.children, c)
			}
		}
		return n, nil
	}
	if !children {
		return n, nil
	}
	for _, c := range r.s.cat.Children(meta.SpaceID, meta.ID) {
		if c.TrashedAt != 0 || c.IsTemplate || c.DatabaseID != "" || !r.acc.CanRead(c.ID) {
			continue
		}
		cn, err := r.add(c, nil, n.dir, children, false)
		if err != nil {
			return nil, err
		}
		n.children = append(n.children, cn)
	}
	return n, nil
}

// uniqueStem 同一目录里重名时加序号；文件与同名子目录一起占用。
func (r *run) uniqueStem(dir, stem string) string {
	used := r.names[dir]
	if used == nil {
		used = map[string]bool{}
		r.names[dir] = used
	}
	s := stem
	for i := 2; used[strings.ToLower(s)] || (dir == "" && strings.EqualFold(s, "files")); i++ {
		s = fmt.Sprintf("%s (%d)", stem, i)
	}
	used[strings.ToLower(s)] = true
	return s
}

var badName = regexp.MustCompile(`[\x00-\x1f\x7f/\\:*?"<>|]+`)

// cleanName 把标题变成安全的文件名。
func cleanName(t string) string {
	t = strings.TrimSpace(badName.ReplaceAllString(t, " "))
	t = strings.Join(strings.Fields(t), " ")
	t = strings.Trim(t, ". ")
	if utf8.RuneCountInString(t) > 80 {
		t = strings.TrimSpace(string([]rune(t)[:80]))
	}
	if t == "" {
		return "Untitled"
	}
	switch strings.ToUpper(t) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "LPT1":
		t = "_" + t
	}
	return t
}

func (r *run) table(dbID string) (*dbs.Table, error) {
	if t, ok := r.tables[dbID]; ok {
		return t, nil
	}
	t, err := r.s.dbs.Load(r.ctx, dbID)
	if err != nil {
		if errors.Is(err, dbs.ErrNotDB) || errors.Is(err, pages.ErrNotFound) {
			r.tables[dbID] = nil
			return nil, nil
		}
		return nil, err
	}
	r.tables[dbID] = t
	return t, nil
}

// ---------- 链接与附件 ----------

// relPath 计算从 from 文件到 to 文件的相对路径（按段转义，可直接放进链接）。
func relPath(from, to string) string {
	fd := strings.Split(path.Dir(from), "/")
	if path.Dir(from) == "." {
		fd = nil
	}
	ts := strings.Split(to, "/")
	i := 0
	for i < len(fd) && i < len(ts)-1 && fd[i] == ts[i] {
		i++
	}
	var parts []string
	for range fd[i:] {
		parts = append(parts, "..")
	}
	for _, s := range ts[i:] {
		parts = append(parts, strings.NewReplacer("(", "%28", ")", "%29").Replace(url.PathEscape(s)))
	}
	return strings.Join(parts, "/")
}

// pageLink 返回页面在导出包里的相对地址（不在导出范围内返回空）。
func (r *run) pageLink(from *enode, id string) string {
	t := r.nodes[id]
	if t == nil || from == nil {
		return ""
	}
	return relPath(from.file, t.file)
}

// fileLink 登记附件并返回相对地址；没有权限或已删除的附件返回空。
func (r *run) fileLink(from *enode, id string) (string, *model.File) {
	if f, ok := r.files[id]; ok {
		if f == nil {
			return "", nil
		}
		return relPath(from.file, f.path), f.m
	}
	m, err := r.s.files.Get(r.ctx, id)
	if err != nil || m.DeletedAt != nil || !r.canReadFile(m) {
		r.files[id] = nil
		return "", nil
	}
	name := files.CleanName(m.Name)
	stem, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	used := r.names["files"]
	if used == nil {
		used = map[string]bool{}
		r.names["files"] = used
	}
	fn := name
	for i := 2; used[strings.ToLower(fn)]; i++ {
		fn = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
	used[strings.ToLower(fn)] = true
	f := &efile{m: m, path: "files/" + fn}
	r.files[id] = f
	r.fOrder = append(r.fOrder, id)
	return relPath(from.file, f.path), m
}

// canReadFile 附件所属页面可读，或所属空间可读。
func (r *run) canReadFile(m *model.File) bool {
	if m.PageID != nil && *m.PageID != "" && r.acc.CanRead(*m.PageID) {
		return true
	}
	return r.acc.CanReadSpace != nil && r.acc.CanReadSpace(m.SpaceID)
}

func fileIDOf(src string) string {
	switch {
	case strings.HasPrefix(src, "lemon://file/"):
		return strings.TrimPrefix(src, "lemon://file/")
	case strings.HasPrefix(src, "/api/files/"):
		return strings.TrimPrefix(src, "/api/files/")
	}
	return ""
}

func pageIDOf(href string) string {
	switch {
	case strings.HasPrefix(href, "lemon://page/"):
		return strings.TrimPrefix(href, "lemon://page/")
	case strings.HasPrefix(href, "/p/"):
		id := strings.TrimPrefix(href, "/p/")
		if i := strings.IndexAny(id, "?#/"); i >= 0 {
			id = id[:i]
		}
		return id
	}
	return ""
}

// ---------- 页面正文 ----------

func (r *run) loadNodes(id string) ([]lnmd.Node, error) {
	bs, err := r.s.pages.Blocks(r.ctx, id)
	if err != nil {
		return nil, err
	}
	nodes := make([]lnmd.Node, 0, len(bs))
	for _, b := range bs {
		var n lnmd.Node
		if json.Unmarshal([]byte(b.Data), &n) == nil {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

func (r *run) title(id string) (string, bool) {
	if r.acc.Render.PageTitle != nil {
		return r.acc.Render.PageTitle(id)
	}
	m, ok := r.s.cat.Page(id)
	return m.Title, ok
}

func (r *run) pageBody(n *enode) (string, error) {
	nodes, err := r.loadNodes(n.meta.ID)
	if err != nil {
		return "", err
	}
	props := r.rowProps(n)
	title := n.meta.Title
	if strings.TrimSpace(title) == "" {
		title = "Untitled"
	}
	if r.format == "html" {
		h := &htmlRenderer{r: r, from: n}
		return h.document(title, props, nodes), nil
	}
	var b strings.Builder
	b.WriteString("# " + strings.ReplaceAll(strings.TrimSpace(title), "\n", " ") + "\n\n")
	if len(props) > 0 {
		for _, p := range props {
			b.WriteString(p[0] + ": " + strings.ReplaceAll(p[1], "\n", " ") + "\n")
		}
		b.WriteString("\n")
	}
	out := r.mdNodes(n, nodes)
	// 正文里没有引用到的子页面在末尾列成链接，方便浏览，重新导入时也能还原层级
	if n.meta.Kind != model.KindDatabase {
		seen := map[string]bool{}
		walk(nodes, func(x lnmd.Node) {
			if x.Type == "childPage" || x.Type == "childDatabase" {
				id, _ := x.Attrs["pageId"].(string)
				seen[id] = true
			}
		})
		for _, c := range n.children {
			if !seen[c.meta.ID] {
				out = append(out, lnmd.Node{Type: "paragraph", Content: []lnmd.Node{linkText(firstNonEmpty(c.meta.Title, "Untitled"), relPath(n.file, c.file))}})
			}
		}
	}
	b.WriteString(lnmd.Render(out, r.acc.Render))
	b.WriteString("\n")
	return b.String(), nil
}

// rowProps 数据库行的属性（显示值）。
func (r *run) rowProps(n *enode) [][2]string {
	if n.db == nil {
		return nil
	}
	t, _ := r.table(n.db.meta.ID)
	if t == nil {
		return nil
	}
	row := t.Row(n.meta.ID)
	if row == nil {
		return nil
	}
	var out [][2]string
	for i := range t.Schema.Properties {
		p := &t.Schema.Properties[i]
		if p.Type == dbs.TTitle {
			continue
		}
		v := r.cellText(n, t, row, p)
		if v == "" {
			continue
		}
		out = append(out, [2]string{p.Name, v})
	}
	return out
}

// cellText 属性显示值：关联到没有权限的数据库时留空；文件给出导出包里的相对路径。
func (r *run) cellText(from *enode, t *dbs.Table, row *dbs.Row, p *dbs.Property) string {
	switch p.Type {
	case dbs.TRelation:
		if !r.acc.CanRead(p.DatabaseID) {
			return ""
		}
	case dbs.TRollup:
		if rel := t.Schema.Prop(p.Relation); rel == nil || !r.acc.CanRead(rel.DatabaseID) {
			return ""
		}
	case dbs.TCheckbox:
		if b, _ := t.Value(row, p).(bool); b {
			return "Yes"
		}
		return "No"
	case dbs.TFiles:
		var parts []string
		if l, ok := row.Props[p.ID].([]any); ok {
			for _, e := range l {
				m, _ := e.(map[string]any)
				if m == nil {
					continue
				}
				if id, _ := m["file_id"].(string); id != "" {
					if link, _ := r.fileLink(from, id); link != "" {
						parts = append(parts, link)
						continue
					}
				}
				if u, _ := m["url"].(string); u != "" {
					parts = append(parts, u)
				} else if nm, _ := m["name"].(string); nm != "" {
					parts = append(parts, nm)
				}
			}
		}
		return strings.Join(parts, ", ")
	}
	return dbs.ToText(t.Value(row, p))
}

// mdNodes 改写 Markdown 导出的节点：站内页面引用改成相对链接，附件改成 files/ 下的相对路径。
func (r *run) mdNodes(from *enode, ns []lnmd.Node) []lnmd.Node {
	out := make([]lnmd.Node, 0, len(ns))
	for _, n := range ns {
		out = append(out, r.mdNode(from, n))
	}
	return out
}

func linkText(text, href string) lnmd.Node {
	return lnmd.Node{Type: "text", Text: text, Marks: []lnmd.Mark{{Type: "link", Attrs: map[string]any{"href": href}}}}
}

func (r *run) mdNode(from *enode, n lnmd.Node) lnmd.Node {
	switch n.Type {
	case "childPage", "linkToPage", "childDatabase":
		id, _ := n.Attrs["pageId"].(string)
		if link := r.pageLink(from, id); link != "" {
			t, _ := r.title(id)
			return lnmd.Node{Type: "paragraph", Content: []lnmd.Node{linkText(firstNonEmpty(t, "Untitled"), link)}}
		}
		return n
	case "mention":
		if k, _ := n.Attrs["kind"].(string); k == "page" {
			id, _ := n.Attrs["id"].(string)
			if link := r.pageLink(from, id); link != "" {
				t, _ := r.title(id)
				return linkText(firstNonEmpty(t, "Untitled"), link)
			}
		}
		return n
	case "image", "video", "audio":
		src, _ := n.Attrs["src"].(string)
		if id := fileIDOf(src); id != "" {
			n.Attrs = cloneAttrs(n.Attrs)
			if link, _ := r.fileLink(from, id); link != "" {
				n.Attrs["src"] = link
			} else {
				n.Attrs["src"] = ""
			}
		}
		return n
	case "fileBlock":
		src, _ := n.Attrs["src"].(string)
		name, _ := n.Attrs["name"].(string)
		if id := fileIDOf(src); id != "" {
			link, m := r.fileLink(from, id)
			if link == "" {
				return lnmd.Node{Type: "paragraph", Content: []lnmd.Node{{Type: "text", Text: firstNonEmpty(name, "file")}}}
			}
			return lnmd.Node{Type: "paragraph", Content: []lnmd.Node{linkText(firstNonEmpty(name, m.Name), link)}}
		}
		return n
	case "text":
		for i, m := range n.Marks {
			if m.Type != "link" {
				continue
			}
			href, _ := m.Attrs["href"].(string)
			if id := pageIDOf(href); id != "" {
				if link := r.pageLink(from, id); link != "" {
					n.Marks = cloneMarks(n.Marks)
					n.Marks[i] = lnmd.Mark{Type: "link", Attrs: map[string]any{"href": link}}
				}
			} else if id := fileIDOf(href); id != "" {
				n.Marks = cloneMarks(n.Marks)
				if link, _ := r.fileLink(from, id); link != "" {
					n.Marks[i] = lnmd.Mark{Type: "link", Attrs: map[string]any{"href": link}}
				} else {
					n.Marks = append(n.Marks[:i], n.Marks[i+1:]...)
				}
			}
			break
		}
		return n
	}
	if len(n.Content) > 0 {
		n.Content = r.mdNodes(from, n.Content)
	}
	return n
}

func cloneAttrs(a map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

func cloneMarks(ms []lnmd.Mark) []lnmd.Mark { return append([]lnmd.Mark{}, ms...) }

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// ---------- 数据库 ----------

// writeCSV 数据库导出成 CSV（显示值）。
func (r *run) writeCSV(zw *zip.Writer, n *enode) error {
	t, err := r.table(n.meta.ID)
	if err != nil {
		return err
	}
	h := &zip.FileHeader{Name: n.file, Method: zip.Deflate, Modified: time.UnixMilli(n.meta.UpdatedAt)}
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if t == nil {
		cw.Write([]string{"Name"})
		cw.Flush()
		return cw.Error()
	}
	props := t.Schema.Properties
	sort.SliceStable(props, func(i, j int) bool { return props[i].Type == dbs.TTitle && props[j].Type != dbs.TTitle })
	head := make([]string, len(props))
	for i, p := range props {
		head[i] = p.Name
	}
	if err := cw.Write(head); err != nil {
		return err
	}
	for _, c := range n.children {
		row := t.Row(c.meta.ID)
		if row == nil {
			continue
		}
		rec := make([]string, len(props))
		for i := range props {
			rec[i] = r.cellText(n, t, row, &props[i])
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
