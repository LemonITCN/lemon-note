package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/engine"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func TestBackupCLI(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data, cache, tdir := filepath.Join(root, "data"), filepath.Join(root, "cache"), filepath.Join(root, "nas")
	os.MkdirAll(tdir, 0o755)
	cfgPath := filepath.Join(root, "lemon-note.toml") // 不存在：用默认值加命令行选项
	pc := persist.Config{DataDir: data, CacheDir: cache, HostID: "cli-test", Version: "test", Logger: slog.New(slog.DiscardHandler)}
	if err := persist.Initialize(ctx, pc, store.SQLite); err != nil {
		t.Fatal(err)
	}
	inst, err := persist.Open(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		_, err := inst.Store.Write(ctx, "t", func(tx *store.Tx) error {
			return tx.Put("settings", store.Row{"id": "k" + string(rune('a'+i)), "value": "v", "updated_at": time.Now().UnixMilli()})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	key, _ := inst.SecretKey()
	box, _ := crypt.New(key)
	e, err := engine.New(engine.Options{Inst: inst, Box: box, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"path": tdir})
	name, kind, mode := "nas", "local", engine.ModeFull
	if _, err := e.CreateTarget(ctx, "t", engine.TargetInput{Name: &name, Kind: &kind, Config: cfg, Mode: &mode,
		Encryption: &engine.EncryptionInput{Enabled: true, Password: "cli password 123"}}); err != nil {
		t.Fatal(err)
	}
	origLineage := inst.Lineage
	e.Close()
	inst.Close(ctx)

	global := []string{"--config", cfgPath, "--data-dir", data, "--cache-dir", cache}
	run := func(args ...string) error { return runBackupCmd(append(args, global...)) }
	if err := run("list"); err != nil {
		t.Fatal(err)
	}
	if err := run("run", "nas"); err != nil {
		t.Fatal(err)
	}
	var pkg string
	entries, _ := os.ReadDir(tdir)
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".lnbak") {
			pkg = en.Name()
		}
	}
	if pkg == "" {
		t.Fatal("没有生成备份包")
	}
	if err := run("list", "nas"); err != nil {
		t.Fatal(err)
	}
	if err := run("verify", "nas", pkg); err != nil {
		t.Fatal(err)
	}
	// 换机器场景：不依赖数据目录，直接对 .lnbak 文件恢复，口令从文件读。
	pwFile := filepath.Join(root, "pw")
	os.WriteFile(pwFile, []byte("cli password 123\n"), 0o600)
	dest := filepath.Join(root, "restored")
	if err := runBackupCmd([]string{"restore", filepath.Join(tdir, pkg), "--to", dest, "--password-file", pwFile}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(pwFile, []byte("wrong password\n"), 0o600)
	if err := runBackupCmd([]string{"verify", tdir, pkg, "--password-file", pwFile}); err == nil {
		t.Fatal("口令错误时校验应失败")
	}
	got, err := persist.Open(ctx, persist.Config{DataDir: dest, CacheDir: filepath.Join(root, "cache2"), HostID: "x", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := got.Store.QueryRow(ctx, "SELECT value FROM settings WHERE id = 'kt'").Scan(&v); err != nil || v != "v" {
		t.Fatalf("恢复出的数据不对：%v", err)
	}
	got.Close(ctx)

	// 覆盖当前数据目录：原目录改名保留。
	if err := run("restore", "nas", pkg, "--overwrite", "--yes"); err != nil {
		t.Fatal(err)
	}
	asides, _ := filepath.Glob(data + ".before-restore-*")
	if len(asides) != 1 {
		t.Fatalf("原数据目录应改名保留：%v", asides)
	}
	cur, err := persist.Open(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close(ctx)
	if err := cur.Store.QueryRow(ctx, "SELECT value FROM settings WHERE id = 'ka'").Scan(&v); err != nil || v != "v" {
		t.Fatalf("覆盖恢复后的数据不对：%v", err)
	}
	if cur.Lineage == origLineage {
		t.Fatal("覆盖恢复后应换新血统")
	}
}
