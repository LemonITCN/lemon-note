package persist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func testCfg(t *testing.T, data, cache, host string) Config {
	t.Helper()
	return Config{DataDir: data, CacheDir: cache, HostID: host, Version: "test", SealInterval: time.Hour, SnapshotInterval: time.Hour}
}

func put(t *testing.T, in *Instance, k, v string) {
	t.Helper()
	_, err := in.Store.Write(context.Background(), "tester", func(tx *store.Tx) error {
		return tx.Put("settings", store.Row{"id": k, "value": v, "updated_at": time.Now().UnixMilli()})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, in *Instance, k string) string {
	t.Helper()
	var v string
	err := in.Store.QueryRow(context.Background(), "SELECT value FROM settings WHERE id = ?", k).Scan(&v)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", k, err)
	}
	return v
}

func TestLifecycleRebuildAndCrash(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data, cache := filepath.Join(root, "data"), filepath.Join(root, "cache")
	cfg := testCfg(t, data, cache, "host-a")
	if err := Initialize(ctx, cfg, store.SQLite); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(ctx, cfg, store.SQLite); err == nil {
		t.Fatal("重复初始化应报错")
	}
	in, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		put(t, in, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}
	put(t, in, "k1", "changed")
	if err := in.SealPending(ctx); err != nil {
		t.Fatal(err)
	}
	segs, _ := in.Dir.ListSegments()
	if len(segs) == 0 {
		t.Fatal("应当有日志段")
	}
	// 再写几条，不封存，模拟崩溃：直接关库、释放锁
	put(t, in, "crash1", "x")
	put(t, in, "crash2", "y")
	in.closed.Store(true)
	close(in.stopCh)
	in.wg.Wait()
	in.Store.Close()
	in.lock.Release()

	// 重开：外箱里的两条应被封存
	in, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if get(t, in, "crash2") != "y" {
		t.Fatal("崩溃前提交的数据丢失")
	}
	st := in.Status(ctx)
	if st.SealedSeq != st.Seq {
		t.Fatalf("重开后应全部封存 sealed=%d seq=%d", st.SealedSeq, st.Seq)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 删除缓存目录（换机器）：从快照 + 日志重建
	os.RemoveAll(cache)
	in, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if get(t, in, "k1") != "changed" || get(t, in, "k49") != "v49" || get(t, in, "crash1") != "x" {
		t.Fatal("重建后数据不一致")
	}
	// 写快照后旧日志应被清理，但仍可重建
	if err := in.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	put(t, in, "after-snap", "1")
	if err := in.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	put(t, in, "after-snap2", "2")
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	snaps, _ := in.Dir.ListSnapshots()
	if len(snaps) > 2 {
		t.Fatalf("快照应只保留两代，实际 %v", snaps)
	}
	os.RemoveAll(cache)
	in, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close(ctx)
	if get(t, in, "after-snap2") != "2" || get(t, in, "k3") != "v3" {
		t.Fatal("多次快照后重建数据不一致")
	}
	// 本机表不进快照
	var n int
	in.Store.QueryRow(ctx, "SELECT count(*) FROM _outbox").Scan(&n)
	if n != 0 {
		t.Fatalf("外箱应为空，实际 %d", n)
	}
}

func TestOwnedElsewhere(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	cfgA := testCfg(t, data, filepath.Join(root, "cacheA"), "host-a")
	cfgB := testCfg(t, data, filepath.Join(root, "cacheB"), "host-b")
	if err := Initialize(ctx, cfgA, store.SQLite); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, cfgA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, cfgB); err == nil {
		t.Fatal("另一台机器在用时应拒绝启动")
	}
	put(t, a, "x", "1")
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := Open(ctx, cfgB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	if get(t, b, "x") != "1" {
		t.Fatal("换机器后数据不一致")
	}
}

func TestCorruptSegmentDetected(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := testCfg(t, filepath.Join(root, "data"), filepath.Join(root, "cache"), "h")
	if err := Initialize(ctx, cfg, store.SQLite); err != nil {
		t.Fatal(err)
	}
	in, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		put(t, in, fmt.Sprintf("k%d", i), "v")
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	segs, _ := in.Dir.ListSegments()
	p := in.Dir.Path(filepath.FromSlash(segs[0].Name))
	b, _ := os.ReadFile(p)
	os.WriteFile(p, b[:len(b)/2], 0o644)
	if _, _, err := store.ReadSegmentFile(p); err == nil {
		t.Fatal("截断的日志段应校验失败")
	}
	os.RemoveAll(filepath.Join(root, "cache"))
	if _, err := Open(ctx, cfg); err == nil {
		t.Fatal("日志段损坏时重建应报错而不是静默丢数据")
	}
}

func TestTempNamesIgnored(t *testing.T) {
	d, _ := datadir.Open(t.TempDir())
	os.MkdirAll(d.JournalDir(), 0o755)
	os.WriteFile(filepath.Join(d.JournalDir(), datadir.TempPrefix+"abc.tmp"), []byte("x"), 0o644)
	segs, _ := d.ListSegments()
	if len(segs) != 0 {
		t.Fatal("临时文件不应被当作日志段")
	}
}
