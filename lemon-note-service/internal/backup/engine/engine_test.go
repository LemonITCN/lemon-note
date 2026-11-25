package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

var discard = slog.New(slog.DiscardHandler)

func ptr[T any](v T) *T { return &v }

func instCfg(data, cache string) persist.Config {
	return persist.Config{DataDir: data, CacheDir: cache, HostID: "host-" + ids.RandomHex(4), Version: "test", Logger: discard,
		SealInterval: time.Hour, SnapshotInterval: time.Hour}
}

// newInstance 初始化并打开一个 SQLite 实例。
func newInstance(t *testing.T) *persist.Instance {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	cfg := instCfg(filepath.Join(root, "data"), filepath.Join(root, "cache"))
	if err := persist.Initialize(ctx, cfg, store.SQLite); err != nil {
		t.Fatal(err)
	}
	inst, err := persist.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close(context.Background()) })
	return inst
}

func newEngine(t *testing.T, inst *persist.Instance, mod func(o *Options)) *Engine {
	t.Helper()
	key, err := inst.SecretKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypt.New(key)
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Inst: inst, Box: box, Log: discard}
	if mod != nil {
		mod(&o)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func putSetting(t *testing.T, inst *persist.Instance, k, v string) {
	t.Helper()
	_, err := inst.Store.Write(context.Background(), "tester", func(tx *store.Tx) error {
		return tx.Put("settings", store.Row{"id": k, "value": v, "updated_at": time.Now().UnixMilli()})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func getSetting(t *testing.T, inst *persist.Instance, k string) (string, bool) {
	t.Helper()
	var v string
	err := inst.Store.QueryRow(context.Background(), "SELECT value FROM settings WHERE id = ?", k).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// addBlob 写一个附件与引用它的 files 行，返回 sha256。
func addBlob(t *testing.T, inst *persist.Instance, data []byte) string {
	t.Helper()
	sha, size, err := inst.Dir.PutBlob(bytes.NewReader(data), filepath.Join(inst.CacheDir, "tmp"), 0)
	if err != nil {
		t.Fatal(err)
	}
	f := model.File{ID: ids.New(), SpaceID: ids.New(), SHA256: sha, Size: size, Mime: "application/octet-stream", Name: "a.bin",
		CreatedAt: time.Now().UnixMilli(), CreatedBy: ids.New()}
	if _, err := inst.Store.Write(context.Background(), "tester", func(tx *store.Tx) error { return tx.Put("files", f) }); err != nil {
		t.Fatal(err)
	}
	return sha
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// seed 写一批设置和附件，并让数据目录里同时有快照和之后的日志段。
func seed(t *testing.T, inst *persist.Instance, n int) (settings map[string]string, blobs map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	settings, blobs = map[string]string{}, map[string][]byte{}
	for i := 0; i < n; i++ {
		k, v := fmt.Sprintf("k%d-%s", i, ids.RandomHex(3)), strings.Repeat(fmt.Sprintf("v%d", i), 20)
		putSetting(t, inst, k, v)
		settings[k] = v
	}
	for _, size := range []int{10, 70 << 10, 200 << 10} {
		b := randBytes(size)
		blobs[addBlob(t, inst, b)] = b
	}
	if err := inst.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		k := fmt.Sprintf("after-snap-%d-%s", i, ids.RandomHex(3))
		putSetting(t, inst, k, "x")
		settings[k] = "x"
	}
	if err := inst.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	return settings, blobs
}

func localTarget(t *testing.T, e *Engine, name, mode string, enc *EncryptionInput, ret *Retention) (string, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": dir})
	v, err := e.CreateTarget(context.Background(), "tester", TargetInput{Name: ptr(name), Kind: ptr("local"), Config: cfg, Mode: ptr(mode),
		Schedule: &Schedule{Kind: ScheduleManual}, Retention: ret, Encryption: enc})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID, dir
}

// openRestored 用全新的缓存目录打开恢复出（或镜像出）的数据目录。
func openRestored(t *testing.T, dir string) *persist.Instance {
	t.Helper()
	inst, err := persist.Open(context.Background(), instCfg(dir, t.TempDir()))
	if err != nil {
		t.Fatalf("打开 %s 失败：%v", dir, err)
	}
	t.Cleanup(func() { inst.Close(context.Background()) })
	return inst
}

func checkData(t *testing.T, inst *persist.Instance, settings map[string]string, blobs map[string][]byte) {
	t.Helper()
	for k, v := range settings {
		got, ok := getSetting(t, inst, k)
		if !ok || got != v {
			t.Fatalf("设置 %s 不一致：%q / %q", k, got, v)
		}
	}
	for sha, data := range blobs {
		f, err := inst.Dir.OpenBlob(sha)
		if err != nil {
			t.Fatalf("附件 %s 缺失：%v", sha, err)
		}
		got, _ := io.ReadAll(f)
		f.Close()
		if !bytes.Equal(got, data) {
			t.Fatalf("附件 %s 内容不一致", sha)
		}
	}
}

// listFiles 列出目录下全部文件（相对路径，/ 分隔）→ 大小。
func listFiles(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		info, _ := d.Info()
		out[filepath.ToSlash(rel)] = info.Size()
		return nil
	})
	return out
}

func managedOnly(m map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for k, v := range m {
		if _, ok := mirrorManaged(k); ok {
			out[k] = v
		}
	}
	return out
}

// ---------------- 全量 ----------------

func TestFullBackupRestore(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 40)
	e := newEngine(t, inst, nil)
	id, dir := localTarget(t, e, "本地全量", ModeFull, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err, run.Log)
	}
	if run.Status != StatusSuccess || !pkgRe.MatchString(run.Artifact) {
		t.Fatalf("备份结果不对：%+v", run)
	}
	if _, err := os.Stat(filepath.Join(dir, run.Artifact)); err != nil {
		t.Fatal("目标上没有备份包")
	}
	if _, err := os.Stat(filepath.Join(dir, HeaderName)); err != nil {
		t.Fatal("目标上没有目标头")
	}
	// 备份之后的写入不应出现在恢复结果里。
	putSetting(t, inst, "after-backup", "1")
	list, err := e.ListTargetBackups(ctx, id, "")
	if err != nil || len(list) != 1 || list[0].Name != run.Artifact || !list[0].Current || list[0].Mode != ModeFull {
		t.Fatalf("列出备份不对：%+v %v", list, err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	res, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Lineage == inst.Lineage || res.PreviousLineage != inst.Lineage || res.InstanceID != inst.Info.InstanceID {
		t.Fatalf("恢复后应换新血统：%+v", res)
	}
	if res.Blobs != len(blobs) || len(res.MissingBlobs) != 0 {
		t.Fatalf("附件个数不对：%+v", res)
	}
	got := openRestored(t, dest)
	checkData(t, got, settings, blobs)
	if _, ok := getSetting(t, got, "after-backup"); ok {
		t.Fatal("备份之后的写入不应出现在恢复结果里")
	}
	if got.Lineage != res.Lineage {
		t.Fatal("恢复出的数据目录血统不对")
	}
	segs, _ := got.Dir.ListSegments()
	if len(segs) != 0 {
		t.Fatalf("恢复后旧血统的日志段应已并入新快照：%v", segs)
	}
	// 执行记录
	runs, err := e.Runs(ctx, id, 10)
	if err != nil || len(runs) != 2 || runs[0].Kind != RunRestore || runs[1].Kind != RunFull {
		t.Fatalf("执行记录不对：%+v %v", runs, err)
	}
	vr, err := e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "", RunOptions{})
	if err != nil || !vr.OK {
		t.Fatalf("校验应通过：%+v %v", vr, err)
	}
	// 恢复目录非空时拒绝
	if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err == nil {
		t.Fatal("恢复到非空目录应被拒绝")
	}
	// 恢复目录不能放在数据目录里
	if _, err := e.RestoreNow(ctx, id, run.Artifact, filepath.Join(inst.Dir.Root, "x"), "", RunOptions{}); err == nil {
		t.Fatal("恢复到数据目录里应被拒绝")
	}
}

// 旧缓存里序号更高的工作库不能把恢复出的数据覆盖掉：恢复换了血统，启动时会从数据目录重建。
func TestRestoredLineageIgnoresOldCache(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 5)
	e := newEngine(t, inst, nil)
	id, _ := localTarget(t, e, "t", ModeFull, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	putSetting(t, inst, "newer", "1")
	dest := filepath.Join(t.TempDir(), "r")
	if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Dir(inst.CacheDir)
	e.Close()
	if err := inst.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// 用原来的缓存目录（里面是序号更高的工作库）打开恢复出的目录。
	cfg := instCfg(dest, cacheRoot)
	got, err := persist.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close(ctx)
	checkData(t, got, settings, blobs)
	if _, ok := getSetting(t, got, "newer"); ok {
		t.Fatal("旧缓存里的工作库覆盖了恢复出的数据")
	}
}

func TestFullMultiVolumeAndCorruption(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 20)
	e := newEngine(t, inst, func(o *Options) { o.VolumeSize = 64 << 10 })
	id, dir := localTarget(t, e, "分卷", ModeFull, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(run.Artifact, ".lnbak")
	parts, _ := filepath.Glob(filepath.Join(dir, base+".lnbak.*"))
	if len(parts) < 3 {
		t.Fatalf("应当分成多卷，实际 %d 个前置分卷", len(parts))
	}
	for _, p := range parts {
		if st, _ := os.Stat(p); st.Size() != 64<<10 {
			t.Fatalf("分卷 %s 大小 %d", p, st.Size())
		}
	}
	dest := filepath.Join(t.TempDir(), "r")
	if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	checkData(t, openRestored(t, dest), settings, blobs)

	// 中间一卷内容损坏：校验与恢复都要失败，失败的恢复不留下文件。
	sort.Strings(parts)
	mid := parts[len(parts)/2]
	b, _ := os.ReadFile(mid)
	b[len(b)/2] ^= 0xff
	os.WriteFile(mid, b, 0o644)
	vr, err := e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "", RunOptions{})
	if err != nil || vr.OK {
		t.Fatalf("损坏的分卷应校验失败：%+v %v", vr, err)
	}
	dest2 := filepath.Join(t.TempDir(), "r2")
	if _, err := e.RestoreNow(ctx, id, run.Artifact, dest2, "", RunOptions{}); err == nil {
		t.Fatal("损坏的分卷应恢复失败")
	}
	if _, err := os.Stat(dest2); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("失败的恢复应清掉新建的目录")
	}
	// 缺一卷
	os.Remove(mid)
	vr, err = e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "", RunOptions{})
	if err != nil || vr.OK {
		t.Fatalf("缺分卷应校验失败：%+v %v", vr, err)
	}
}

