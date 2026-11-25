package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func TestGCFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	discard := slog.New(slog.DiscardHandler)
	boot, err := config.Load(filepath.Join(root, "lemon-note.toml"), config.UserMode)
	if err != nil {
		t.Fatal(err)
	}
	boot.DataDir, boot.CacheDir = filepath.Join(root, "data"), filepath.Join(root, "cache")
	pc := persist.Config{DataDir: boot.DataDir, CacheDir: boot.CacheDir, HostID: "gc-test", Version: "test", Logger: discard}
	if err := persist.Initialize(ctx, pc, store.SQLite); err != nil {
		t.Fatal(err)
	}
	inst, err := persist.Open(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	a := New(boot, config.UserMode, discard)
	svc, err := a.buildServices(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if svc.Backup != nil {
			svc.Backup.Close()
		}
		svc.Search.Close()
		inst.Close(ctx)
	}()
	u, err := svc.Accounts.CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "Alice-Pass-2026!x", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	sp := svc.Cat.VisibleSpaces(u.ID)[0].ID
	save := func(content string) *model.File {
		f, err := svc.Files.Save(ctx, u.ID, sp, "", "a.txt", strings.NewReader(content), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	used, inHistory, unused, fresh := save("正文引用"), save("历史版本引用"), save("没人引用"), save("刚上传")
	shared := save("没人引用") // 和 unused 同内容
	img := func(id string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"type": "image", "attrs": map[string]any{"id": "b-" + id, "src": "lemon://file/" + id}})
		return b
	}
	p, err := svc.Pages.Create(ctx, u.ID, pages.CreateInput{SpaceID: sp, Title: "页", Blocks: []pages.BlockDoc{{ID: "b-" + inHistory.ID, Type: "image", Data: img(inHistory.ID)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Pages.SaveVersion(ctx, u.ID, p.ID, "manual", nil); err != nil {
		t.Fatal(err)
	}
	// 正文换成另一张图：inHistory 只剩历史版本引用
	if _, err := svc.Pages.ApplyOps(ctx, u.ID, "", p.ID, []pages.BlockOp{{Op: "del", ID: "b-" + inHistory.ID, BaseVersion: 1}, {Op: "put", ID: "b-" + used.ID, Type: "image", Data: img(used.ID), Position: "a0"}}); err != nil {
		t.Fatal(err)
	}
	// 除了 fresh，都改成 40 天前创建
	old := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	for _, f := range []*model.File{used, inHistory, unused, shared} {
		f.CreatedAt = old
		if _, err := inst.Store.Write(ctx, "t", func(tx *store.Tx) error { return tx.Put("files", f) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := gcFiles(ctx, svc); err != nil {
		t.Fatal(err)
	}
	state := func(f *model.File) bool {
		g, err := svc.Files.Get(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		return g.DeletedAt != nil
	}
	if state(used) || state(inHistory) || state(fresh) {
		t.Fatal("被正文、历史版本引用的或刚上传的附件不能标记删除")
	}
	if !state(unused) || !state(shared) {
		t.Fatal("超过 30 天没人引用的附件应标记删除")
	}
	if !inst.Dir.HasBlob(unused.SHA256) {
		t.Fatal("刚标记删除时不能删文件")
	}
	// 删除标记满 30 天后才删文件
	for _, f := range []*model.File{unused, shared} {
		g, _ := svc.Files.Get(ctx, f.ID)
		g.DeletedAt = &old
		if _, err := inst.Store.Write(ctx, "t", func(tx *store.Tx) error { return tx.Put("files", g) }); err != nil {
			t.Fatal(err)
		}
	}
	release := inst.Hold()
	if err := gcFiles(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if !inst.Dir.HasBlob(unused.SHA256) {
		t.Fatal("备份进行中不能删文件")
	}
	release()
	if err := gcFiles(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if inst.Dir.HasBlob(unused.SHA256) {
		t.Fatal("删除满 30 天的附件文件应删掉")
	}
	if !inst.Dir.HasBlob(used.SHA256) || !inst.Dir.HasBlob(inHistory.SHA256) {
		t.Fatal("还在用的附件文件不能删")
	}
}
