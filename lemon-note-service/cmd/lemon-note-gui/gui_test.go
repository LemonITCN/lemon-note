//go:build gui

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestWriteDataDirKeepsOtherSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "lemon-note.toml")
	if err := writeDataDir(p, "/a"); err != nil {
		t.Fatal(err)
	}
	orig := "listen = '127.0.0.1:9000'\ndsn = 'x'\n\n[tls]\nmode = 'file'\ncert_file = 'c.pem'\n"
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDataDir(p, "/new/data"); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	b, _ := os.ReadFile(p)
	if err := toml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	tls, _ := m["tls"].(map[string]any)
	if m["data_dir"] != "/new/data" || m["listen"] != "127.0.0.1:9000" || m["dsn"] != "x" || tls["cert_file"] != "c.pem" {
		t.Fatalf("其他设置应原样保留：%v", m)
	}
	if _, set := m["cache_dir"]; set {
		t.Fatal("不应把默认值写进配置文件")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("配置文件权限应为 600，实际 %v", st.Mode().Perm())
	}
}

func TestResolveDataDir(t *testing.T) {
	root := t.TempDir()
	cur := viewStatus{}
	cur.DataDir = filepath.Join(root, "current")
	cur.CacheDir = filepath.Join(root, "cache")

	// 空目录：直接用
	empty := filepath.Join(root, "empty")
	os.MkdirAll(empty, 0o755)
	os.WriteFile(filepath.Join(empty, ".DS_Store"), nil, 0o644)
	if ch, err := resolveDataDir(empty, cur); err != nil || ch.Path != empty || ch.Kind != "empty" {
		t.Fatalf("空目录：%+v %v", ch, err)
	}
	// 已有的数据目录：直接用
	existing := filepath.Join(root, "existing")
	os.MkdirAll(existing, 0o755)
	os.WriteFile(filepath.Join(existing, "lemon-note.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(existing, "MANIFEST"), nil, 0o644)
	if ch, err := resolveDataDir(existing, cur); err != nil || ch.Kind != "existing" {
		t.Fatalf("已有数据目录：%+v %v", ch, err)
	}
	// 有别的内容（例如同步盘根目录）：在里面建 Lemon Note 子目录
	sync := filepath.Join(root, "OneDrive")
	os.MkdirAll(sync, 0o755)
	os.WriteFile(filepath.Join(sync, "photo.jpg"), []byte("x"), 0o644)
	if ch, err := resolveDataDir(sync, cur); err != nil || ch.Path != filepath.Join(sync, "Lemon Note") || ch.Kind != "empty" {
		t.Fatalf("同步盘根目录：%+v %v", ch, err)
	}
	// 子目录也被别的东西占了：拒绝
	os.MkdirAll(filepath.Join(sync, "Lemon Note"), 0o755)
	os.WriteFile(filepath.Join(sync, "Lemon Note", "other.txt"), []byte("x"), 0o644)
	if _, err := resolveDataDir(sync, cur); err == nil {
		t.Fatal("子目录有别的内容时应拒绝")
	}
	// 当前目录、缓存目录里：拒绝
	if _, err := resolveDataDir(cur.DataDir, cur); err == nil {
		t.Fatal("选当前数据目录应拒绝")
	}
	if _, err := resolveDataDir(filepath.Join(cur.CacheDir, "x"), cur); err == nil {
		t.Fatal("放在缓存目录里应拒绝")
	}
}

func TestStatusLongPoll(t *testing.T) {
	dir := t.TempDir()
	c := newController(filepath.Join(dir, "lemon-note.toml"), slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	srv := httptest.NewServer(c.handler("zh"))
	defer srv.Close()

	get := func(q string) viewStatus {
		t.Helper()
		r, err := http.Get(srv.URL + "/api/status" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var v viewStatus
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := get("")
	if first.Lang != "zh" || first.State != "stopped" || first.ConfigPath == "" || first.Version == "" {
		t.Fatalf("初始状态不对：%+v", first)
	}
	// 有变化时长轮询立即返回
	go func() {
		time.Sleep(100 * time.Millisecond)
		c.notify()
	}()
	start := time.Now()
	next := get("?since=" + itoa(first.Gen))
	if next.Gen == first.Gen || time.Since(start) > 5*time.Second {
		t.Fatalf("长轮询应在状态变化后返回：gen %d → %d，用时 %v", first.Gen, next.Gen, time.Since(start))
	}
	// 没变化时等到超时（用短超时直接测 waitStatus）
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if v := c.waitStatus(ctx, next.Gen, time.Hour); v.Gen != next.Gen {
		t.Fatal("没有变化时版本号不应改变")
	}

	// 页面本身
	r, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("首页：%d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
