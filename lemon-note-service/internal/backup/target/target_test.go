package target

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"testing/iotest"
)

func randData(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

var badPaths = []string{
	"", "/abs", "/", "../x", "a/../b", "a/..", "..", ".", "./a", "a/./b", "a//b", "a/",
	`a\b`, `..\x`, "C:/x", "c:x", "a\x00b", "a\nb", "a\x7fb", "\xff\xfe",
}

func TestCleanPath(t *testing.T) {
	for _, p := range badPaths {
		if _, err := cleanPath(p); err == nil {
			t.Errorf("cleanPath(%q) 应该被拒绝", p)
		}
	}
	good := []string{"a", "a/b/c.txt", "中文 目录/文件 名.txt", "lemon-note-x-20261125T120000.lnbak", "a b/c+d=e@f!g'h(i)", "...a", "a..b", ".hidden"}
	for _, p := range good {
		if got, err := cleanPath(p); err != nil || got != p {
			t.Errorf("cleanPath(%q) = %q, %v", p, got, err)
		}
	}
	if _, err := cleanFilePath("dir/~$abc.tmp"); err == nil {
		t.Error("临时文件名应该被 Put 拒绝")
	}
	for _, p := range []string{"", "a/", "a/b", "a/b-", "dir/"} {
		if _, err := cleanPrefix(p); err != nil {
			t.Errorf("cleanPrefix(%q) 不应报错：%v", p, err)
		}
	}
	for _, p := range []string{"../", "/a", "a//", `a\`, "./"} {
		if _, err := cleanPrefix(p); err == nil {
			t.Errorf("cleanPrefix(%q) 应该被拒绝", p)
		}
	}
	for in, want := range map[string]string{"": "", "/": "", " /a/b/ ": "a/b", "x": "x"} {
		if got, err := cleanRootPrefix(in); err != nil || got != want {
			t.Errorf("cleanRootPrefix(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := cleanRootPrefix("a/../b"); err == nil {
		t.Error("配置 prefix 含 .. 应被拒绝")
	}
}

func TestSizedReader(t *testing.T) {
	data := randData(t, 100000)
	read := func(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

	got, err := read(newSizedReader(bytes.NewReader(data), int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("长度正确时应原样读出：%v", err)
	}
	got, err = read(newSizedReader(iotest.OneByteReader(bytes.NewReader(data)), int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("逐字节读也应原样读出：%v", err)
	}
	if _, err := read(newSizedReader(bytes.NewReader(data), int64(len(data))+1)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据短了应报 ErrSizeMismatch，实际 %v", err)
	}
	// 数据长了：读出的数据不能达到声明长度（最后一段不交出去），并报错。
	sr := newSizedReader(bytes.NewReader(data), int64(len(data))-1)
	got, err = read(sr)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据长了应报 ErrSizeMismatch，实际 %v", err)
	}
	if len(got) >= len(data)-1 {
		t.Fatalf("数据长了时不应交出完整的声明长度（交出了 %d 字节）", len(got))
	}
	if _, err := read(newSizedReader(bytes.NewReader(nil), 0)); err != nil {
		t.Fatalf("空数据长度 0 应成功：%v", err)
	}
	if _, err := read(newSizedReader(bytes.NewReader([]byte{1}), 0)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("声明 0 却有数据应报错，实际 %v", err)
	}
	boom := errors.New("boom")
	if _, err := read(newSizedReader(iotest.ErrReader(boom), 10)); !errors.Is(err, boom) {
		t.Fatalf("下层错误应原样返回，实际 %v", err)
	}
}

func TestCheckedReadCloser(t *testing.T) {
	rc := newCheckedReadCloser(io.NopCloser(bytes.NewReader([]byte("abc"))), 5)
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("数据短于声明长度应报错")
	}
	rc = newCheckedReadCloser(io.NopCloser(bytes.NewReader([]byte("abcdef"))), 5)
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("数据长于声明长度应报错")
	}
	rc = newCheckedReadCloser(io.NopCloser(bytes.NewReader([]byte("abcde"))), 5)
	if b, err := io.ReadAll(rc); err != nil || string(b) != "abcde" {
		t.Fatalf("长度正确应成功：%v", err)
	}
}

func TestOpenUnknownKind(t *testing.T) {
	if _, err := Open("ftp", []byte(`{}`), Deps{}); err == nil {
		t.Fatal("未知类型应报错")
	}
	if _, err := Open(KindLocal, nil, Deps{}); err == nil {
		t.Fatal("空配置应报错")
	}
	if _, err := Open(KindLocal, []byte(`{"path":`), Deps{}); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

// ---------------- 通用一致性测试 ----------------

type suiteOpts struct {
	noEmptyFile bool // 百度网盘不支持空文件
	bigSize     int  // 大文件测试的大小（覆盖分片上传）
}

func mustPut(t *testing.T, tg Target, p string, data []byte) {
	t.Helper()
	if err := tg.Put(context.Background(), p, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("Put(%q): %v", p, err)
	}
}

func mustGet(t *testing.T, tg Target, p string) []byte {
	t.Helper()
	rc, err := tg.Get(context.Background(), p)
	if err != nil {
		t.Fatalf("Get(%q): %v", p, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Get(%q) 读取: %v", p, err)
	}
	return b
}

func listPaths(t *testing.T, tg Target, prefix string) []string {
	t.Helper()
	es, err := tg.List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("List(%q): %v", prefix, err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Path)
	}
	sort.Strings(out)
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func runSuite(t *testing.T, tg Target, o suiteOpts) {
	ctx := context.Background()

	// 空目标
	if _, err := tg.Stat(ctx, "nope.txt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Stat 不存在的文件应返回 ErrNotExist，实际 %v", err)
	}
	if _, err := tg.Get(ctx, "nope/x.txt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Get 不存在的文件应返回 ErrNotExist，实际 %v", err)
	}
	if err := tg.Delete(ctx, "nope/x.txt"); err != nil {
		t.Fatalf("Delete 不存在的文件应成功，实际 %v", err)
	}
	if got := listPaths(t, tg, ""); len(got) != 0 {
		t.Fatalf("空目标 List 应为空，实际 %v", got)
	}
	if got := listPaths(t, tg, "nodir/"); len(got) != 0 {
		t.Fatalf("不存在的目录 List 应为空，实际 %v", got)
	}

	files := map[string][]byte{
		"a.txt":              []byte("hello"),
		"dir/b.bin":          randData(t, 100*1024+3),
		"dir/sub/c.bin":      randData(t, 7),
		"dir2/d.bin":         randData(t, 10),
		"中文 目录/文件 名+(1).txt": []byte("重要笔记"),
	}
	if !o.noEmptyFile {
		files["empty.dat"] = nil
	}
	for p, d := range files {
		mustPut(t, tg, p, d)
	}
	for p, d := range files {
		e, err := tg.Stat(ctx, p)
		if err != nil {
			t.Fatalf("Stat(%q): %v", p, err)
		}
		if e.Path != p || e.Size != int64(len(d)) {
			t.Fatalf("Stat(%q) = %+v，期望大小 %d", p, e, len(d))
		}
		if got := mustGet(t, tg, p); !bytes.Equal(got, d) {
			t.Fatalf("Get(%q) 内容不一致", p)
		}
	}
	var all []string
	for p := range files {
		all = append(all, p)
	}
	sort.Strings(all)
	if got := listPaths(t, tg, ""); !eqStrings(got, all) {
		t.Fatalf("List(\"\") = %v，期望 %v", got, all)
	}
	es, _ := tg.List(ctx, "dir/b.bin")
	if len(es) != 1 || es[0].Size != int64(len(files["dir/b.bin"])) {
		t.Fatalf("List 返回的大小不对：%+v", es)
	}
	cases := map[string][]string{
		"dir/":     {"dir/b.bin", "dir/sub/c.bin"},
		"dir":      {"dir/b.bin", "dir/sub/c.bin", "dir2/d.bin"},
		"dir/sub/": {"dir/sub/c.bin"},
		"dir/s":    {"dir/sub/c.bin"},
		"dir/b":    {"dir/b.bin"},
		"di":       {"dir/b.bin", "dir/sub/c.bin", "dir2/d.bin"},
		"zzz/":     nil,
		"中文 目录/":   {"中文 目录/文件 名+(1).txt"},
	}
	for prefix, want := range cases {
		if got := listPaths(t, tg, prefix); !eqStrings(got, want) {
			t.Fatalf("List(%q) = %v，期望 %v", prefix, got, want)
		}
	}

	// 覆盖写
	mustPut(t, tg, "a.txt", []byte("world, overwritten"))
	if got := mustGet(t, tg, "a.txt"); string(got) != "world, overwritten" {
		t.Fatalf("覆盖写后内容不对：%q", got)
	}

	// 长度不符：不能以正式文件名留下文件，也不能留下可见的临时文件。
	short := randData(t, 5000)
	if err := tg.Put(ctx, "dir/short.bin", bytes.NewReader(short), int64(len(short))+10); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据比声明短应报 ErrSizeMismatch，实际 %v", err)
	}
	if _, err := tg.Stat(ctx, "dir/short.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("长度不符的上传不能留下文件，Stat 返回 %v", err)
	}
	if err := tg.Put(ctx, "dir/long.bin", bytes.NewReader(short), int64(len(short))-10); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("数据比声明长应报 ErrSizeMismatch，实际 %v", err)
	}
	if _, err := tg.Stat(ctx, "dir/long.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("长度不符的上传不能留下文件，Stat 返回 %v", err)
	}
	if !o.noEmptyFile {
		if err := tg.Put(ctx, "dir/zero.bin", bytes.NewReader([]byte("x")), 0); !errors.Is(err, ErrSizeMismatch) {
			t.Fatalf("声明 0 字节却有数据应报 ErrSizeMismatch，实际 %v", err)
		}
		if _, err := tg.Stat(ctx, "dir/zero.bin"); !errors.Is(err, ErrNotExist) {
			t.Fatalf("长度不符的上传不能留下文件，Stat 返回 %v", err)
		}
	}
	// 覆盖写失败时旧文件保持原样。
	if err := tg.Put(ctx, "a.txt", bytes.NewReader(short), int64(len(short))+1); err == nil {
		t.Fatal("长度不符应报错")
	}
	if got := mustGet(t, tg, "a.txt"); string(got) != "world, overwritten" {
		t.Fatalf("覆盖写失败后旧文件被破坏：%q", got)
	}
	if got := listPaths(t, tg, "dir/"); !eqStrings(got, []string{"dir/b.bin", "dir/sub/c.bin"}) {
		t.Fatalf("失败的上传后 List(dir/) = %v", got)
	}

	// 大文件
	if o.bigSize > 0 {
		big := randData(t, o.bigSize)
		mustPut(t, tg, "big/big.bin", big)
		if got := mustGet(t, tg, "big/big.bin"); !bytes.Equal(got, big) {
			t.Fatal("大文件往返不一致")
		}
		if e, err := tg.Stat(ctx, "big/big.bin"); err != nil || e.Size != int64(len(big)) {
			t.Fatalf("大文件 Stat: %+v %v", e, err)
		}
		if err := tg.Delete(ctx, "big/big.bin"); err != nil {
			t.Fatal(err)
		}
	}

	// 删除
	if err := tg.Delete(ctx, "dir/b.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tg.Stat(ctx, "dir/b.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("删除后 Stat 应返回 ErrNotExist，实际 %v", err)
	}
	if err := tg.Delete(ctx, "dir/b.bin"); err != nil {
		t.Fatalf("重复删除应成功，实际 %v", err)
	}
	if got := listPaths(t, tg, "dir/"); !eqStrings(got, []string{"dir/sub/c.bin"}) {
		t.Fatalf("删除后 List(dir/) = %v", got)
	}
	// 目录不能被当成文件删掉。
	if err := tg.Delete(ctx, "dir/sub"); err != nil && !strings.Contains(err.Error(), "目录") {
		t.Fatalf("删除目录路径返回了意外错误：%v", err)
	}
	if got := mustGet(t, tg, "dir/sub/c.bin"); !bytes.Equal(got, files["dir/sub/c.bin"]) {
		t.Fatal("Delete 目录路径不能删掉目录里的文件")
	}
	if _, err := tg.Stat(ctx, "dir/sub"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Stat 目录应视为文件不存在，实际 %v", err)
	}

	// 越界路径一律拒绝
	for _, p := range badPaths {
		if err := tg.Put(ctx, p, bytes.NewReader([]byte("x")), 1); err == nil {
			t.Fatalf("Put(%q) 应被拒绝", p)
		}
		if _, err := tg.Get(ctx, p); err == nil || errors.Is(err, ErrNotExist) {
			t.Fatalf("Get(%q) 应被拒绝，实际 %v", p, err)
		}
		if _, err := tg.Stat(ctx, p); err == nil || errors.Is(err, ErrNotExist) {
			t.Fatalf("Stat(%q) 应被拒绝，实际 %v", p, err)
		}
		if err := tg.Delete(ctx, p); err == nil {
			t.Fatalf("Delete(%q) 应被拒绝", p)
		}
	}
	for _, p := range []string{"../", "/", "a//", `a\b`} {
		if _, err := tg.List(ctx, p); err == nil {
			t.Fatalf("List(%q) 应被拒绝", p)
		}
	}
	if err := tg.Put(ctx, "~$evil.tmp", bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("Put 临时文件名应被拒绝")
	}

	// 测试连接
	if err := Probe(ctx, tg); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := listPaths(t, tg, "lemon-note-probe"); len(got) != 0 {
		t.Fatalf("Probe 后应删除测试文件，实际 %v", got)
	}
	if err := tg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
