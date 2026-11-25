package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func TestReadFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: filepath.Join(root, "w.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cat, _ := catalog.Load(ctx, st)
	acc := accounts.New(st, cat)
	a, _ := acc.CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "password-123", Role: model.RoleAdmin})
	b, _ := acc.CreateUser(ctx, a.ID, accounts.NewUser{Username: "bob", Password: "password-123", Role: model.RoleMember})
	dir, err := datadir.Open(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	fs := files.New(st, dir, filepath.Join(root, "cache"))
	sp := cat.VisibleSpaces(a.ID)[0].ID
	p, _ := pages.New(st, cat).Create(ctx, a.ID, pages.CreateInput{SpaceID: sp, Title: "页"})
	txt, err := fs.Save(ctx, a.ID, sp, p.ID, "notes.txt", strings.NewReader("第一行\n第二行\n第三行"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32)
	img, _ := fs.Save(ctx, a.ID, sp, p.ID, "a.png", strings.NewReader(png), 1<<20)
	svc := &Services{Store: st, Cat: cat, Files: fs}
	env := func(u *model.User) *Env {
		return &Env{Ctx: ctx, User: u, CanWrite: true, Svc: svc, Reads: NewReadTracker()}
	}
	call := func(e *Env, args map[string]any) *Result {
		raw, _ := json.Marshal(args)
		return Default.Call(e, "read_file", raw)
	}
	r := call(env(a), map[string]any{"file_id": "lemon://file/" + txt.ID, "offset": 2, "limit": 1})
	if r.IsError || !strings.Contains(r.Text, "第二行") || strings.Contains(r.Text, "第一行\n") || !strings.Contains(r.Text, "offset=3") {
		t.Fatalf("分段读文本不对: %+v", r)
	}
	r = call(env(a), map[string]any{"file_id": img.ID})
	if r.IsError || len(r.Images) != 1 || r.Images[0].MediaType != "image/png" {
		t.Fatalf("图片应作为图片返回: %+v", r.Text)
	}
	if r = call(env(b), map[string]any{"file_id": txt.ID}); !r.IsError {
		t.Fatal("没有权限的用户不能读")
	}
}
