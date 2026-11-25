package target

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/net/webdav"
)

type davServer struct {
	srv         *httptest.Server
	dir         string
	noOverwrite bool // 模拟不支持覆盖式 MOVE 的服务器
	puts        atomic.Int64
	chunked     atomic.Int64
}

// countingBody 记录请求体读到的字节数与错误。
type countingBody struct {
	io.ReadCloser
	n   int64
	err error
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if err != nil && err != io.EOF {
		b.err = err
	}
	return n, err
}

func startDAV(t *testing.T, noOverwrite bool) *davServer {
	t.Helper()
	ds := &davServer{dir: t.TempDir(), noOverwrite: noOverwrite}
	h := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.Dir(ds.dir), LockSystem: webdav.NewMemLS()}
	ds.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "user" || p != "口令 pw" {
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPut {
			ds.puts.Add(1)
			if r.ContentLength < 0 || len(r.TransferEncoding) > 0 {
				ds.chunked.Add(1) // 坚果云不接受 chunked 上传
			}
		}
		if ds.noOverwrite && r.Method == "MOVE" {
			if dst, err := url.Parse(r.Header.Get("Destination")); err == nil {
				rel := strings.TrimPrefix(dst.Path, "/dav")
				if _, err := os.Stat(filepath.Join(ds.dir, filepath.FromSlash(rel))); err == nil {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
			}
		}
		if r.Method == http.MethodPut {
			// 像真实的 WebDAV 服务器一样丢弃没传完的上传。x/net/webdav 会留下半个文件；客户端中断上传后
			// 立刻发出的 DELETE 可能先于这里的处理到达，测试检查残留临时文件时就会偶发失败。
			body := &countingBody{ReadCloser: r.Body}
			r.Body = body
			h.ServeHTTP(w, r)
			if body.err != nil || (r.ContentLength >= 0 && body.n != r.ContentLength) {
				os.Remove(filepath.Join(ds.dir, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/dav"))))
			}
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ds.srv.Close)
	return ds
}

func openDAV(t *testing.T, ds *davServer, user, pw, prefix string) Target {
	t.Helper()
	cfg, _ := json.Marshal(WebDAVConfig{URL: ds.srv.URL + "/dav/", Username: user, Password: pw, Prefix: prefix})
	tg, err := Open(KindWebDAV, cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func TestWebDAVSuite(t *testing.T) {
	ds := startDAV(t, false)
	runSuite(t, openDAV(t, ds, "user", "口令 pw", "备份/笔记 1"), suiteOpts{bigSize: 3<<20 + 7})
	if ds.chunked.Load() != 0 {
		t.Fatalf("有 %d 次 PUT 用了 chunked 传输", ds.chunked.Load())
	}
	// 文件确实落在 prefix 子目录下，且没有残留临时文件。
	if _, err := os.Stat(filepath.Join(ds.dir, "备份", "笔记 1", "dir", "sub", "c.bin")); err != nil {
		t.Fatalf("文件不在 prefix 子目录下：%v", err)
	}
	filepath.WalkDir(ds.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tempPrefix) {
			t.Errorf("残留临时文件：%s", p)
		}
		return nil
	})
}

func TestWebDAVNoOverwriteMove(t *testing.T) {
	ds := startDAV(t, true)
	tg := openDAV(t, ds, "user", "口令 pw", "")
	mustPut(t, tg, "m/a.txt", []byte("one"))
	mustPut(t, tg, "m/a.txt", []byte("two"))
	if got := mustGet(t, tg, "m/a.txt"); string(got) != "two" {
		t.Fatalf("不支持覆盖式 MOVE 时覆盖写失败：%q", got)
	}
}

func TestWebDAVWrongPassword(t *testing.T) {
	ds := startDAV(t, false)
	tg := openDAV(t, ds, "user", "wrong", "")
	err := Probe(context.Background(), tg)
	if err == nil || !strings.Contains(err.Error(), "用户名或口令不正确") {
		t.Fatalf("口令错误应提示「用户名或口令不正确」，实际 %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("错误信息泄露了口令")
	}
	if _, err := tg.List(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "用户名或口令不正确") {
		t.Fatalf("List 口令错误提示不对：%v", err)
	}
}

func TestWebDAVDirCacheAvoidsMkcol(t *testing.T) {
	ds := startDAV(t, false)
	var mkcol atomic.Int64
	inner := ds.srv.Config.Handler
	ds.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "MKCOL" {
			mkcol.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	tg := openDAV(t, ds, "user", "口令 pw", "p")
	for i := 0; i < 5; i++ {
		mustPut(t, tg, "same/dir/f"+string(rune('a'+i)), []byte("x"))
	}
	if n := mkcol.Load(); n > 4 { // 第一次：建最深一级失败 1 次 + 逐级 3 次
		t.Fatalf("同一目录多次上传发了 %d 次 MKCOL，应只建一次", n)
	}
}

func TestWebDAVConfigErrors(t *testing.T) {
	bad := []WebDAVConfig{
		{URL: ""},
		{URL: "ftp://x/"},
		{URL: "https://user:pw@x/dav"},
		{URL: "https://x/dav?a=1"},
		{URL: "https://x/dav", Prefix: "../up"},
	}
	for _, c := range bad {
		b, _ := json.Marshal(c)
		if _, err := Open(KindWebDAV, b, Deps{}); err == nil {
			t.Errorf("配置 %+v 应报错", c)
		}
	}
}

func TestWebDAVCanceled(t *testing.T) {
	ds := startDAV(t, false)
	tg := openDAV(t, ds, "user", "口令 pw", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tg.List(ctx, ""); err == nil {
		t.Fatal("取消后 List 应报错")
	}
	if err := tg.Put(ctx, "c.txt", bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("取消后 Put 应报错")
	}
}
