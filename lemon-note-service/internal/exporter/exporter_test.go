package exporter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

type fixture struct {
	t                       *testing.T
	ctx                     context.Context
	s                       *Service
	cat                     *catalog.Catalog
	alice, bob              string
	root, child, db, row    string
	secret, alicePic, bobFi string
}

func mdBlocks(t *testing.T, md string) []pages.BlockDoc {
	ns, err := lnmd.Parse(md)
	if err != nil {
		t.Fatal(err)
	}
	var out []pages.BlockDoc
	for _, n := range ns {
		b, _ := json.Marshal(n)
		out = append(out, pages.BlockDoc{Type: n.Type, Data: b})
	}
	return out
}

func setup(t *testing.T) *fixture {
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
	a, _ := acc.CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "password-123", Role: model.RoleAdmin})
	b, _ := acc.CreateUser(ctx, a.ID, accounts.NewUser{Username: "bob", Password: "password-123", Role: model.RoleMember})
	dd, _ := datadir.Open(filepath.Join(dir, "data"))
	pg := pages.New(st, cat)
	db := dbs.New(st, cat, pg)
	fs := files.New(st, dd, filepath.Join(dir, "cache"))
	f := &fixture{t: t, ctx: ctx, s: New(cat, pg, db, fs, nil), cat: cat, alice: a.ID, bob: b.ID}
	sp := cat.VisibleSpaces(a.ID)[0].ID
	bsp := cat.VisibleSpaces(b.ID)[0].ID
	// bob 的私有页面与附件：alice 的页面里引用了它们，导出时不能带出来
	sec, err := pg.Create(ctx, b.ID, pages.CreateInput{SpaceID: bsp, Title: "Bob Secret"})
	if err != nil {
		t.Fatal(err)
	}
	f.secret = sec.ID
	bf, err := fs.Save(ctx, b.ID, bsp, sec.ID, "secret.txt", strings.NewReader("top secret"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	f.bobFi = bf.ID
	pic, err := fs.Save(ctx, a.ID, sp, "", "pic.png", strings.NewReader("\x89PNG\r\n\x1a\nfake"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	f.alicePic = pic.ID
	root, err := pg.Create(ctx, a.ID, pages.CreateInput{SpaceID: sp, Title: "Root"})
	if err != nil {
		t.Fatal(err)
	}
	f.root = root.ID
	child, err := pg.Create(ctx, a.ID, pages.CreateInput{SpaceID: sp, ParentID: root.ID, Title: "Child",
		Blocks: mdBlocks(t, "Back to <mention-page id=\""+root.ID+"\"/>\n\n![p](lemon://file/"+pic.ID+")")})
	if err != nil {
		t.Fatal(err)
	}
	f.child = child.ID
	if _, err := pg.Create(ctx, a.ID, pages.CreateInput{SpaceID: sp, ParentID: root.ID, Title: "Same"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Create(ctx, a.ID, pages.CreateInput{SpaceID: sp, ParentID: root.ID, Title: "Same"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Create(ctx, a.ID, pages.CreateInput{SpaceID: sp, ParentID: root.ID, Title: "a/b:c"}); err != nil {
		t.Fatal(err)
	}
	_, err = st.Write(ctx, a.ID, func(tx *store.Tx) error {
		sc := &dbs.Schema{Properties: []dbs.Property{{ID: "title", Name: "Name", Type: dbs.TTitle},
			{ID: "st", Name: "Status", Type: dbs.TSelect, Options: []dbs.Option{{ID: "o1", Name: "Todo"}, {ID: "o2", Name: "Done"}}},
			{ID: "dn", Name: "Done", Type: dbs.TCheckbox}, {ID: "fl", Name: "Files", Type: dbs.TFiles}}}
		p, _, err := db.CreateTx(tx, a.ID, dbs.CreateInput{SpaceID: sp, ParentID: root.ID, Title: "Tasks", Schema: sc, Inline: true, Zh: true})
		f.db = p.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Write(ctx, a.ID, func(tx *store.Tx) error {
		r, err := db.CreateRowTx(tx, a.ID, f.db, "Row 1", map[string]any{"Status": "Done", "Done": true,
			"Files": []any{map[string]any{"file_id": pic.ID, "name": "pic.png"}}}, "", mdBlocks(t, "row body"), false)
		f.row = r.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	body := "Intro <mention-page id=\"" + child.ID + "\"/> and [link](/p/" + child.ID + ") and <mention-page id=\"" + sec.ID + "\"/>\n\n" +
		"<page id=\"" + child.ID + "\">Child</page>\n\n<database id=\"" + f.db + "\">Tasks</database>\n\n" +
		"![pic](lemon://file/" + pic.ID + ")\n\n<file src=\"lemon://file/" + bf.ID + "\" name=\"secret.txt\"/>\n\n" +
		"<callout icon=\"💡\" color=\"blue_background\">\nNote\n</callout>\n\n- [x] done item\n\n```go\nx := 1\n```\n\n| a | b |\n| --- | --- |\n| 1 | 2 |"
	if _, err := st.Write(ctx, a.ID, func(tx *store.Tx) error {
		_, err := pg.ReplaceBlocksTx(tx, a.ID, root.ID, mdBlocks(t, body))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) access(user string) Access {
	return Access{
		CanRead:      func(id string) bool { return f.cat.PageRole(user, id) != model.RoleNone },
		CanReadSpace: func(id string) bool { return f.cat.SpaceRole(user, id) != model.RoleNone },
		Render: lnmd.RenderOptions{PageTitle: func(id string) (string, bool) {
			p, ok := f.cat.Page(id)
			if !ok || f.cat.PageRole(user, id) == model.RoleNone {
				return "", false
			}
			return p.Title, true
		}},
	}
}

func (f *fixture) zip(id string, o Options) map[string]string {
	f.t.Helper()
	acc := f.access(f.alice)
	_, single, err := f.s.Plan(f.ctx, acc, id, o)
	if err != nil {
		f.t.Fatal(err)
	}
	if single {
		f.t.Fatal("应当打 zip")
	}
	var buf bytes.Buffer
	if err := f.s.WriteZip(f.ctx, &buf, acc, id, o); err != nil {
		f.t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range zr.File {
		rc, _ := e.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[e.Name] = string(b)
	}
	return out
}

func names(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestExportMarkdownZip(t *testing.T) {
	f := setup(t)
	z := f.zip(f.root, Options{Format: "md", Children: true})
	for _, n := range []string{"Root.md", "Root/Child.md", "Root/Same.md", "Root/Same (2).md", "Root/a b c.md", "Root/Tasks.csv", "Root/Tasks/Row 1.md", "files/pic.png"} {
		if _, ok := z[n]; !ok {
			t.Fatalf("缺少 %s：%v", n, names(z))
		}
	}
	if len(z) != 8 {
		t.Fatalf("文件不对：%v", names(z))
	}
	root := z["Root.md"]
	for _, s := range []string{"# Root", "[Child](Root/Child.md)", "[link](Root/Child.md)", "[Tasks](Root/Tasks.csv)", "![pic](files/pic.png)", "- [x] done item",
		"[Same](Root/Same%20%282%29.md)", "[a/b:c](Root/a%20b%20c.md)"} {
		if !strings.Contains(root, s) {
			t.Fatalf("Root.md 应当包含 %q：\n%s", s, root)
		}
	}
	// 没有权限的页面与附件：不泄露标题、不打包附件
	if strings.Contains(root, "Bob Secret") || strings.Contains(root, "secret.txt](") {
		t.Fatalf("不能带出无权访问的内容：\n%s", root)
	}
	for n, body := range z {
		if strings.Contains(body, "top secret") || strings.Contains(n, "secret") {
			t.Fatal("别人的附件不能导出")
		}
	}
	if !strings.Contains(z["Root/Child.md"], "[Root](../Root.md)") || !strings.Contains(z["Root/Child.md"], "![p](../files/pic.png)") {
		t.Fatalf("子页面的相对路径不对：\n%s", z["Root/Child.md"])
	}
	csv := z["Root/Tasks.csv"]
	if !strings.HasPrefix(csv, "\ufeffName,Status,Done,Files\n") || !strings.Contains(csv, "Row 1,Done,Yes,../files/pic.png") {
		t.Fatalf("CSV 应当是显示值：%q", csv)
	}
	row := z["Root/Tasks/Row 1.md"]
	if !strings.Contains(row, "Status: Done") || !strings.Contains(row, "row body") {
		t.Fatalf("行页面不对：\n%s", row)
	}
	if z["files/pic.png"] != "\x89PNG\r\n\x1a\nfake" {
		t.Fatal("附件内容不对")
	}
}

func TestExportHTMLZip(t *testing.T) {
	f := setup(t)
	z := f.zip(f.root, Options{Format: "html", Children: true})
	root := z["Root.html"]
	for _, s := range []string{"<!doctype html>", "<title>Root</title>", `<a href="Root/Child.html">Child</a>`, `<img src="files/pic.png"`,
		`<input type="checkbox" disabled checked>`, `class="language-go"`, "<th>a</th>", `class="callout"`, "无权访问"} {
		if !strings.Contains(root, s) {
			t.Fatalf("Root.html 应当包含 %q：\n%s", s, root)
		}
	}
	if strings.Contains(root, "Bob Secret") || strings.Contains(root, "<script") {
		t.Fatal("HTML 不能泄露无权访问的标题，也不能带脚本")
	}
	if !strings.Contains(z["Root/Child.html"], `href="../Root.html"`) {
		t.Fatal("子页面回链不对")
	}
	if _, ok := z["Root/Tasks/Row 1.html"]; !ok {
		t.Fatalf("行页面应当导出：%v", names(z))
	}
}

func TestExportSingleAndPermission(t *testing.T) {
	f := setup(t)
	acc := f.access(f.alice)
	// 没有子页面、没有附件：单个文件
	out, single, err := f.s.Plan(f.ctx, acc, f.child, Options{Format: "md"})
	if err != nil || single {
		t.Fatalf("Child 有图片，应当打 zip：%v %v", single, err)
	}
	same := ""
	for _, c := range f.cat.Children(f.cat.VisibleSpaces(f.alice)[0].ID, f.root) {
		if c.Title == "Same" {
			same = c.ID
		}
	}
	out, single, err = f.s.Plan(f.ctx, acc, same, Options{Format: "md"})
	if err != nil || !single || out.Filename != "Same.md" {
		t.Fatalf("单页应当是单个 .md：%+v %v %v", out, single, err)
	}
	var b bytes.Buffer
	if err := f.s.WriteSingle(f.ctx, &b, acc, same, Options{Format: "md"}); err != nil || !strings.HasPrefix(b.String(), "# Same\n") {
		t.Fatalf("单页内容不对：%q %v", b.String(), err)
	}
	// 只含当前页：不带子页面
	z := f.zip(f.root, Options{Format: "md"})
	if _, ok := z["Root/Child.md"]; ok || len(z) != 2 {
		t.Fatalf("不含子页面时只导出本页与附件：%v", names(z))
	}
	// 别人的页面：没有权限
	if _, _, err := f.s.Plan(f.ctx, f.access(f.bob), f.root, Options{Format: "md"}); err == nil {
		t.Fatal("bob 不能导出 alice 的页面")
	}
	// bob 导出自己的页面，看不到 alice 的东西
	if _, _, err := f.s.Plan(f.ctx, f.access(f.bob), f.secret, Options{Format: "md"}); err != nil {
		t.Fatal(err)
	}
}

func TestRelPathAndNames(t *testing.T) {
	cases := [][3]string{{"Root.md", "Root/Child.md", "Root/Child.md"}, {"Root/Child.md", "Root.md", "../Root.md"},
		{"A/B/c.md", "files/x y.png", "../../files/x%20y.png"}, {"A/B/c.md", "A/d (1).md", "../d%20%281%29.md"}}
	for _, c := range cases {
		if got := relPath(c[0], c[1]); got != c[2] {
			t.Errorf("relPath(%q, %q) = %q，应当 %q", c[0], c[1], got, c[2])
		}
	}
	if cleanName("  ") != "Untitled" || cleanName("a/b\\c:d") != "a b c d" || cleanName("..x..") != "x" {
		t.Error("文件名清洗不对")
	}
}