func TestTruncatedPackage(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			ctx := context.Background()
			inst := newInstance(t)
			seed(t, inst, 10)
			e := newEngine(t, inst, nil)
			var enc *EncryptionInput
			if encrypted {
				enc = &EncryptionInput{Enabled: true, Password: "correct horse battery"}
			}
			id, dir := localTarget(t, e, "t", ModeFull, enc, nil)
			run, err := e.RunBackup(ctx, id, RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, run.Artifact)
			orig, _ := os.ReadFile(p)
			for _, cut := range []int{1, 100, len(orig) / 3} {
				os.WriteFile(p, orig[:len(orig)-cut], 0o644)
				vr, err := e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "", RunOptions{})
				if err != nil || vr.OK {
					t.Fatalf("截掉 %d 字节后校验应失败：%+v %v", cut, vr, err)
				}
				dest := filepath.Join(t.TempDir(), "r")
				if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err == nil {
					t.Fatalf("截掉 %d 字节后恢复应失败", cut)
				}
			}
			// 尾部追加数据也要发现
			os.WriteFile(p, append(append([]byte{}, orig...), 0, 1, 2, 3), 0o644)
			vr, err := e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "", RunOptions{})
			if err != nil || vr.OK {
				t.Fatalf("追加数据后校验应失败：%+v %v", vr, err)
			}
			os.WriteFile(p, orig, 0o644)
			vr, err = e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "", RunOptions{})
			if err != nil || !vr.OK {
				t.Fatalf("还原后校验应通过：%+v %v", vr, err)
			}
		})
	}
}

