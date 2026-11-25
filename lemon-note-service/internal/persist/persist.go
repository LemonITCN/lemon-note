// Package persist 负责数据目录与工作库之间的一切：首次初始化、启动恢复、
// 外箱封存成日志段、定期快照、清理旧文件、备份租约。
package persist

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Config 打开实例需要的参数。
type Config struct {
	DataDir  string
	CacheDir string // 缓存根目录；实例缓存在 <CacheDir>/<instance_id>
	DSN      string // MySQL / PostgreSQL 连接串
	HostID   string
	Version  string
	TakeOver bool
	Logger   *slog.Logger

	// 以下可选，测试用
	SealInterval     time.Duration
	SnapshotBytes    int64
	SnapshotInterval time.Duration
}

func (c *Config) defaults() {
	if c.SealInterval == 0 {
		c.SealInterval = 2 * time.Second
	}
	if c.SnapshotBytes == 0 {
		c.SnapshotBytes = 16 << 20
	}
	if c.SnapshotInterval == 0 {
		c.SnapshotInterval = 24 * time.Hour
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Instance 是一个打开着的实例。
type Instance struct {
	cfg      Config
	Dir      *datadir.Dir
	Info     *datadir.Instance
	Store    *store.Store
	CacheDir string // 本实例的缓存目录
	Lineage  string
	log      *slog.Logger
	lock     *datadir.Lock

	sealMu     sync.Mutex
	maxSealed  int64
	sinceSnap  atomic.Int64 // 上次快照之后封存的日志字节数
	lastSnap   atomic.Int64 // 上次快照时间
	snapMu     sync.Mutex
	holds      atomic.Int32 // 备份租约计数，>0 时不清理旧文件
	warnings   sync.Map     // key → string
	stopCh     chan struct{}
	wg         sync.WaitGroup
	closed     atomic.Bool
	lastSealOK atomic.Int64
}

// Status 是给界面看的持久化状态。
type Status struct {
	DataDir        string   `json:"data_dir"`
	CacheDir       string   `json:"cache_dir"`
	Engine         string   `json:"engine"`
	InstanceID     string   `json:"instance_id"`
	Seq            int64    `json:"seq"`
	SealedSeq      int64    `json:"sealed_seq"`
	SnapshotSeq    int64    `json:"snapshot_seq"`
	SnapshotAt     int64    `json:"snapshot_at"`
	JournalFiles   int      `json:"journal_files"`
	JournalBytes   int64    `json:"journal_bytes"`
	Warnings       []string `json:"warnings"`
	ReadOnlyReason string   `json:"read_only_reason,omitempty"`
}

// ErrNeedSetup 表示数据目录尚未初始化，需要先走首次设置。
var ErrNeedSetup = errors.New("数据目录尚未初始化")

// Initialize 初始化一个全新的数据目录（数据目录必须为空或不存在实例文件）。
func Initialize(ctx context.Context, cfg Config, engine store.Engine) error {
	cfg.defaults()
	d, err := datadir.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	if _, err := d.ReadInstance(); err == nil {
		return errors.New("数据目录已经初始化过")
	}
	entries, _ := os.ReadDir(d.Root)
	for _, e := range entries {
		n := e.Name()
		if n == ".DS_Store" || n == "desktop.ini" || datadir.IsTempName(n) {
			continue
		}
		return fmt.Errorf("数据目录不是空目录（发现 %s），请选择一个空目录", n)
	}
	if !engine.Valid() {
		return fmt.Errorf("未知的数据库引擎 %q", engine)
	}
	info := &datadir.Instance{Format: datadir.FormatVersion, InstanceID: ids.New(), Engine: string(engine), CreatedAt: time.Now().UnixMilli(), App: "lemon-note"}
	cache := filepath.Join(cfg.CacheDir, info.InstanceID)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return err
	}
	st, err := openStore(ctx, cfg, store.Engine(info.Engine), cache, true)
	if err != nil {
		return err
	}
	defer st.Close()
	if engine != store.SQLite {
		// 外部库必须是空库，防止误把别的系统的数据覆盖
		var n int64
		_ = st.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
		if n > 0 {
			return errors.New("目标数据库里已经有数据，请使用一个空数据库")
		}
	}
	lineage := ids.New()
	if err := st.SetMeta(ctx, "instance_id", info.InstanceID); err != nil {
		return err
	}
	if err := st.SetMeta(ctx, "lineage", lineage); err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := datadir.WriteFileAtomic(d.SecretKeyPath(), key, 0o600); err != nil {
		return err
	}
	for _, sub := range []string{"journal", "snapshots", "blobs", "locks"} {
		if err := os.MkdirAll(d.Path(sub), 0o755); err != nil {
			return err
		}
	}
	inst := &Instance{cfg: cfg, Dir: d, Info: info, Store: st, CacheDir: cache, Lineage: lineage, log: cfg.Logger}
	if err := inst.writeSnapshot(ctx, true); err != nil {
		return err
	}
	// 实例文件最后写：只有它存在，才算初始化完成
	return d.WriteInstance(info)
}

func openStore(ctx context.Context, cfg Config, engine store.Engine, cache string, fresh bool) (*store.Store, error) {
	o := store.Options{Engine: engine, DSN: cfg.DSN, WorkPath: filepath.Join(cache, "work.db")}
	if engine == store.SQLite && fresh {
		removeSQLiteFiles(o.WorkPath)
	}
	return store.Open(ctx, o)
}

func removeSQLiteFiles(path string) {
	for _, suf := range []string{"", "-wal", "-shm", "-journal"} {
		os.Remove(path + suf)
	}
}

// Open 打开已初始化的数据目录，必要时从快照与日志重建工作库。
func Open(ctx context.Context, cfg Config) (*Instance, error) {
	cfg.defaults()
	d, err := datadir.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	info, err := d.ReadInstance()
	if errors.Is(err, datadir.ErrNotInitialized) {
		return nil, ErrNeedSetup
	}
	if err != nil {
		return nil, err
	}
	cache := filepath.Join(cfg.CacheDir, info.InstanceID)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, fmt.Errorf("无法创建缓存目录: %w", err)
	}
	lock, err := d.Acquire(filepath.Join(cache, "instance.lock"), cfg.HostID, cfg.Version, cfg.TakeOver)
	if err != nil {
		return nil, err
	}
	inst := &Instance{cfg: cfg, Dir: d, Info: info, CacheDir: cache, log: cfg.Logger, lock: lock, stopCh: make(chan struct{})}
	if err := inst.recover(ctx); err != nil {
		lock.Release()
		if inst.Store != nil {
			inst.Store.Close()
		}
		return nil, err
	}
	datadir.CleanupTemps(d.Root, time.Hour)
	datadir.CleanupTemps(filepath.Join(cache, "tmp"), time.Hour)
	if c := d.ConflictCopies(); len(c) > 0 {
		inst.warn("conflict", fmt.Sprintf("数据目录里发现同步盘生成的冲突副本：%v。可能有两台机器同时使用过该目录。", c))
	}
	lock.OnError(func(err error) {
		inst.warn("lock", err.Error())
		var oe *datadir.ErrOwnedElsewhere
		if errors.As(err, &oe) {
			inst.Store.SetReadOnly("数据目录已被另一台机器接管")
		}
	})
	inst.wg.Add(1)
	go inst.loop()
	return inst, nil
}

