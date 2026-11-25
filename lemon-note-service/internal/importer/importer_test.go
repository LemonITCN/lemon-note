package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/exporter"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	cat   *catalog.Catalog
	pg    *pages.Service
	db    *dbs.Service
	s     *Service
	user  *model.User
	other *model.User
	space string
	dir   string
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: filepath.Join(dir, "w.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cat, err := catalog.Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	acc := accounts.New(st, cat)
	u, err := acc.CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "password-123", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	o, err := acc.CreateUser(ctx, u.ID, accounts.NewUser{Username: "bob", Password: "password-123", Role: model.RoleMember})
	if err != nil {
		t.Fatal(err)
	}
	dd, err := datadir.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	pg := pages.New(st, cat)
	db := dbs.New(st, cat, pg)
	fs := files.New(st, dd, filepath.Join(dir, "cache"))
	s := New(st, cat, pg, db, fs, settings.New(st), filepath.Join(dir, "cache"), nil)
	s.NotionInterval = 0
	return &env{t: t, ctx: ctx, st: st, cat: cat, pg: pg, db: db, s: s, user: u, other: o, space: cat.VisibleSpaces(u.ID)[0].ID, dir: dir}
}

// zipOf 生成 zip；值为 nil 的条目是目录。
func zipOf(t *testing.T, entries [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngBytes(t *testing.T) string {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// run 上传一个导入包并等任务结束。
func (e *env) run(kind, name string, data []byte, tgt Target) *JobView {
	e.t.Helper()
	f, err := e.s.TempFile()
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		e.t.Fatal(err)
	}
	f.Close()
	if tgt.SpaceID == "" {
		tgt.SpaceID = e.space
	}
	if _, err := e.s.StartFile(e.ctx, e.user.ID, kind, tgt, f.Name(), name); err != nil {
		e.t.Fatal(err)
	}
	e.s.Wait()
	return e.lastJob()
}

func (e *env) lastJob() *JobView {
	e.t.Helper()
	var id string
	if err := e.st.QueryRow(e.ctx, "SELECT id FROM import_jobs ORDER BY created_at DESC, id DESC LIMIT 1").Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	v, err := e.s.Get(e.ctx, e.user.ID, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *env) child(parent, title string) catalog.PageMeta {
	e.t.Helper()
	for _, c := range e.cat.Children(e.space, parent) {
		if c.Title == title && c.TrashedAt == 0 {
			return c
		}
	}
	var titles []string
	for _, c := range e.cat.Children(e.space, parent) {
		titles = append(titles, c.Title)
	}
	e.t.Fatalf("在 %q 下找不到「%s」，现有：%v", parent, title, titles)
	return catalog.PageMeta{}
}

func (e *env) nodes(pageID string) []lnmd.Node {
	e.t.Helper()
	bs, err := e.pg.Blocks(e.ctx, pageID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []lnmd.Node
	for _, b := range bs {
		var n lnmd.Node
		if err := json.Unmarshal([]byte(b.Data), &n); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func (e *env) markdown(pageID string) string {
	return lnmd.Render(e.nodes(pageID), lnmd.RenderOptions{})
}

func find(ns []lnmd.Node, fn func(n lnmd.Node) bool) *lnmd.Node {
	for i := range ns {
		if fn(ns[i]) {
			return &ns[i]
		}
		if f := find(ns[i].Content, fn); f != nil {
			return f
		}
	}
	return nil
}

func ofType(t string) func(lnmd.Node) bool { return func(n lnmd.Node) bool { return n.Type == t } }

func hasWarning(v *JobView, code string) bool {
	for _, w := range v.Report.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func mustDone(t *testing.T, v *JobView) {
	t.Helper()
	if v.Status != StatusDone {
		t.Fatalf("任务没有成功：%s %+v", v.Status, v.Report)
	}
}

const (
	hRoot  = "0123456789abcdef0123456789abcdef"
	hChild = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hDB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hRowA  = "cccccccccccccccccccccccccccccccc"
	hRowB  = "dddddddddddddddddddddddddddddddd"
)

func notionExport(t *testing.T) []byte {
	root := "Root Page " + hRoot
	rootEsc := "Root%20Page%20" + hRoot
	csvHead := "Name,Status,Tags,Due,Done,Points,Website,Email,Blocked by\n"
	rows := "Task A,Todo,\"x, y\",\"November 25, 2026\",Yes,3,https://a.example.com,a@example.com,\n" +
		"Task B,Doing,x,2026/11/26,No,\"1,200\",https://b.example.com,b@example.com,Task A (Task%20A%20" + hRowA + ".md)\n"
	inner := zipOf(t, [][2]string{
		{root + ".md", "# Root Page\n\nIntro with [Child Page](" + rootEsc + "/Child%20Page%20" + hChild + ".md) inline and [other words](" + rootEsc + "/Child%20Page%20" + hChild + ".md).\n\n" +
			"[Child Page](" + rootEsc + "/Child%20Page%20" + hChild + ".md)\n\n![pic](" + rootEsc + "/pic.png)\n\n<aside>\n💡 Callout **body**\n\n</aside>\n\n" +
			"[Tasks](" + rootEsc + "/Tasks%20" + hDB + ".csv)\n\nMissing ![gone](" + rootEsc + "/gone.png)\n\nSee https://www.notion.so/Child-Page-" + hChild + " and [notion link](https://www.notion.so/Child-Page-" + hChild + ")\n"},
		{root + "/Child Page " + hChild + ".md", "# Child Page\n\nBack to [Root Page](../Root%20Page%20" + hRoot + ".md)\n"},
		{root + "/pic.png", pngBytes(t)},
		{root + "/Tasks " + hDB + ".csv", "\ufeff" + csvHead + rows},
		{root + "/Tasks " + hDB + "_all.csv", "\ufeff" + csvHead + rows + "Task C,Done,,,No,,,,\n"},
		{root + "/Tasks " + hDB + "/Task A " + hRowA + ".md", "# Task A\n\nStatus: Todo\nTags: x, y\nPoints: 3\n\nTask body text\n"},
		{root + "/Tasks " + hDB + "/Task B " + hRowB + ".md", "# Task B\n\nStatus: Doing\n\nSecond body\n"},
	})
	// 大导出分包：外层 zip 里套 zip
	return zipOf(t, [][2]string{{"Export-1234/Export-1234-Part-1.zip", string(inner)}})
}

func TestNotionZip(t *testing.T) {
	e := setup(t)
	v := e.run(KindNotionZip, "Export-1234.zip", notionExport(t), Target{})
	mustDone(t, v)
	rep := v.Report
	if rep.Pages != 2 || rep.Databases != 1 || rep.Rows != 3 || rep.Files != 1 {
		t.Fatalf("计数不对：%+v", rep)
	}
	if !hasWarning(v, "file_missing") {
		t.Fatalf("缺失的图片应当报告：%+v", rep.Warnings)
	}
	root := e.child("", "Root Page")
	child := e.child(root.ID, "Child Page")
	tasks := e.child(root.ID, "Tasks")
	if tasks.Kind != model.KindDatabase {
		t.Fatal("Tasks 应当是数据库")
	}
	if len(rep.RootIDs) != 1 || rep.RootIDs[0] != root.ID {
		t.Fatalf("顶层页面不对：%v", rep.RootIDs)
	}
	ns := e.nodes(root.ID)
	if n := find(ns, ofType("childPage")); n == nil || n.Attrs["pageId"] != child.ID {
		t.Fatalf("独占一行的子页面链接应当变成子页面块：%s", e.markdown(root.ID))
	}
	if n := find(ns, func(n lnmd.Node) bool { return n.Type == "mention" && n.Attrs["id"] == child.ID }); n == nil {
		t.Fatal("链接文字等于标题时应当变成提及")
	}
	if n := find(ns, func(n lnmd.Node) bool {
		return n.Type == "text" && linkHref(n) == "/p/"+child.ID && n.Text == "other words"
	}); n == nil {
		t.Fatalf("其他文字的页面链接应当改成站内链接：%s", e.markdown(root.ID))
	}
	if n := find(ns, func(n lnmd.Node) bool {
		return n.Type == "text" && n.Text == "notion link" && linkHref(n) == "/p/"+child.ID
	}); n == nil {
		t.Fatalf("notion.so 链接应当改成站内链接：%s", e.markdown(root.ID))
	}
	img := find(ns, ofType("image"))
	if img == nil || !strings.HasPrefix(img.Attrs["src"].(string), "lemon://file/") {
		t.Fatalf("图片应当上传成附件：%+v", img)
	}
	if c := find(ns, ofType("callout")); c == nil || c.Attrs["icon"] != "💡" || find(c.Content, func(n lnmd.Node) bool { return n.Text == "body" }) == nil {
		t.Fatalf("<aside> 应当变成标注框：%s", e.markdown(root.ID))
	}
	if n := find(ns, ofType("childDatabase")); n == nil || n.Attrs["pageId"] != tasks.ID {
		t.Fatal("数据库链接应当变成内嵌数据库块")
	}
	if n := find(e.nodes(child.ID), func(n lnmd.Node) bool { return n.Type == "mention" && n.Attrs["id"] == root.ID }); n == nil {
		t.Fatalf("../ 相对链接应当解析：%s", e.markdown(child.ID))
	}
	// 数据库结构与行
	info, err := e.db.Info(e.ctx, tasks.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Name": dbs.TTitle, "Status": dbs.TSelect, "Tags": dbs.TMultiSelect, "Due": dbs.TDate, "Done": dbs.TCheckbox,
		"Points": dbs.TNumber, "Website": dbs.TURL, "Email": dbs.TEmail, "Blocked by": dbs.TRelation}
	for name, typ := range want {
		p := info.Schema.Find(name)
		if p == nil || p.Type != typ {
			t.Fatalf("属性 %s 应当推断为 %s：%+v", name, typ, p)
		}
	}
	if p := info.Schema.Find("Blocked by"); p.DatabaseID != tasks.ID {
		t.Fatalf("关联应当指向自己：%+v", p)
	}
	tbl, err := e.db.Load(e.ctx, tasks.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 3 {
		t.Fatalf("应当用 _all.csv 的 3 行，实际 %d", len(tbl.Rows))
	}
	var rowA, rowB *dbs.Row
	for _, r := range tbl.Rows {
		switch r.Title {
		case "Task A":
			rowA = r
		case "Task B":
			rowB = r
		}
	}
	if rowA == nil || rowB == nil {
		t.Fatal("行标题不对")
	}
	text := func(r *dbs.Row, name string) string { return dbs.ToText(tbl.Value(r, info.Schema.Find(name))) }
	if text(rowA, "Status") != "Todo" || text(rowA, "Tags") != "x, y" || text(rowA, "Due") != "2026-11-25" || text(rowA, "Done") != "true" {
		t.Fatalf("行 A 的值不对：%v %v %v %v", text(rowA, "Status"), text(rowA, "Tags"), text(rowA, "Due"), text(rowA, "Done"))
	}
	if text(rowB, "Points") != "1200" || text(rowB, "Blocked by") != "Task A" {
		t.Fatalf("行 B 的值不对：%v %v", text(rowB, "Points"), text(rowB, "Blocked by"))
	}
	md := e.markdown(rowA.ID)
	if !strings.Contains(md, "Task body text") || strings.Contains(md, "Status: Todo") || strings.Contains(md, "# Task A") {
		t.Fatalf("行正文应当去掉标题与属性行：%q", md)
	}
	if c, _ := e.cat.Page(rowA.ID); c.DatabaseID != tasks.ID {
		t.Fatal("行应当属于数据库")
	}
}

func TestObsidianVault(t *testing.T) {
	e := setup(t)
	canvas := `{"nodes":[{"id":"n1","type":"text","text":"# Hello\nworld","x":0,"y":0,"width":200,"height":80,"color":"1"},
		{"id":"n2","type":"file","file":"Notes/Idea.md","x":400,"y":0,"width":200,"height":80},
		{"id":"g1","type":"group","label":"Group","x":-20,"y":-40,"width":700,"height":200}],
		"edges":[{"id":"e1","fromNode":"n1","fromSide":"right","toNode":"n2","toSide":"left","label":"leads to"}]}`
	data := zipOf(t, [][2]string{
		{"Vault/Home.md", "---\ntags: [alpha, beta]\nstatus: draft\naliases:\n  - Start\n---\nSee [[Notes/Idea]] and [[Idea|the idea]] ==important== #tag\n\n" +
			"![[pic.png|200]]\n\n![[Idea]]\n\n> [!warning] Careful\n> callout body [[Home]]\n\nCode `[[not a link]]` %%hidden%% and [[Missing note]]\n\n```\n[[in fence]]\n```\n"},
		{"Vault/Notes/Idea.md", "# Idea\n\nlink back [[Home]] ^block-1\n"},
		{"Vault/Notes/Notes.md", "Folder note body\n"},
		{"Vault/attachments/pic.png", pngBytes(t)},
		{"Vault/Board.canvas", canvas},
		{"Vault/.obsidian/app.json", "{}"},
		{"Vault/unused.pdf", "%PDF-1.4"},
	})
	v := e.run(KindObsidian, "Vault.zip", data, Target{})
	mustDone(t, v)
	root := e.child("", "Vault")
	notes := e.child(root.ID, "Notes")
	idea := e.child(notes.ID, "Idea")
	home := e.child(root.ID, "Home")
	board := e.child(root.ID, "Board")
	if len(e.cat.Children(e.space, notes.ID)) != 1 {
		t.Fatal("文件夹笔记应当合并进目录页面，不再单独成页")
	}
	if !strings.Contains(e.markdown(notes.ID), "Folder note body") {
		t.Fatal("目录页面应当带上文件夹笔记的正文")
	}
	ns := e.nodes(home.ID)
	if ns[0].Type != "callout" || !strings.Contains(lnmd.PlainText(ns[0]), "#alpha") || !strings.Contains(lnmd.PlainText(ns[0]), "draft") {
		t.Fatalf("属性区应当变成开头的标注框：%s", e.markdown(home.ID))
	}
	mentions := 0
	walkNodes(ns, func(n *lnmd.Node) {
		if n.Type == "mention" && n.Attrs["id"] == idea.ID {
			mentions++
		}
	})
	if mentions != 2 {
		t.Fatalf("两个双链都应当变成提及，实际 %d：%s", mentions, e.markdown(home.ID))
	}
	if find(ns, func(n lnmd.Node) bool {
		return n.Type == "text" && len(n.Marks) > 0 && n.Marks[0].Type == "highlight" && n.Text == "important"
	}) == nil {
		t.Fatal("==高亮== 应当变成高亮标记")
	}
	img := find(ns, ofType("image"))
	if img == nil || !strings.HasPrefix(img.Attrs["src"].(string), "lemon://file/") || img.Attrs["width"] != float64(200) {
		t.Fatalf("图片嵌入不对：%+v", img)
	}
	if n := find(ns, ofType("linkToPage")); n == nil || n.Attrs["pageId"] != idea.ID {
		t.Fatal("笔记嵌入应当降级为页面链接")
	}
	if c := find(ns, func(n lnmd.Node) bool { return n.Type == "callout" && n.Attrs["color"] == "yellow_background" }); c == nil {
		t.Fatal("Obsidian 标注框应当转换")
	}
	md := e.markdown(home.ID)
	for _, s := range []string{"`[[not a link]]`", "#tag", "[[in fence]]", "Missing note"} {
		if !strings.Contains(md, s) {
			t.Fatalf("应当保留 %q：%s", s, md)
		}
	}
	if strings.Contains(md, "hidden") {
		t.Fatal("%%注释%% 应当去掉")
	}
	if strings.Contains(e.markdown(idea.ID), "^block-1") || !strings.Contains(e.markdown(idea.ID), "<mention-page id=\""+home.ID+"\"") {
		t.Fatalf("块 id 应当去掉、回链应当解析：%s", e.markdown(idea.ID))
	}
	wb := find(e.nodes(board.ID), ofType("whiteboard"))
	if wb == nil {
		t.Fatal("Canvas 应当变成白板")
	}
	els := wb.Attrs["data"].(map[string]any)["elements"].([]any)
	kinds := map[string]int{}
	var arrow map[string]any
	for _, x := range els {
		m := x.(map[string]any)
		kinds[m["type"].(string)]++
		if m["type"] == "arrow" {
			arrow = m
		}
	}
	if kinds["rectangle"] != 3 || kinds["arrow"] != 1 || kinds["text"] < 3 {
		t.Fatalf("白板元素不对：%v", kinds)
	}
	if arrow["startBinding"].(map[string]any)["elementId"] != "r-n1" || arrow["endBinding"].(map[string]any)["elementId"] != "r-n2" {
		t.Fatalf("箭头应当绑定两端：%v", arrow)
	}
	for _, code := range []string{"embed_note", "unreferenced_files", "link_unresolved"} {
		if !hasWarning(v, code) {
			t.Fatalf("应当报告 %s：%+v", code, v.Report.Warnings)
		}
	}
	if v.Report.Files != 1 {
		t.Fatalf("只有被引用的附件导入：%d", v.Report.Files)
	}
}

func TestMarkdownAndCSV(t *testing.T) {
	e := setup(t)
	target, err := e.pg.Create(e.ctx, e.user.ID, pages.CreateInput{SpaceID: e.space, Title: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	v := e.run(KindMarkdown, "note.md", []byte("# Real Title\n\nHello **world**\n\n- [ ] todo\n"), Target{ParentID: target.ID})
	mustDone(t, v)
	p := e.child(target.ID, "Real Title")
	if md := e.markdown(p.ID); !strings.Contains(md, "**world**") || strings.Contains(md, "# Real Title") || !strings.Contains(md, "- [ ] todo") {
		t.Fatalf("Markdown 正文不对：%q", md)
	}
	csvData := "Name,Age,Joined,Website,Team,Phone\nAda,36,2026-01-02,example.com/ada,Core,+86 138-0000-0000\nBob,41,2026-02-03,https://bob.example,Core,+1 (555) 123-4567\nCy,29,,https://cy.example,Web,\n"
	v = e.run(KindCSV, "people.csv", []byte(csvData), Target{ParentID: target.ID})
	mustDone(t, v)
	db := e.child(target.ID, "people")
	info, err := e.db.Info(e.ctx, db.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, typ := range map[string]string{"Age": dbs.TNumber, "Joined": dbs.TDate, "Team": dbs.TSelect, "Phone": dbs.TPhone, "Website": dbs.TText} {
		if p := info.Schema.Find(name); p == nil || p.Type != typ {
			t.Fatalf("%s 应当是 %s：%+v", name, typ, p)
		}
	}
	tbl, _ := e.db.Load(e.ctx, db.ID)
	if len(tbl.Rows) != 3 || v.Report.Rows != 3 || v.Report.Databases != 1 {
		t.Fatalf("行数不对：%d %+v", len(tbl.Rows), v.Report)
	}
	// 不能导入到数据库里；别人的页面没有权限由 web 层拦，这里只测服务层的目标校验
	_, err = e.s.StartFile(e.ctx, e.user.ID, KindCSV, Target{SpaceID: e.space, ParentID: db.ID}, writeTemp(t, e, []byte(csvData)), "x.csv")
	if err != nil {
		t.Fatal(err)
	}
	e.s.Wait()
	if v := e.lastJob(); v.Status != StatusFailed || !strings.Contains(v.Report.Error, "数据库") {
		t.Fatalf("导入到数据库里应当失败：%+v", v)
	}
	// 只能看自己的任务
	if _, err := e.s.Get(e.ctx, e.other.ID, v.ID); err == nil {
		t.Fatal("别人的任务不能看")
	}
}

func writeTemp(t *testing.T, e *env, data []byte) string {
	f, err := e.s.TempFile()
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	f.Close()
	return f.Name()
}

func TestZipSafety(t *testing.T) {
	e := setup(t)
	// 路径穿越、绝对路径、符号链接都跳过
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string, mode os.FileMode) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if mode != 0 {
			h.SetMode(mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	add("../evil.md", "# Evil\n", 0)
	add("/abs.md", "# Abs\n", 0)
	add("notes/../../up.md", "# Up\n", 0)
	add("link.md", "/etc/passwd", os.ModeSymlink|0o777)
	add("ok.md", "# OK\n\nfine\n", 0)
	zw.Close()
	v := e.run(KindMarkdown, "bad.zip", buf.Bytes(), Target{})
	mustDone(t, v)
	if !hasWarning(v, "unsafe_path") || !hasWarning(v, "symlink") {
		t.Fatalf("应当报告不安全路径与符号链接：%+v", v.Report.Warnings)
	}
	all := e.cat.SpacePages(e.space)
	if len(all) != 1 || all[0].Title != "OK" {
		var ts []string
		for _, p := range all {
			ts = append(ts, p.Title)
		}
		t.Fatalf("只应导入 ok.md：%v", ts)
	}

	// zip 炸弹：解压总量超过上限
	e.s.Limits.MaxTotalBytes = 1 << 20
	big := strings.Repeat("0", 8<<20)
	v = e.run(KindMarkdown, "bomb.zip", zipOf(t, [][2]string{{"a.md", "# A\n"}, {"big.md", big}}), Target{})
	if v.Status != StatusFailed || !strings.Contains(v.Report.Error, "zip 炸弹") {
		t.Fatalf("zip 炸弹应当被拦截：%+v", v)
	}
	// 套娃 zip 的内层同样计数
	inner := zipOf(t, [][2]string{{"big.md", big}})
	v = e.run(KindNotionZip, "nested.zip", zipOf(t, [][2]string{{"part.zip", string(inner)}}), Target{})
	if v.Status != StatusFailed || !strings.Contains(v.Report.Error, "zip 炸弹") {
		t.Fatalf("内层 zip 炸弹应当被拦截：%+v", v)
	}
	e.s.Limits.MaxTotalBytes = DefaultLimits.MaxTotalBytes
	// 文件数上限
	e.s.Limits.MaxEntries = 5
	var many [][2]string
	for i := 0; i < 10; i++ {
		many = append(many, [2]string{"n" + string(rune('a'+i)) + ".md", "x"})
	}
	v = e.run(KindMarkdown, "many.zip", zipOf(t, many), Target{})
	if v.Status != StatusFailed || !strings.Contains(v.Report.Error, "超过 5 个") {
		t.Fatalf("文件数超限应当失败：%+v", v)
	}
	e.s.Limits.MaxEntries = DefaultLimits.MaxEntries
	// 附件超过大小上限：跳过并报告
	e.s.Limits.MaxPages = 2
	v = e.run(KindMarkdown, "toomany.zip", zipOf(t, [][2]string{{"a.md", "a"}, {"b.md", "b"}, {"c.md", "c"}}), Target{})
	if v.Status != StatusFailed || !strings.Contains(v.Report.Error, "上限") {
		t.Fatalf("页面数超限应当失败：%+v", v)
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]bool{"a/b.md": true, "./a.md": true, "a/../b.md": false, "../a": false, "/etc/x": false, "C:/x": false, `a\..\..\b`: false, "a//b": true}
	for in, ok := range cases {
		if _, got := cleanPath(in); got != ok {
			t.Errorf("cleanPath(%q) = %v，应当 %v", in, got, ok)
		}
	}
}

func TestParseDateAndNumber(t *testing.T) {
	for in, want := range map[string]string{"November 25, 2026": "2026-11-25", "November 25, 2026 2:30 PM": "2026-11-25 14:30", "2026/11/25": "2026-11-25",
		"2026年11月25日": "2026-11-25", "@Jan 2, 2026": "2026-01-02"} {
		if got, ok := parseDate(in); !ok || got != want {
			t.Errorf("parseDate(%q) = %q %v", in, got, ok)
		}
	}
	if a, b, ok := parseDateRange("November 25, 2026 → November 27, 2026"); !ok || a != "2026-11-25" || b != "2026-11-27" {
		t.Errorf("日期范围解析不对：%s %s", a, b)
	}
	for in, want := range map[string]float64{"1,200": 1200, "$3.50": 3.5, "50%": 0.5, "-2": -2, "¥8": 8} {
		if got, ok := parseNumber(in); !ok || got != want {
			t.Errorf("parseNumber(%q) = %v %v", in, got, ok)
		}
	}
	if _, ok := parseNumber("12abc"); ok {
		t.Error("12abc 不是数字")
	}
}

// 导出的 Markdown zip 能原样导入回来：页面层级、页面链接、附件、数据库（CSV + 行页面）。
func TestExportRoundTrip(t *testing.T) {
	e := setup(t)
	pic, err := e.s.files.Save(e.ctx, e.user.ID, e.space, "", "pic.png", strings.NewReader(pngBytes(t)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := e.pg.Create(e.ctx, e.user.ID, pages.CreateInput{SpaceID: e.space, Title: "Handbook"})
	child, _ := e.pg.Create(e.ctx, e.user.ID, pages.CreateInput{SpaceID: e.space, ParentID: root.ID, Title: "Setup"})
	var dbID string
	if _, err := e.st.Write(e.ctx, e.user.ID, func(tx *store.Tx) error {
		sc := &dbs.Schema{Properties: []dbs.Property{{ID: "title", Name: "Name", Type: dbs.TTitle},
			{ID: "st", Name: "Status", Type: dbs.TSelect, Options: []dbs.Option{{ID: "o1", Name: "Todo"}, {ID: "o2", Name: "Done"}}}}}
		p, _, err := e.db.CreateTx(tx, e.user.ID, dbs.CreateInput{SpaceID: e.space, ParentID: root.ID, Title: "Tasks", Schema: sc, Zh: true})
		dbID = p.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]string{{"Write docs", "Done"}, {"Ship", "Todo"}} {
		if _, err := e.st.Write(e.ctx, e.user.ID, func(tx *store.Tx) error {
			ns, _ := lnmd.Parse("body of " + r[0])
			bs := make([]pages.BlockDoc, 0, len(ns))
			for _, n := range ns {
				d, _ := json.Marshal(n)
				bs = append(bs, pages.BlockDoc{Type: n.Type, Data: d})
			}
			_, err := e.db.CreateRowTx(tx, e.user.ID, dbID, r[0], map[string]any{"Status": r[1]}, "", bs, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	body := "See <mention-page id=\"" + child.ID + "\"/>\n\n<page id=\"" + child.ID + "\"/>\n\n<database id=\"" + dbID + "\"/>\n\n![pic](lemon://file/" + pic.ID + ")"
	ns, _ := lnmd.Parse(body)
	var bs []pages.BlockDoc
	for _, n := range ns {
		d, _ := json.Marshal(n)
		bs = append(bs, pages.BlockDoc{Type: n.Type, Data: d})
	}
	if _, err := e.st.Write(e.ctx, e.user.ID, func(tx *store.Tx) error {
		_, err := e.pg.ReplaceBlocksTx(tx, e.user.ID, root.ID, bs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ex := exporter.New(e.cat, e.pg, e.db, e.s.files, nil)
	acc := exporter.Access{CanRead: func(id string) bool { return e.cat.CanRead(e.user.ID, id) },
		CanReadSpace: func(id string) bool { return e.cat.SpaceRole(e.user.ID, id) != model.RoleNone }}
	var buf bytes.Buffer
	if err := ex.WriteZip(e.ctx, &buf, acc, root.ID, exporter.Options{Format: "md", Children: true}); err != nil {
		t.Fatal(err)
	}
	target, _ := e.pg.Create(e.ctx, e.user.ID, pages.CreateInput{SpaceID: e.space, Title: "Imported"})
	v := e.run(KindMarkdown, "Handbook.zip", buf.Bytes(), Target{ParentID: target.ID})
	mustDone(t, v)
	hb := e.child(target.ID, "Handbook")
	if len(e.cat.Children(e.space, target.ID)) != 1 {
		t.Fatal("顶层只有一个页面时不应再套一层")
	}
	setup := e.child(hb.ID, "Setup")
	tasks := e.child(hb.ID, "Tasks")
	if tasks.Kind != model.KindDatabase {
		t.Fatalf("CSV 应当还原成数据库：%+v", tasks)
	}
	ns2 := e.nodes(hb.ID)
	if find(ns2, func(n lnmd.Node) bool { return n.Type == "mention" && n.Attrs["id"] == setup.ID }) == nil ||
		find(ns2, func(n lnmd.Node) bool { return n.Type == "childPage" && n.Attrs["pageId"] == setup.ID }) == nil ||
		find(ns2, func(n lnmd.Node) bool { return n.Type == "childDatabase" && n.Attrs["pageId"] == tasks.ID }) == nil {
		t.Fatalf("页面引用没有还原：%s", e.markdown(hb.ID))
	}
	if img := find(ns2, ofType("image")); img == nil || !strings.HasPrefix(img.Attrs["src"].(string), "lemon://file/") {
		t.Fatal("附件没有还原")
	}
	tbl, _ := e.db.Load(e.ctx, tasks.ID)
	info, _ := e.db.Info(e.ctx, tasks.ID)
	if len(tbl.Rows) != 2 || info.Schema.Find("Status").Type != dbs.TSelect {
		t.Fatalf("数据库没有还原：%d 行 %+v", len(tbl.Rows), info.Schema)
	}
	for _, r := range tbl.Rows {
		md := e.markdown(r.ID)
		if !strings.Contains(md, "body of "+r.Title) || strings.Contains(md, "Status:") {
			t.Fatalf("行正文没有还原：%q", md)
		}
	}
	if len(e.cat.Children(e.space, tasks.ID)) != 2 {
		t.Fatal("行页面不应重复成普通页面")
	}
}
