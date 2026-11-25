package target

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func openLocalT(t *testing.T, dir string) Target {
	t.Helper()
	cfg, _ := json.Marshal(LocalConfig{Path: dir})
	tg, err := Open(KindLocal, cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestLocalSuite(t *testing.T) {
	dir := t.TempDir()
	runSuite(t, openLocalT(t, dir), suiteOpts{bigSize: 5<<20 + 123})
	// 目录里不能残留临时文件。
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tempPrefix) {
			t.Errorf("残留临时文件：%s", p)
		}
		return nil
	})
}

func TestLocalConfig(t *testing.T) {
	if _, err := Open(KindLocal, []byte(`{"path":""}`), Deps{}); err == nil {
		t.Fatal("空路径应报错")
	}
	if _, err := Open(KindLocal, []byte(`{"path":"relative/dir"}`), Deps{}); err == nil {
		t.Fatal("相对路径应报错")
	}
	// 根目录不存在（NAS 未挂载）时不能自动创建。
	missing := filepath.Join(t.TempDir(), "nas-not-mounted")
	tg := openLocalT(t, missing)
	err := tg.Put(context.Background(), "a/b.txt", bytes.NewReader([]byte("x")), 1)
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("根目录不存在时应报错，实际 %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("不应自动创建根目录")
	}
	if _, err := tg.List(context.Background(), ""); err == nil {
		t.Fatal("根目录不存在时 List 应报错，不能当成空目标")
	}
}

func TestLocalTempFilesHiddenAndPerms(t *testing.T) {
	dir := t.TempDir()
	tg := openLocalT(t, dir)
	os.MkdirAll(filepath.Join(dir, "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "x", "~$abcd.tmp"), []byte("half"), 0o600)
	old := filepath.Join(dir, "x", "~$0000.tmp")
	os.WriteFile(old, []byte("stale"), 0o600)
	past := time.Now().Add(-72 * time.Hour)
	os.Chtimes(old, past, past)
	mustPut(t, tg, "x/real.bin", []byte("data"))
	if got := listPaths(t, tg, ""); !eqStrings(got, []string{"x/real.bin"}) {
		t.Fatalf("List 应跳过临时文件，实际 %v", got)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("List 应清理 48 小时前的残留临时文件")
	}
	if _, err := os.Stat(filepath.Join(dir, "x", "~$abcd.tmp")); err != nil {
		t.Fatal("List 不能删掉新的临时文件（可能正在上传）")
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "x", "real.bin"))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("备份文件权限应为 0600，实际 %o", st.Mode().Perm())
		}
	}
}

func TestLocalSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要特权")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	tg := openLocalT(t, dir)
	ctx := context.Background()
	if _, err := tg.Get(ctx, "link/secret.txt"); err == nil {
		t.Fatal("通过符号链接读到了根目录之外的文件")
	}
	if err := tg.Put(ctx, "link/new.txt", bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("通过符号链接写到了根目录之外")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Fatal("根目录之外出现了文件")
	}
	if err := tg.Delete(ctx, "link/secret.txt"); err == nil {
		if _, serr := os.Stat(filepath.Join(outside, "secret.txt")); serr != nil {
			t.Fatal("通过符号链接删掉了根目录之外的文件")
		}
	}
}

func TestLocalPutCanceled(t *testing.T) {
	dir := t.TempDir()
	tg := openLocalT(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := randData(t, 1<<20)
	if err := tg.Put(ctx, "c.bin", bytes.NewReader(data), int64(len(data))); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
	}
	if _, err := tg.Stat(context.Background(), "c.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatal("取消的上传不能留下文件")
	}
}