func (in *Instance) warn(key, msg string) {
	in.warnings.Store(key, msg)
	in.log.Warn(msg)
}

// recover 把工作库带到与数据目录一致（或更新）的状态。
func (in *Instance) recover(ctx context.Context) error {
	man, err := in.Dir.ReadManifest()
	if err != nil {
		return fmt.Errorf("读取 MANIFEST 失败: %w", err)
	}
	if man.InstanceID != in.Info.InstanceID {
		return errors.New("MANIFEST 与实例信息不匹配，数据目录可能被混入了别的实例的文件")
	}
	in.Lineage = man.Lineage
	segs, err := in.Dir.ListSegments()
	if err != nil {
		return err
	}
	_, dirLast, gap := datadir.ChainAfter(segs, man.SnapshotSeq)
	if gap {
		in.warn("gap", fmt.Sprintf("数据目录的日志在序号 %d 之后不连续，可能有日志段被同步盘删掉或尚未同步下来", dirLast))
	}
	engine := store.Engine(in.Info.Engine)

	st, err := openStore(ctx, in.cfg, engine, in.CacheDir, false)
	if err != nil {
		if engine != store.SQLite {
			return err
		}
		in.log.Warn("工作库无法打开，将从数据目录重建", "err", err)
		return in.rebuild(ctx, man, segs)
	}
	in.Store = st
	dbInstance, _ := st.Meta(ctx, "instance_id")
	dbLineage, _ := st.Meta(ctx, "lineage")
	dbSeq, err := st.Seq(ctx)
	if err != nil {
		return err
	}
	if dbInstance == "" || dbInstance != in.Info.InstanceID || dbLineage != man.Lineage {
		if engine != store.SQLite && dbInstance != "" && dbInstance != in.Info.InstanceID {
			return errors.New("外部数据库里是另一个实例的数据，请换一个空库或正确的库")
		}
		in.log.Info("工作库与数据目录不是同一份数据，从数据目录重建")
		st.Close()
		in.Store = nil
		return in.rebuild(ctx, man, segs)
	}
	if engine == store.SQLite {
		if err := quickCheck(ctx, st); err != nil {
			in.log.Warn("工作库完整性检查失败，从数据目录重建", "err", err)
			st.Close()
			in.Store = nil
			return in.rebuild(ctx, man, segs)
		}
	}
	switch {
	case dbSeq < dirLast:
		chain, _, _ := datadir.ChainAfter(segs, dbSeq)
		if len(chain) == 0 || chain[0].First != dbSeq+1 || dbSeq < man.SnapshotSeq {
			in.log.Info("工作库落后且日志已被合并，从快照重建", "db_seq", dbSeq)
			st.Close()
			in.Store = nil
			return in.rebuild(ctx, man, segs)
		}
		if err := in.replay(ctx, chain); err != nil {
			return err
		}
		in.maxSealed = dirLast
	default:
		in.maxSealed = dirLast
		if dbSeq > dirLast {
			minOut, _ := st.MinOutboxSeq(ctx)
			if minOut == 0 || minOut > dirLast+1 {
				// 数据目录比工作库旧，且外箱补不上：以工作库为准，立即写新快照
				in.warn("behind", fmt.Sprintf("数据目录（序号 %d）比本机工作库（序号 %d）旧，已用工作库重新写出快照补齐", dirLast, dbSeq))
				if err := st.TrimOutbox(ctx, dbSeq); err != nil {
					return err
				}
				in.maxSealed = dbSeq
				if err := in.writeSnapshot(ctx, true); err != nil {
					return err
				}
			}
		}
	}
	if in.maxSealed > 0 {
		if err := st.TrimOutbox(ctx, in.maxSealed); err != nil {
			return err
		}
	}
	in.lastSnap.Store(man.WrittenAt)
	in.sinceSnap.Store(journalBytesAfter(segs, man.SnapshotSeq))
	return in.SealPending(ctx)
}