// ---------------- 镜像 ----------------

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sourceManaged(t *testing.T, inst *persist.Instance) map[string]int64 {
	return managedOnly(listFiles(t, inst.Dir.Root))
}

func TestMirror(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 30)
	e := newEngine(t, inst, nil)
	id, dir := localTarget(t, e, "镜像", ModeMirror, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mdir := filepath.Join(dir, run.Artifact)
	if run.Artifact != mirrorDir(instanceShort(inst.Info.InstanceID)) {
		t.Fatalf("镜像目录名不对：%s", run.Artifact)
	}
	if !equalMaps(managedOnly(listFiles(t, mdir)), sourceManaged(t, inst)) {
		t.Fatal("镜像与数据目录的文件不一致")
	}
	if _, err := os.Stat(filepath.Join(mdir, "locks")); err == nil {
		t.Fatal("镜像不应包含 locks/")
	}
	// 镜像副本本身就能当数据目录打开。
	c1 := filepath.Join(t.TempDir(), "copy1")
	copyDir(t, mdir, c1)
	checkData(t, openRestored(t, c1), settings, blobs)

	// 没有业务变化时，只会传上一次执行记录写出的新日志段，附件、快照、MANIFEST 都不重传。
	snap0 := statAll(t, mdir)
	run2, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	uploaded := reuploaded(t, mdir, snap0)
	for _, name := range uploaded {
		if !strings.HasPrefix(name, "journal/") {
			t.Fatalf("没有变化时不应重传 %s", name)
		}
	}
	if int(run2.Files) != len(uploaded) {
		t.Fatalf("上传个数 %d 与实际变化的文件 %v 不一致", run2.Files, uploaded)
	}
	if !equalMaps(managedOnly(listFiles(t, mdir)), sourceManaged(t, inst)) {
		t.Fatal("镜像与数据目录的文件不一致")
	}

	// 改数据：新设置、新附件、连写两个快照（数据目录会删掉最老一代快照与日志段）。
	before := managedOnly(listFiles(t, mdir))
	for i := 0; i < 10; i++ {
		k := fmt.Sprintf("second-%d", i)
		putSetting(t, inst, k, "y")
		settings[k] = "y"
	}
	nb := randBytes(90 << 10)
	nbSHA := addBlob(t, inst, nb)
	blobs[nbSHA] = nb
	if err := inst.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	putSetting(t, inst, "third", "z")
	settings["third"] = "z"
	if err := inst.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	putSetting(t, inst, "fourth", "w")
	settings["fourth"] = "w"
	if err := inst.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	src := sourceManaged(t, inst)
	var stale int
	for k := range before {
		if _, ok := src[k]; !ok {
			stale++
		}
	}
	if stale == 0 {
		t.Fatal("测试前提：源端应当删掉了一些旧文件")
	}
	snap1 := statAll(t, mdir)
	run3, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	uploaded = reuploaded(t, mdir, snap1)
	for _, name := range uploaded {
		if _, existed := before[name]; existed && name != "MANIFEST" {
			t.Fatalf("已存在且未变化的文件 %s 被重传了", name)
		}
	}
	if !contains(uploaded, "MANIFEST") || !contains(uploaded, datadir.BlobRel(nbSHA)) {
		t.Fatalf("应上传新附件与 MANIFEST：%v", uploaded)
	}
	if int(run3.Files) != len(uploaded) {
		t.Fatalf("上传个数 %d 与实际变化的文件 %v 不一致\n%s", run3.Files, uploaded, run3.Log)
	}
	after := managedOnly(listFiles(t, mdir))
	if !equalMaps(after, sourceManaged(t, inst)) {
		t.Fatalf("镜像应删掉源端已不存在的文件：\n镜像 %v\n源端 %v", keys(after), keys(sourceManaged(t, inst)))
	}
	for k := range before {
		if _, ok := src[k]; !ok {
			if _, still := after[k]; still {
				t.Fatalf("源端已删除的 %s 仍留在镜像里", k)
			}
		}
	}
	c2 := filepath.Join(t.TempDir(), "copy2")
	copyDir(t, mdir, c2)
	checkData(t, openRestored(t, c2), settings, blobs)

	vr, err := e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{FullHash: true}, "", RunOptions{})
	if err != nil || !vr.OK || vr.Hashed != len(blobs) {
		t.Fatalf("镜像校验应通过：%+v %v", vr, err)
	}
	dest := filepath.Join(t.TempDir(), "r")
	res, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := openRestored(t, dest)
	checkData(t, got, settings, blobs)
	if got.Lineage == inst.Lineage || res.Lineage != got.Lineage {
		t.Fatal("从镜像恢复后应换新血统")
	}
	list, err := e.ListTargetBackups(ctx, id, "")
	if err != nil || len(list) != 1 || list[0].Mode != ModeMirror || list[0].InstanceID != inst.Info.InstanceID {
		t.Fatalf("列出镜像不对：%+v %v", list, err)
	}

	// 镜像里丢了一个附件：校验发现。
	for k := range after {
		if strings.HasPrefix(k, "blobs/") {
			os.Remove(filepath.Join(mdir, filepath.FromSlash(k)))
			break
		}
	}
	vr, err = e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "", RunOptions{})
	if err != nil || vr.OK {
		t.Fatalf("镜像缺附件时校验应失败：%+v %v", vr, err)
	}
	// 下一次同步会补上
	if _, err := e.RunBackup(ctx, id, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	vr, err = e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{FullHash: true}, "", RunOptions{})
	if err != nil || !vr.OK {
		t.Fatalf("补传后校验应通过：%+v %v", vr, err)
	}
}

// statAll 记下目录里每个文件的 FileInfo，用来判断之后有没有被重写（本地目标写文件是临时文件 + 改名，重写后不再是同一个文件）。
func statAll(t *testing.T, root string) map[string]os.FileInfo {
	t.Helper()
	out := map[string]os.FileInfo{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		info, _ := os.Stat(p)
		out[filepath.ToSlash(rel)] = info
		return nil
	})
	return out
}

