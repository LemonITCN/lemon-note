package share

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

type fixture struct {
	s                    *Service
	cat                  *catalog.Catalog
	root, child, outside string
	inlineDB, row, extDB string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: filepath.Join(t.TempDir(), "w.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cat, err := catalog.Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	u, err := accounts.New(st, cat).CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "password-123", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	sp := cat.VisibleSpaces(u.ID)[0].ID
	pg := pages.New(st, cat)
	db := dbs.New(st, cat, pg)
	mk := func(parent, title string) string {
		p, err := pg.Create(ctx, u.ID, pages.CreateInput{SpaceID: sp, ParentID: parent, Kind: model.KindPage, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	f := &fixture{cat: cat}
	f.root = mk("", "公开根")
	f.child = mk(f.root, "子页面")
	f.outside = mk("", "机密")
	_, err = st.Write(ctx, u.ID, func(tx *store.Tx) error {
		p, _, err := db.CreateTx(tx, u.ID, dbs.CreateInput{SpaceID: sp, ParentID: f.root, Title: "内嵌库", Inline: true, Zh: true})
		if err != nil {
			return err
		}
		f.inlineDB = p.ID
		e, _, err := db.CreateTx(tx, u.ID, dbs.CreateInput{SpaceID: sp, ParentID: f.outside, Title: "外部库", Zh: true})
		if err != nil {
			return err
		}
		f.extDB = e.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Write(ctx, u.ID, func(tx *store.Tx) error {
		r, err := db.CreateRowTx(tx, u.ID, f.inlineDB, "第一行", nil, "", nil, false)
		if err == nil {
			f.row = r.ID
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s = New(st, cat, pg, db, nil, settings.New(st), nil, nil)
	return f
}

func pub(f *fixture, children bool) *scope {
	tok := "tok"
	return f.s.newScope(model.PageShare{ID: "s1", Kind: model.SharePublic, PageID: f.root, Token: &tok, IncludeChildren: children})
}

func TestScope(t *testing.T) {
	f := setup(t)
	sc := pub(f, false)
	for id, want := range map[string]bool{f.root: true, f.child: false, f.outside: false, f.inlineDB: true, f.row: true, f.extDB: false, "": false, "nope": false} {
		if got := sc.has(id); got != want {
			t.Errorf("不含子页面 %s: got %v want %v", id, got, want)
		}
	}
	sc = pub(f, true)
	if !sc.has(f.child) || sc.has(f.outside) || sc.has(f.extDB) {
		t.Error("含子页面时范围不对")
	}
	revoked := int64(1)
	sh := sc.share
	sh.RevokedAt = &revoked
	if f.s.newScope(sh).has(f.root) {
		t.Error("已撤销的链接不能访问")
	}
	past := int64(1000)
	sh = sc.share
	sh.ExpiresAt = &past
	if f.s.newScope(sh).has(f.root) {
		t.Error("过期的链接不能访问")
	}
}

func TestSanitize(t *testing.T) {
	f := setup(t)
	sc := pub(f, false)
	in := map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "mention", "attrs": map[string]any{"kind": "page", "id": f.outside, "label": "机密"}},
		map[string]any{"type": "mention", "attrs": map[string]any{"kind": "page", "id": f.inlineDB, "label": "内嵌库"}},
		map[string]any{"type": "text", "text": "a", "marks": []any{map[string]any{"type": "link", "attrs": map[string]any{"href": "/p/" + f.outside}}, map[string]any{"type": "bold"}}},
		map[string]any{"type": "text", "text": "b", "marks": []any{map[string]any{"type": "link", "attrs": map[string]any{"href": "https://x.example/p/" + f.root}}}},
		map[string]any{"type": "text", "text": "c", "marks": []any{map[string]any{"type": "link", "attrs": map[string]any{"href": "https://other.example/p/abc"}}}},
		map[string]any{"type": "text", "text": "d", "marks": []any{map[string]any{"type": "comment", "attrs": map[string]any{"threadId": "t1"}}}},
	}}
	raw, _ := json.Marshal(in)
	out := string(sc.sanitizeBlock(raw, "tok"))
	if strings.Contains(out, f.outside) || strings.Contains(out, "机密") {
		t.Fatalf("范围外页面泄露: %s", out)
	}
	if !strings.Contains(out, `"label":"内嵌库"`) {
		t.Fatalf("范围内提及被误删: %s", out)
	}
	if !strings.Contains(out, `"/share/tok/`+f.root+`"`) {
		t.Fatalf("站内链接没改写: %s", out)
	}
	if !strings.Contains(out, "https://other.example/p/abc") {
		t.Fatalf("外站链接被误删: %s", out)
	}
	if strings.Contains(out, "comment") || strings.Contains(out, "t1") {
		t.Fatalf("评论标记没去掉: %s", out)
	}
	if !strings.Contains(out, `"bold"`) {
		t.Fatalf("其他标记被误删: %s", out)
	}
	cp, _ := json.Marshal(map[string]any{"type": "childPage", "attrs": map[string]any{"pageId": f.child}})
	if o := string(sc.sanitizeBlock(cp, "tok")); strings.Contains(o, f.child) {
		t.Fatalf("不含子页面时子页面块应去掉 id: %s", o)
	}
}

func TestHiddenProps(t *testing.T) {
	f := setup(t)
	sc := &dbs.Schema{Properties: []dbs.Property{
		{ID: "title", Name: "名称", Type: dbs.TTitle},
		{ID: "n", Name: "数量", Type: dbs.TNumber},
		{ID: "rin", Name: "内部关联", Type: dbs.TRelation, DatabaseID: f.inlineDB},
		{ID: "rout", Name: "外部关联", Type: dbs.TRelation, DatabaseID: f.extDB},
		{ID: "roll", Name: "外部汇总", Type: dbs.TRollup, Relation: "rout", Target: "title", Function: "count"},
		{ID: "f1", Name: "加倍", Type: dbs.TFormula, Expression: `prop("数量") * 2`},
		{ID: "f2", Name: "间接", Type: dbs.TFormula, Expression: `prop("外部汇总") + 1`},
		{ID: "f3", Name: "再间接", Type: dbs.TFormula, Expression: `prop("间接") * 2`},
	}}
	hide := hiddenProps(sc, pub(f, false).has)
	want := map[string]bool{"rout": true, "roll": true, "f2": true, "f3": true}
	for _, p := range sc.Properties {
		if hide[p.ID] != want[p.ID] {
			t.Errorf("%s(%s): hidden=%v want %v", p.ID, p.Name, hide[p.ID], want[p.ID])
		}
	}
}

func TestInternalPageID(t *testing.T) {
	for href, want := range map[string]string{"/p/abc": "abc", "lemon://page/x1": "x1", "https://h/p/k?x=1": "k", "/q/abc": "", "https://h/p/": ""} {
		got, _ := internalPageID(href)
		if got != want {
			t.Errorf("%s: %q want %q", href, got, want)
		}
	}
}