func journalBytesAfter(segs []datadir.Segment, seq int64) int64 {
	var n int64
	for _, s := range segs {
		if s.Last > seq {
			n += s.Size
		}
	}
	return n
}

func quickCheck(ctx context.Context, st *store.Store) error {
	var res string
	if err := st.W.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("quick_check: %s", res)
	}
	return nil
}

// rebuild 从 MANIFEST 指向的快照重建工作库，再重放之后的日志。
func (in *Instance) rebuild(ctx context.Context, man *datadir.Manifest, segs []datadir.Segment) error {
	engine := store.Engine(in.Info.Engine)
	snapPath := in.Dir.Path(filepath.FromSlash(man.Snapshot))
	tmpDir := filepath.Join(in.CacheDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	local := datadir.TempPath(tmpDir)
	if err := copyVerify(snapPath, local, man.SnapshotSHA256); err != nil {
		return fmt.Errorf("快照 %s 不可用: %w", man.Snapshot, err)
	}
	defer os.Remove(local)
	if engine == store.SQLite {
		work := filepath.Join(in.CacheDir, "work.db")
		removeSQLiteFiles(work)
		if err := os.Rename(local, work); err != nil {
			return err
		}
		st, err := openStore(ctx, in.cfg, engine, in.CacheDir, false)
		if err != nil {
			return err
		}
		in.Store = st
	} else {
		st, err := openStore(ctx, in.cfg, engine, in.CacheDir, false)
		if err != nil {
			return err
		}
		in.Store = st
		if _, err := st.ImportSnapshot(ctx, local); err != nil {
			return err
		}
	}
	seq, err := in.Store.Seq(ctx)
	if err != nil {
		return err
	}
	if seq != man.SnapshotSeq {
		return fmt.Errorf("快照序号不一致：MANIFEST 记录 %d，快照里是 %d", man.SnapshotSeq, seq)
	}
	chain, last, _ := datadir.ChainAfter(segs, seq)
	if err := in.replay(ctx, chain); err != nil {
		return err
	}
	in.maxSealed = last
	in.lastSnap.Store(man.WrittenAt)
	in.sinceSnap.Store(journalBytesAfter(segs, man.SnapshotSeq))
	in.log.Info("工作库已从数据目录重建", "snapshot_seq", man.SnapshotSeq, "seq", last)
	return nil
}

func copyVerify(src, dst, wantSHA string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	out.Close()
	if wantSHA != "" && hex.EncodeToString(h.Sum(nil)) != wantSHA {
		os.Remove(dst)
		return errors.New("sha256 校验不一致（文件可能还没同步完整或已损坏）")
	}
	return nil
}

func (in *Instance) replay(ctx context.Context, chain []datadir.Segment) error {
	for _, seg := range chain {
		hdr, entries, err := store.ReadSegmentFile(in.Dir.Path(filepath.FromSlash(seg.Name)))
		if err != nil {
			return fmt.Errorf("日志段 %s 不可用: %w", seg.Name, err)
		}
		if hdr.Lineage != in.Lineage {
			return fmt.Errorf("日志段 %s 属于另一份数据（血统不一致）", seg.Name)
		}
		if err := in.Store.ApplyEntries(ctx, entries); err != nil {
			return fmt.Errorf("重放日志段 %s 失败: %w", seg.Name, err)
		}
	}
	return nil
}

// loop 后台封存与快照。
func (in *Instance) loop() {
	defer in.wg.Done()
	t := time.NewTicker(in.cfg.SealInterval)
	defer t.Stop()
	snapCheck := time.NewTicker(time.Minute)
	defer snapCheck.Stop()
	ctx := context.Background()
	for {
		select {
		case <-in.stopCh:
			return
		case <-t.C:
			if err := in.SealPending(ctx); err != nil {
				in.warn("seal", "写入数据目录失败："+err.Error())
			} else {
				in.warnings.Delete("seal")
			}
		case <-snapCheck.C:
			if in.shouldSnapshot() {
				if err := in.Snapshot(ctx); err != nil {
					in.warn("snapshot", "写快照失败："+err.Error())
				} else {
					in.warnings.Delete("snapshot")
				}
			}
		}
	}
}

func (in *Instance) shouldSnapshot() bool {
	since := in.sinceSnap.Load()
	if since >= in.cfg.SnapshotBytes {
		return true
	}
	return since > 0 && time.Since(time.UnixMilli(in.lastSnap.Load())) >= in.cfg.SnapshotInterval
}

// SealPending 把外箱里全部待封存的变更写成日志段。
func (in *Instance) SealPending(ctx context.Context) error {
	in.sealMu.Lock()
	defer in.sealMu.Unlock()
	for {
		entries, err := in.Store.PendingOutbox(ctx, in.maxSealed, 5000, 4<<20)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			in.lastSealOK.Store(time.Now().UnixMilli())
			return nil
		}
		if entries[0].Seq != in.maxSealed+1 {
			in.warn("outbox-gap", fmt.Sprintf("外箱序号不连续（期望 %d，实际 %d），将写新快照补齐", in.maxSealed+1, entries[0].Seq))
		}
		// 截断到连续的一段
		n := 1
		for n < len(entries) && entries[n].Seq == entries[n-1].Seq+1 {
			n++
		}
		entries = entries[:n]
		first, last := entries[0].Seq, entries[len(entries)-1].Seq
		rel := datadir.SegmentName(first, last)
		var size int64
		err = datadir.WriteAtomic(in.Dir.Path(filepath.FromSlash(rel)), 0o644, func(w io.Writer) error {
			cw := &countWriter{w: w}
			err := store.EncodeSegment(cw, in.Lineage, entries)
			size = cw.n
			return err
		})
		if err != nil {
			return err
		}
		in.maxSealed = last
		in.sinceSnap.Add(size)
		if err := in.Store.TrimOutbox(ctx, last); err != nil {
			return err
		}
	}
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Snapshot 立即写一个新快照。
func (in *Instance) Snapshot(ctx context.Context) error {
	return in.writeSnapshot(ctx, false)
}

func (in *Instance) writeSnapshot(ctx context.Context, force bool) error {
	in.snapMu.Lock()
	defer in.snapMu.Unlock()
	if in.stopCh != nil {
		if err := in.SealPending(ctx); err != nil {
			return err
		}
	}
	prev, _ := in.Dir.ReadManifest()
	seq, err := in.Store.Seq(ctx)
	if err != nil {
		return err
	}
	if !force && prev != nil && prev.SnapshotSeq == seq {
		in.sinceSnap.Store(0)
		in.lastSnap.Store(time.Now().UnixMilli())
		return nil
	}
	tmpDir := filepath.Join(in.CacheDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	local := filepath.Join(tmpDir, "snapshot-"+ids.RandomHex(6)+".db")
	defer os.Remove(local)
	got, err := in.Store.ExportSnapshot(ctx, local)
	if err != nil {
		return err
	}
	sum, size, err := fileSHA256(local)
	if err != nil {
		return err
	}
	rel := datadir.SnapshotName(got)
	dst := in.Dir.Path(filepath.FromSlash(rel))
	if _, err := os.Stat(dst); err != nil {
		if err := datadir.MoveIntoPlace(local, dst, 0o644); err != nil {
			return err
		}
	} else if prev != nil && prev.Snapshot == rel {
		sum, size = prev.SnapshotSHA256, prev.SnapshotSize
	} else {
		// 同序号快照已存在（之前写到一半前崩溃过）：校验它
		s2, sz2, err := fileSHA256(dst)
		if err != nil || s2 != sum {
			_ = datadir.RemoveRetry(dst)
			if err := datadir.MoveIntoPlace(local, dst, 0o644); err != nil {
				return err
			}
		} else {
			size = sz2
		}
	}
	man := &datadir.Manifest{InstanceID: in.Info.InstanceID, Lineage: in.Lineage, Snapshot: rel, SnapshotSeq: got,
		SnapshotSHA256: sum, SnapshotSize: size, SchemaVersion: store.SchemaVersion}
	if err := in.Dir.WriteManifest(man); err != nil {
		return err
	}
	in.sinceSnap.Store(0)
	in.lastSnap.Store(time.Now().UnixMilli())
	if prev != nil {
		in.cleanup(prev.SnapshotSeq, got)
	}
	return nil
}

// cleanup 保留当前与上一个快照（以及上一个快照之后的日志），其余删除。
func (in *Instance) cleanup(prevSeq, curSeq int64) {
	if in.holds.Load() > 0 {
		return
	}
	keepFrom := prevSeq
	if keepFrom > curSeq {
		keepFrom = curSeq
	}
	snaps, _ := in.Dir.ListSnapshots()
	for _, s := range snaps {
		if s < keepFrom {
			_ = datadir.RemoveRetry(in.Dir.Path(filepath.FromSlash(datadir.SnapshotName(s))))
		}
	}
	segs, _ := in.Dir.ListSegments()
	for _, s := range segs {
		if s.Last <= keepFrom {
			_ = datadir.RemoveRetry(in.Dir.Path(filepath.FromSlash(s.Name)))
		}
	}
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Hold 取得备份租约：持有期间不删除任何旧快照与日志段。返回释放函数。
func (in *Instance) Hold() func() {
	in.holds.Add(1)
	var once sync.Once
	return func() { once.Do(func() { in.holds.Add(-1) }) }
}

// Flush 立即封存外箱（备份前、退出前调用）。
func (in *Instance) Flush(ctx context.Context) error { return in.SealPending(ctx) }

// Status 返回当前状态。
func (in *Instance) Status(ctx context.Context) Status {
	st := Status{DataDir: in.Dir.Root, CacheDir: in.CacheDir, Engine: in.Info.Engine, InstanceID: in.Info.InstanceID,
		ReadOnlyReason: in.Store.ReadOnlyReason()}
	st.Seq, _ = in.Store.Seq(ctx)
	in.sealMu.Lock()
	st.SealedSeq = in.maxSealed
	in.sealMu.Unlock()
	if man, err := in.Dir.ReadManifest(); err == nil {
		st.SnapshotSeq = man.SnapshotSeq
		st.SnapshotAt = man.WrittenAt
	}
	segs, _ := in.Dir.ListSegments()
	st.JournalFiles = len(segs)
	for _, s := range segs {
		st.JournalBytes += s.Size
	}
	in.warnings.Range(func(_, v any) bool {
		st.Warnings = append(st.Warnings, v.(string))
		return true
	})
	return st
}

// Close 封存剩余变更、按需写快照、释放占用锁。
func (in *Instance) Close(ctx context.Context) error {
	if !in.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(in.stopCh)
	in.wg.Wait()
	var firstErr error
	if err := in.SealPending(ctx); err != nil {
		firstErr = err
	}
	if in.sinceSnap.Load() >= 1<<20 {
		if err := in.writeSnapshot(ctx, false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := in.Store.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if in.lock != nil {
		in.lock.Release()
	}
	return firstErr
}

// SecretKey 读取数据目录里的凭据加密密钥。
func (in *Instance) SecretKey() ([]byte, error) {
	b, err := os.ReadFile(in.Dir.SecretKeyPath())
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("config/secret.key 长度不对")
	}
	return b, nil
}