// reuploaded 返回相对 before 新出现或被重写的文件。
func reuploaded(t *testing.T, root string, before map[string]os.FileInfo) []string {
	t.Helper()
	var out []string
	for name, info := range statAll(t, root) {
		if old, ok := before[name]; !ok || !os.SameFile(old, info) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func equalMaps(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func keys(m map[string]int64) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------- 加密 ----------------

func TestEncryption(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 15)
	e := newEngine(t, inst, nil)
	const pw = "correct horse battery"
	for _, mode := range []string{ModeFull, ModeMirror} {
		t.Run(mode, func(t *testing.T) {
			id, dir := localTarget(t, e, "加密-"+mode, mode, &EncryptionInput{Enabled: true, Password: pw}, nil)
			v, _ := e.GetTarget(ctx, id)
			if !v.Encryption.Enabled || !v.Encryption.HasPassword {
				t.Fatalf("加密设置不对：%+v", v.Encryption)
			}
			// 口令不以任何形式出现在数据库里
			var row model.BackupTarget
			inst.Store.Get(ctx, &row, "SELECT * FROM backup_targets WHERE id = ?", id)
			if strings.Contains(row.Encryption, pw) || strings.Contains(row.Config, dir) {
				t.Fatal("口令或配置以明文存进了数据库")
			}
			run, err := e.RunBackup(ctx, id, RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			hdr, _ := os.ReadFile(filepath.Join(dir, HeaderName))
			if !bytes.Contains(hdr, []byte(`"key_check"`)) || bytes.Contains(hdr, []byte(pw)) {
				t.Fatalf("目标头不对：%s", hdr)
			}
			// 目标上的文件都是密文
			var sample string
			if mode == ModeFull {
				sample = filepath.Join(dir, run.Artifact)
			} else {
				sample = filepath.Join(dir, run.Artifact, "MANIFEST")
				for sha, data := range blobs {
					enc, _ := os.ReadFile(filepath.Join(dir, run.Artifact, filepath.FromSlash(datadir.BlobRel(sha))))
					if bytes.Contains(enc, data[:min(len(data), 32)]) || !bytes.HasPrefix(enc, []byte("LNBK")) {
						t.Fatal("镜像里的附件没有加密")
					}
				}
			}
			b, _ := os.ReadFile(sample)
			if !bytes.HasPrefix(b, []byte("LNBK")) {
				t.Fatalf("%s 没有加密", sample)
			}
			dest := filepath.Join(t.TempDir(), "r")
			if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err != nil {
				t.Fatal(err)
			}
			checkData(t, openRestored(t, dest), settings, blobs)

			// 口令错误
			_, err = e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{}, "wrong password!", RunOptions{})
			if !errors.Is(err, ErrWrongPassword) {
				t.Fatalf("口令错误时应返回 ErrWrongPassword：%v", err)
			}
			dest2 := filepath.Join(t.TempDir(), "r2")
			if _, err := e.RestoreNow(ctx, id, run.Artifact, dest2, "wrong password!", RunOptions{}); !errors.Is(err, ErrWrongPassword) {
				t.Fatalf("口令错误时恢复应失败：%v", err)
			}
			if _, err := os.Stat(dest2); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("口令错误时不应创建恢复目录")
			}
			// 不给口令直接读
			lt, _ := target.Open("local", []byte(`{"path":`+jsonStr(dir)+`}`), target.Deps{})
			if _, _, err := OpenKey(ctx, lt, ""); !errors.Is(err, ErrNeedPassword) {
				t.Fatalf("没有口令时应返回 ErrNeedPassword：%v", err)
			}
			if mode == ModeFull {
				if _, err := Restore(ctx, lt, run.Artifact, filepath.Join(t.TempDir(), "r3"), nil, RestoreOptions{}, nil); !errors.Is(err, ErrNeedPassword) {
					t.Fatalf("没有密钥时恢复加密包应返回 ErrNeedPassword：%v", err)
				}
			}
			// 目标上已有备份时换口令：备份被拒绝，旧备份仍能用旧口令恢复。
			if _, err := e.UpdateTarget(ctx, "tester", id, TargetInput{Encryption: &EncryptionInput{Enabled: true, Password: "another password"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.RunBackup(ctx, id, RunOptions{}); err == nil || !strings.Contains(err.Error(), "口令不一致") {
				t.Fatalf("换口令后备份应被拒绝：%v", err)
			}
			tr, _ := e.TestTarget(ctx, id)
			if tr == nil || !tr.OK || tr.Warning == "" {
				t.Fatalf("测试连接应提示口令不一致：%+v", tr)
			}
			// 关掉加密同样被拒绝
			if _, err := e.UpdateTarget(ctx, "tester", id, TargetInput{Encryption: &EncryptionInput{Enabled: false}}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.RunBackup(ctx, id, RunOptions{}); err == nil {
				t.Fatal("目标上有加密备份时关闭加密应被拒绝")
			}
			dest4 := filepath.Join(t.TempDir(), "r4")
			if _, err := e.RestoreNow(ctx, id, run.Artifact, dest4, pw, RunOptions{}); err != nil {
				t.Fatalf("旧口令应能恢复：%v", err)
			}
		})
	}
	// 未加密目标改成加密：目标上已有备份时拒绝；空目标可以直接开启。
	id, dir := localTarget(t, e, "明文", ModeFull, nil, nil)
	if _, err := e.RunBackup(ctx, id, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	e.UpdateTarget(ctx, "tester", id, TargetInput{Encryption: &EncryptionInput{Enabled: true, Password: pw}})
	if _, err := e.RunBackup(ctx, id, RunOptions{}); err == nil {
		t.Fatal("目标上有未加密备份时开启加密应被拒绝")
	}
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if en.Name() != HeaderName {
			os.Remove(filepath.Join(dir, en.Name()))
		}
	}
	if _, err := e.RunBackup(ctx, id, RunOptions{}); err != nil {
		t.Fatalf("空目标开启加密应可以：%v", err)
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// ---------------- 保留策略 ----------------

func TestSelectKeep(t *testing.T) {
	loc := time.UTC
	start := time.Date(2026, 1, 1, 3, 0, 0, 0, loc)
	var pkgs []*pkgFiles
	// 120 天，每天两个备份
	for d := 0; d < 120; d++ {
		for _, h := range []int{3, 15} {
			ts := start.AddDate(0, 0, d).Add(time.Duration(h-3) * time.Hour)
			pkgs = append(pkgs, &pkgFiles{Base: packageBase("abcdefgh", ts), Time: ts})
		}
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Time.After(pkgs[j].Time) })
	keep := selectKeep(pkgs, Retention{Kind: RetainLast, KeepLast: 5}, loc)
	if len(keep) != 5 || !keep[pkgs[0].Base] || !keep[pkgs[4].Base] || keep[pkgs[5].Base] {
		t.Fatalf("保留最近 5 个不对：%v", keep)
	}
	keep = selectKeep(pkgs, Retention{Kind: RetainTiered, Daily: 7, Weekly: 4, Monthly: 3}, loc)
	// 每天最新一个（15 点那个）
	for i := 0; i < 14; i += 2 {
		if !keep[pkgs[i].Base] {
			t.Fatalf("应保留最近 7 天每天最新的：%s", pkgs[i].Base)
		}
		if keep[pkgs[i+1].Base] && i > 0 {
			t.Fatalf("同一天只保留最新的一个：%s", pkgs[i+1].Base)
		}
	}
	months := map[string]bool{}
	for b := range keep {
		for _, p := range pkgs {
			if p.Base == b {
				months[p.Time.Format("2006-01")] = true
			}
		}
	}
	if len(months) != 3 {
		t.Fatalf("应覆盖 3 个月：%v", months)
	}
	if len(keep) > 7+4+3 || len(keep) < 7 {
		t.Fatalf("分层保留个数不对：%d", len(keep))
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestRetentionApplied(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	seed(t, inst, 5)
	clock := &fakeClock{t: time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)}
	e := newEngine(t, inst, func(o *Options) { o.Now = clock.Now })
	id, dir := localTarget(t, e, "保留", ModeFull, nil, &Retention{Kind: RetainLast, KeepLast: 2})
	id8 := instanceShort(inst.Info.InstanceID)
	// 上次中断留下的残缺分卷与其他实例的包都不能误删其他实例的
	orphan := partName(packageBase(id8, clock.Now().Add(-48*time.Hour)), 1)
	os.WriteFile(filepath.Join(dir, orphan), []byte("x"), 0o644)
	other := tailName(packageBase("zzzzzzzz", clock.Now().Add(-72*time.Hour)))
	os.WriteFile(filepath.Join(dir, other), []byte("x"), 0o644)
	var names []string
	for i := 0; i < 4; i++ {
		putSetting(t, inst, fmt.Sprintf("r%d", i), "v")
		run, err := e.RunBackup(ctx, id, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, run.Artifact)
		clock.Add(time.Hour)
	}
	files := listFiles(t, dir)
	for i, n := range names {
		_, ok := files[n]
		if (i >= 2) != ok {
			t.Fatalf("保留最近 2 个：%s 存在=%v\n%v", n, ok, keys(files))
		}
	}
	if _, ok := files[orphan]; ok {
		t.Fatal("残缺分卷应被清理")
	}
	if _, ok := files[other]; !ok {
		t.Fatal("其他实例的备份包不能被删")
	}
}

// ---------------- 并发 ----------------

// gateTarget 让匹配的 Put 停在闸门处，直到放行。
type gateTarget struct {
	target.Target
	match   string
	entered chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (g *gateTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	if strings.Contains(p, g.match) {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.Target.Put(ctx, p, r, size)
}

func gated(match string) (func(o *Options), chan struct{}, chan struct{}) {
	entered, release := make(chan struct{}), make(chan struct{})
	once := &sync.Once{}
	return func(o *Options) {
		o.OpenTarget = func(kind string, cfg []byte, deps target.Deps) (target.Target, error) {
			t, err := target.Open(kind, cfg, deps)
			if err != nil {
				return nil, err
			}
			return &gateTarget{Target: t, match: match, entered: entered, release: release, once: once}, nil
		}
	}, entered, release
}

func waitCh(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatal("等待超时")
	}
}

func TestWritesDuringBackupKeepConsistency(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 20)
	mod, entered, release := gated(".lnbak")
	e := newEngine(t, inst, func(o *Options) { mod(o); o.VolumeSize = 32 << 10 })
	id, _ := localTarget(t, e, "并发写", ModeFull, nil, nil)
	runID, err := e.StartBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitCh(t, entered) // 已经开始上传第一卷，文件集合已经确定
	man, _ := inst.Dir.ReadManifest()
	held := []string{man.Snapshot}
	segs, _ := inst.Dir.ListSegments()
	for _, s := range segs {
		held = append(held, s.Name)
	}
	// 备份进行中继续写入并连写几个快照：没有租约时旧快照与日志段会被删掉。
	for i := 0; i < 3; i++ {
		for j := 0; j < 10; j++ {
			putSetting(t, inst, fmt.Sprintf("during-%d-%d", i, j), "d")
		}
		if err := inst.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range held {
		if _, err := os.Stat(inst.Dir.Path(filepath.FromSlash(rel))); err != nil {
			t.Fatalf("备份租约期间 %s 被删除了", rel)
		}
	}
	close(release)
	e.Wait(runID)
	rv, err := e.Run(ctx, runID)
	if err != nil || rv.Status != StatusSuccess {
		t.Fatalf("备份应成功：%+v %v", rv, err)
	}
	dest := filepath.Join(t.TempDir(), "r")
	if _, err := e.RestoreNow(ctx, id, rv.Artifact, dest, "", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	got := openRestored(t, dest)
	checkData(t, got, settings, blobs)
	if _, ok := getSetting(t, got, "during-0-0"); ok {
		t.Fatal("开始打包之后的写入不应出现在这次备份里")
	}
	// 租约释放后清理恢复正常
	putSetting(t, inst, "post", "1")
	if err := inst.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	snaps, _ := inst.Dir.ListSnapshots()
	if len(snaps) > 2 {
		t.Fatalf("租约释放后旧快照应被清理：%v", snaps)
	}
}

func TestConcurrentRunRejected(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	seed(t, inst, 5)
	mod, entered, release := gated(".lnbak")
	e := newEngine(t, inst, mod)
	id, _ := localTarget(t, e, "互斥", ModeFull, nil, nil)
	runID, err := e.StartBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitCh(t, entered)
	if _, err := e.StartBackup(ctx, id, RunOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("同一目标并发执行应被拒绝：%v", err)
	}
	if _, err := e.RunBackup(ctx, id, RunOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("同一目标并发执行应被拒绝：%v", err)
	}
	if _, err := e.VerifyNow(ctx, id, "x.lnbak", VerifyOptions{}, "", RunOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("备份进行中校验应被拒绝：%v", err)
	}
	if err := e.DeleteTarget(ctx, "tester", id); err == nil {
		t.Fatal("进行中的目标不能删除")
	}
	v, _ := e.GetTarget(ctx, id)
	if v.Running == nil || v.Running.ID != runID {
		t.Fatalf("目标应显示进行中的操作：%+v", v.Running)
	}
	// 另一个目标不受影响
	id2, _ := localTarget(t, e, "另一个", ModeMirror, nil, nil)
	if _, err := e.RunBackup(ctx, id2, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	e.Wait(runID)
	if _, err := e.RunBackup(ctx, id, RunOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelCleansUp(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	seed(t, inst, 5)
	mod, entered, _ := gated(".lnbak.0002")
	e := newEngine(t, inst, func(o *Options) { mod(o); o.VolumeSize = 32 << 10 })
	id, dir := localTarget(t, e, "取消", ModeFull, nil, nil)
	runID, err := e.StartBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitCh(t, entered)
	if !e.Cancel(runID) {
		t.Fatal("应能取消")
	}
	e.Wait(runID)
	rv, _ := e.Run(ctx, runID)
	if rv.Status != StatusCanceled {
		t.Fatalf("状态应为已取消：%s %s", rv.Status, rv.Error)
	}
	for name := range listFiles(t, dir) {
		if strings.Contains(name, ".lnbak") {
			t.Fatalf("取消后不应留下分卷：%s", name)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(inst.CacheDir, "tmp", "backup"))
	if len(entries) != 0 {
		t.Fatalf("取消后不应留下临时文件：%v", entries)
	}
}

// ---------------- 目标管理 ----------------

func TestTargetMasking(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	e := newEngine(t, inst, nil)
	cfg := `{"url":"https://dav.example.com/dav/","username":"u","password":"s3cret-pass","prefix":"ln"}`
	v, err := e.CreateTarget(ctx, "tester", TargetInput{Name: ptr("坚果云"), Kind: ptr("webdav"), Config: json.RawMessage(cfg),
		Mode: ptr(ModeMirror), Schedule: &Schedule{Kind: ScheduleWeekly, At: "2:30", Weekdays: []int{1, 3, 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Config["password"] != Masked || v.Config["username"] != "u" {
		t.Fatalf("凭据应打码：%v", v.Config)
	}
	if v.Schedule.At != "02:30" || len(v.Schedule.Weekdays) != 2 || v.NextRunAt == nil {
		t.Fatalf("计划没有规范化：%+v", v.Schedule)
	}
	raw, _ := json.Marshal(v)
	if bytes.Contains(raw, []byte("s3cret-pass")) {
		t.Fatal("接口返回里出现了明文口令")
	}
	var row model.BackupTarget
	inst.Store.Get(ctx, &row, "SELECT * FROM backup_targets WHERE id = ?", v.ID)
	if strings.Contains(row.Config, "s3cret-pass") || !strings.HasPrefix(row.Config, "enc:v1:") {
		t.Fatal("配置应加密存储")
	}
	// 打码值表示不改
	upd := `{"url":"https://dav.example.com/dav2/","username":"u2","password":"` + Masked + `","prefix":"ln"}`
	if _, err := e.UpdateTarget(ctx, "tester", v.ID, TargetInput{Config: json.RawMessage(upd)}); err != nil {
		t.Fatal(err)
	}
	spec, _ := e.loadSpec(ctx, v.ID)
	var c map[string]string
	json.Unmarshal(spec.config, &c)
	if c["password"] != "s3cret-pass" || c["url"] != "https://dav.example.com/dav2/" || c["username"] != "u2" {
		t.Fatalf("打码字段应保留旧值：%v", c)
	}
	upd = `{"url":"https://dav.example.com/dav2/","username":"u2","password":"new-pass","prefix":"ln"}`
	e.UpdateTarget(ctx, "tester", v.ID, TargetInput{Config: json.RawMessage(upd)})
	spec, _ = e.loadSpec(ctx, v.ID)
	json.Unmarshal(spec.config, &c)
	if c["password"] != "new-pass" {
		t.Fatal("新口令没有保存")
	}
	// 重名、非法配置、备份目录放进数据目录
	if _, err := e.CreateTarget(ctx, "tester", TargetInput{Name: ptr("坚果云"), Kind: ptr("local"), Config: json.RawMessage(`{"path":"/tmp"}`)}); err == nil {
		t.Fatal("重名应被拒绝")
	}
	if _, err := e.CreateTarget(ctx, "tester", TargetInput{Name: ptr("x"), Kind: ptr("local"), Config: json.RawMessage(`{"path":"rel"}`)}); err == nil {
		t.Fatal("相对路径应被拒绝")
	}
	if _, err := e.CreateTarget(ctx, "tester", TargetInput{Name: ptr("y"), Kind: ptr("local"), Config: json.RawMessage(`{"path":` + jsonStr(filepath.Join(inst.Dir.Root, "bk")) + `}`)}); err == nil {
		t.Fatal("备份目录放在数据目录里应被拒绝")
	}
	if _, err := e.CreateTarget(ctx, "tester", TargetInput{Name: ptr("z"), Kind: ptr("local"), Config: json.RawMessage(`{"path":"/tmp"}`),
		Encryption: &EncryptionInput{Enabled: true, Password: "short"}}); err == nil {
		t.Fatal("过短的口令应被拒绝")
	}
	// 百度网盘未授权也能先保存；令牌与 SecretKey 打码
	bv, err := e.CreateTarget(ctx, "tester", TargetInput{Name: ptr("百度"), Kind: ptr("baidu"),
		Config: json.RawMessage(`{"app_key":"ak","secret_key":"sk-secret","app_name":"lemon"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if bv.Config["secret_key"] != Masked || bv.Config["app_key"] != "ak" {
		t.Fatalf("百度网盘配置打码不对：%v", bv.Config)
	}
	if id, err := e.FindTarget(ctx, "百度"); err != nil || id != bv.ID {
		t.Fatalf("按名称查找目标失败：%v", err)
	}
	if err := e.DeleteTarget(ctx, "tester", bv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.GetTarget(ctx, bv.ID); !errors.Is(err, ErrTargetNotFound) {
		t.Fatal("删除后应查不到")
	}
}

// ---------------- 计划 ----------------

func TestScheduleSlots(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	daily := Schedule{Kind: ScheduleDaily, At: "03:00"}
	if got := daily.Last(at("2026-11-25 02:59"), loc); !got.Equal(at("2026-11-24 03:00")) {
		t.Fatalf("daily last = %v", got)
	}
	if got := daily.Last(at("2026-11-25 03:00"), loc); !got.Equal(at("2026-11-25 03:00")) {
		t.Fatalf("daily last = %v", got)
	}
	if got := daily.Next(at("2026-11-25 03:00"), loc); !got.Equal(at("2026-11-26 03:00")) {
		t.Fatalf("daily next = %v", got)
	}
	// 2026-11-25 是周三
	weekly := Schedule{Kind: ScheduleWeekly, At: "22:30", Weekdays: []int{1, 5}}
	if got := weekly.Last(at("2026-11-25 12:00"), loc); !got.Equal(at("2026-11-23 22:30")) {
		t.Fatalf("weekly last = %v", got)
	}
	if got := weekly.Next(at("2026-11-25 12:00"), loc); !got.Equal(at("2026-11-27 22:30")) {
		t.Fatalf("weekly next = %v", got)
	}
	hourly := Schedule{Kind: ScheduleHourly, EveryHours: 6, At: "00:15"}
	if got := hourly.Last(at("2026-11-25 13:00"), loc); !got.Equal(at("2026-11-25 12:15")) {
		t.Fatalf("hourly last = %v", got)
	}
	if got := hourly.Next(at("2026-11-25 13:00"), loc); !got.Equal(at("2026-11-25 18:15")) {
		t.Fatalf("hourly next = %v", got)
	}
	if !(Schedule{Kind: ScheduleManual}).Last(at("2026-11-25 13:00"), loc).IsZero() {
		t.Fatal("手动计划没有时间点")
	}
	bad := []Schedule{{Kind: ScheduleHourly, EveryHours: 0}, {Kind: ScheduleWeekly, At: "03:00"}, {Kind: ScheduleDaily, At: "25:00"}, {Kind: "yearly"}}
	for _, s := range bad {
		if err := s.Normalize(); err == nil {
			t.Fatalf("非法计划应报错：%+v", s)
		}
	}
}

func TestTickRunsDueOnlyOnce(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	seed(t, inst, 3)
	loc := time.UTC
	clock := &fakeClock{t: time.Date(2026, 11, 20, 10, 0, 0, 0, loc)}
	e := newEngine(t, inst, func(o *Options) { o.Now = clock.Now; o.Location = loc })
	id, dir := localTarget(t, e, "计划", ModeFull, nil, nil)
	if _, err := e.UpdateTarget(ctx, "tester", id, TargetInput{Schedule: &Schedule{Kind: ScheduleDaily, At: "03:00"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle := func() {
		for i := 0; i < 600; i++ {
			if e.busyOp(id) == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("等待备份结束超时")
	}
	count := func() int {
		n := 0
		for name := range listFiles(t, dir) {
			if strings.HasSuffix(name, ".lnbak") {
				n++
			}
		}
		return n
	}
	// 新建的目标：今天 3 点已过但在创建之前，等到明天 3 点。
	e.Tick(ctx)
	waitIdle()
	if count() != 0 {
		t.Fatal("新建目标不应立即执行")
	}
	// 服务停了 5 天：只补跑一次
	clock.Add(5 * 24 * time.Hour)
	e.Tick(ctx)
	waitIdle()
	e.Tick(ctx)
	waitIdle()
	if count() != 1 {
		t.Fatalf("错过的计划只应补跑一次，实际 %d 次", count())
	}
	clock.Add(time.Hour)
	e.Tick(ctx)
	waitIdle()
	if count() != 1 {
		t.Fatal("同一个计划时间点不应重复执行")
	}
	clock.Add(24 * time.Hour)
	e.Tick(ctx)
	waitIdle()
	if count() != 2 {
		t.Fatalf("到下一个时间点应再执行，实际 %d 次", count())
	}
	// 关闭的目标不执行
	e.UpdateTarget(ctx, "tester", id, TargetInput{Enabled: ptr(false)})
	clock.Add(24 * time.Hour)
	e.Tick(ctx)
	waitIdle()
	if count() != 2 {
		t.Fatal("停用的目标不应执行")
	}
}

func TestFailedRunRetry(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	loc := time.UTC
	clock := &fakeClock{t: time.Date(2026, 11, 20, 10, 0, 0, 0, loc)}
	e := newEngine(t, inst, func(o *Options) { o.Now = clock.Now; o.Location = loc })
	missing := filepath.Join(t.TempDir(), "gone")
	os.MkdirAll(missing, 0o755)
	cfg, _ := json.Marshal(map[string]string{"path": missing})
	v, err := e.CreateTarget(ctx, "tester", TargetInput{Name: ptr("会失败"), Kind: ptr("local"), Config: cfg,
		Schedule: &Schedule{Kind: ScheduleDaily, At: "03:00"}})
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(missing) // NAS 掉线
	clock.Add(24 * time.Hour)
	spec, _ := e.loadSpec(ctx, v.ID)
	if !e.due(ctx, spec, clock.Now()) {
		t.Fatal("到点应执行")
	}
	if _, err := e.RunBackup(ctx, v.ID, RunOptions{Trigger: triggerSchedule}); err == nil {
		t.Fatal("目录不存在应失败")
	}
	spec, _ = e.loadSpec(ctx, v.ID)
	if spec.row.LastStatus != StatusFailed || e.due(ctx, spec, clock.Now()) {
		t.Fatal("刚失败时不应立即重试")
	}
	clock.Add(31 * time.Minute)
	if !e.due(ctx, spec, clock.Now()) {
		t.Fatal("失败 30 分钟后应重试")
	}
}

// 恢复出的目录可以直接交给 sha256 工具核对：附件文件名就是内容哈希。
func TestRestoredBlobsHashMatch(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	_, blobs := seed(t, inst, 3)
	e := newEngine(t, inst, nil)
	id, _ := localTarget(t, e, "t", ModeFull, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "r")
	if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	d, _ := datadir.Open(dest)
	n := 0
	d.ListBlobs(func(sha string, size int64) error {
		f, _ := d.OpenBlob(sha)
		h := sha256.New()
		io.Copy(h, f)
		f.Close()
		if hex.EncodeToString(h.Sum(nil)) != sha {
			t.Fatalf("附件 %s 内容不对", sha)
		}
		n++
		return nil
	})
	if n != len(blobs) {
		t.Fatalf("附件个数 %d / %d", n, len(blobs))
	}
}

// 恢复后换了血统的数据继续镜像到同一个目标：镜像整体替换快照与日志段，替换后仍然完整可用。
func TestMirrorAfterRestoreLineageSwitch(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 10)
	e := newEngine(t, inst, nil)
	id, dir := localTarget(t, e, "镜像", ModeMirror, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	// 换到恢复出的数据目录上运行（同一个实例 ID、新的血统），继续镜像到同一个目标。
	inst2 := openRestored(t, dest)
	putSetting(t, inst2, "after-restore", "1")
	settings["after-restore"] = "1"
	e2 := newEngine(t, inst2, nil)
	// 备份里本来就有这个目标的配置（用恢复出的 config/secret.key 解密）。
	tid, err := e2.FindTarget(ctx, "镜像")
	if err != nil {
		t.Fatal(err)
	}
	v, err := e2.GetTarget(ctx, tid)
	if err != nil || v.Config["path"] != dir {
		t.Fatalf("恢复出的目标配置不对：%+v %v", v, err)
	}
	run2, err := e2.RunBackup(ctx, v.ID, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run2.Status != StatusWarning || !strings.Contains(run2.Log, "血统不同") {
		t.Fatalf("应提示血统不同并整体替换：%s\n%s", run2.Status, run2.Log)
	}
	mdir := filepath.Join(dir, run2.Artifact)
	if !equalMaps(managedOnly(listFiles(t, mdir)), sourceManaged(t, inst2)) {
		t.Fatal("替换后镜像应与新数据目录一致")
	}
	vr, err := e2.VerifyNow(ctx, v.ID, run2.Artifact, VerifyOptions{FullHash: true}, "", RunOptions{})
	if err != nil || !vr.OK {
		t.Fatalf("替换后校验应通过：%+v %v", vr, err)
	}
	c := filepath.Join(t.TempDir(), "copy")
	copyDir(t, mdir, c)
	checkData(t, openRestored(t, c), settings, blobs)
}

// 镜像里的 MANIFEST 丢了（例如上次替换到一半中断）：同名同大小的日志段不能再相信，要重新上传。
func TestMirrorWithoutManifestReuploadsJournal(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 10)
	e := newEngine(t, inst, nil)
	id, dir := localTarget(t, e, "镜像", ModeMirror, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mdir := filepath.Join(dir, run.Artifact)
	os.Remove(filepath.Join(mdir, "MANIFEST"))
	segs, _ := filepath.Glob(filepath.Join(mdir, "journal", "*.lnj"))
	if len(segs) == 0 {
		t.Fatal("测试前提：镜像里应有日志段")
	}
	for _, s := range segs {
		st, _ := os.Stat(s)
		os.WriteFile(s, randBytes(int(st.Size())), 0o644) // 大小不变、内容是别的
	}
	if _, err := e.RunBackup(ctx, id, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	vr, err := e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{FullHash: true}, "", RunOptions{})
	if err != nil || !vr.OK {
		t.Fatalf("重新同步后校验应通过：%+v %v", vr, err)
	}
	c := filepath.Join(t.TempDir(), "copy")
	copyDir(t, mdir, c)
	checkData(t, openRestored(t, c), settings, blobs)
}

func TestRestoredRunRecordsNotLeftRunning(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	seed(t, inst, 3)
	e := newEngine(t, inst, nil)
	id, _ := localTarget(t, e, "t", ModeFull, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "r")
	if _, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	got := openRestored(t, dest)
	var rows []model.BackupRun
	got.Store.Select(ctx, &rows, "SELECT * FROM backup_runs")
	if len(rows) == 0 {
		t.Fatal("恢复出的数据里应有执行记录")
	}
	for _, r := range rows {
		if r.Status == StatusRunning || r.FinishedAt == nil {
			t.Fatalf("恢复出的执行记录不应停在进行中：%+v", r)
		}
	}
	var tg model.BackupTarget
	got.Store.Get(ctx, &tg, "SELECT * FROM backup_targets WHERE id = ?", id)
	if tg.LastStatus == StatusRunning {
		t.Fatal("恢复出的目标状态不应停在进行中")
	}
}

// 空附件：镜像里不上传（百度网盘不接受空文件），恢复时重建，校验不报缺失。
func TestMirrorEmptyBlob(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	settings, blobs := seed(t, inst, 3)
	blobs[addBlob(t, inst, nil)] = []byte{}
	e := newEngine(t, inst, nil)
	id, dir := localTarget(t, e, "镜像", ModeMirror, nil, nil)
	run, err := e.RunBackup(ctx, id, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, run.Artifact, filepath.FromSlash(datadir.BlobRel(emptySHA)))); err == nil {
		t.Fatal("空附件不应上传")
	}
	vr, err := e.VerifyNow(ctx, id, run.Artifact, VerifyOptions{FullHash: true}, "", RunOptions{})
	if err != nil || !vr.OK {
		t.Fatalf("校验应通过：%+v %v", vr, err)
	}
	dest := filepath.Join(t.TempDir(), "r")
	res, err := e.RestoreNow(ctx, id, run.Artifact, dest, "", RunOptions{})
	if err != nil || len(res.MissingBlobs) != 0 {
		t.Fatalf("恢复不对：%+v %v", res, err)
	}
	checkData(t, openRestored(t, dest), settings, blobs)
}
