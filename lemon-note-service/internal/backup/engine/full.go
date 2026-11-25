package engine

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/cryptstream"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/klauspost/compress/zstd"
)

// 全量包里除数据目录文件之外的两个条目：第一个条目是包信息，最后一个条目是逐文件索引。
const (
	infoName  = "lemon-note-backup-info.json"
	indexName = "lemon-note-backup-index.json"
	pkgFormat = 1
)

// PackageInfo 是全量包的第一个条目。
type PackageInfo struct {
	Format      int    `json:"format"`
	App         string `json:"app"`
	InstanceID  string `json:"instance_id"`
	Engine      string `json:"engine"`
	Lineage     string `json:"lineage"`
	SnapshotSeq int64  `json:"snapshot_seq"`
	LastSeq     int64  `json:"last_seq"`
	CreatedAt   int64  `json:"created_at"`
	Host        string `json:"host"`
	Version     string `json:"version"`
	Files       int64  `json:"files"` // 数据目录文件个数（不含本条目与索引）
	Bytes       int64  `json:"bytes"` // 数据目录文件总字节数
}

// packageIndex 是全量包的最后一个条目：每个数据目录文件的大小与 sha256。
// 它出现在包尾，读到它就说明包没有被截断。
type packageIndex struct {
	Format int         `json:"format"`
	Files  []indexItem `json:"files"`
}

type indexItem struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type runResult struct {
	Files    int64
	Bytes    int64
	Artifact string
}

func newZstdWriter(w io.Writer) (*zstd.Encoder, error) {
	n := runtime.GOMAXPROCS(0)
	if n > 4 {
		n = 4
	}
	return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(n), zstd.WithEncoderCRC(true))
}

// backupFull 生成一个全量包并上传，成功后按保留策略删除旧包。
func (e *Engine) backupFull(ctx context.Context, p *Progress, t target.Target, key []byte, ret Retention) (*runResult, error) {
	release := e.inst.Hold()
	defer release()
	p.SetPhase("封存待写入的变更")
	if err := e.inst.Flush(ctx); err != nil {
		return nil, fmt.Errorf("封存待写入的变更失败：%w", err)
	}
	p.SetPhase("收集数据目录文件")
	fs, err := collectValidated(ctx, e.inst, p)
	if err != nil {
		return nil, err
	}
	id8 := instanceShort(fs.Info.InstanceID)
	base, err := e.freePackageBase(ctx, t, id8)
	if err != nil {
		return nil, err
	}
	files := int64(3 + 1 + len(fs.Chain) + len(fs.Blobs))
	var plain int64 = int64(len(fs.InstanceRaw) + len(fs.ManifestRaw) + len(fs.SecretKey))
	plain += fs.Snapshot.Size + fs.BlobBytes
	for _, s := range fs.Chain {
		plain += s.Size
	}
	p.Logf("快照 %s（序号 %d），之后的日志段 %d 个（到序号 %d），附件 %d 个（%s），合计 %s",
		fs.Snapshot.Rel, fs.Manifest.SnapshotSeq, len(fs.Chain), fs.LastSeq, len(fs.Blobs), humanBytes(fs.BlobBytes), humanBytes(plain))
	volSize := int64(ret.VolumeMB) << 20
	if e.o.VolumeSize > 0 {
		volSize = e.o.VolumeSize
	}
	tmpDir, err := e.tempDir()
	if err != nil {
		return nil, err
	}
	vw := &volumeWriter{ctx: ctx, t: t, base: base, volSize: volSize, tmpDir: tmpDir, p: p}
	p.SetPhase("打包并上传 " + tailName(base))
	p.SetTotal(files, plain)
	info := PackageInfo{Format: pkgFormat, App: "lemon-note", InstanceID: fs.Info.InstanceID, Engine: fs.Info.Engine,
		Lineage: fs.Manifest.Lineage, SnapshotSeq: fs.Manifest.SnapshotSeq, LastSeq: fs.LastSeq, CreatedAt: e.now().UnixMilli(),
		Version: version.Version, Files: files, Bytes: plain}
	info.Host, _ = os.Hostname()
	if err := e.streamPackage(ctx, vw, key, fs, &info, p); err != nil {
		vw.abort()
		return nil, err
	}
	// 回查目标上的分卷，确认个数与大小都对。
	if err := checkUploadedVolumes(ctx, t, base, vw.sizes); err != nil {
		vw.abort()
		return nil, err
	}
	p.Logf("已上传 %s，共 %d 个分卷，%s", tailName(base), len(vw.sizes), humanBytes(vw.total))
	release()
	if err := e.applyRetention(ctx, t, id8, base, ret, p); err != nil {
		p.Warnf("按保留策略清理旧备份失败：%v", err)
	}
	return &runResult{Files: files, Bytes: vw.total, Artifact: tailName(base)}, nil
}

// freePackageBase 选一个目标上还没有的包名（同一秒内多次备份时顺延一秒）。
func (e *Engine) freePackageBase(ctx context.Context, t target.Target, id8 string) (string, error) {
	now := e.now()
	for i := 0; i < 120; i++ {
		base := packageBase(id8, now.Add(time.Duration(i)*time.Second))
		list, err := t.List(ctx, base+".lnbak")
		if err != nil {
			return "", wrapUser(fmt.Errorf("列出目标上的文件失败：%w", err))
		}
		if len(list) == 0 {
			return base, nil
		}
	}
	return "", uerr("无法为备份包选出不重名的文件名")
}

// streamPackage 把文件集合写成 tar → zstd →（加密）→ 分卷上传。
func (e *Engine) streamPackage(ctx context.Context, vw *volumeWriter, key []byte, fs *fileSet, info *PackageInfo, p *Progress) error {
	var sink io.Writer = vw
	var enc io.WriteCloser
	if key != nil {
		var err error
		enc, err = cryptstream.NewEncryptWriter(vw, key)
		if err != nil {
			return err
		}
		sink = enc
	}
	zw, err := newZstdWriter(sink)
	if err != nil {
		return err
	}
	pw := &packageWriter{ctx: ctx, tw: tar.NewWriter(zw), p: p, mtime: time.UnixMilli(info.CreatedAt), buf: make([]byte, 1<<20)}
	err = pw.writeAll(e.inst.Dir, fs, info)
	if err == nil {
		err = pw.tw.Close()
	}
	if err != nil {
		vw.done = true // 已经失败：下面关闭压缩器时冲出的数据不再上传
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if enc != nil {
		if cerr := enc.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = vw.finish()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

type packageWriter struct {
	ctx   context.Context
	tw    *tar.Writer
	p     *Progress
	mtime time.Time
	index []indexItem
	buf   []byte
}

func (w *packageWriter) header(name string, size int64, mtime time.Time) *tar.Header {
	return &tar.Header{Name: name, Mode: 0o644, Size: size, ModTime: mtime, Typeflag: tar.TypeReg}
}

func (w *packageWriter) writeBytes(name string, data []byte, indexed bool) error {
	if err := w.tw.WriteHeader(w.header(name, int64(len(data)), w.mtime)); err != nil {
		return err
	}
	if _, err := w.tw.Write(data); err != nil {
		return err
	}
	if indexed {
		sum := sha256.Sum256(data)
		w.index = append(w.index, indexItem{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		w.p.AddFiles(1)
		w.p.AddBytes(int64(len(data)))
	}
	return nil
}

// writeFile 写一个数据目录文件；返回实际 sha256。文件消失时返回 os.ErrNotExist。
func (w *packageWriter) writeFile(d *datadir.Dir, f srcFile) (string, error) {
	if err := w.ctx.Err(); err != nil {
		return "", err
	}
	fh, err := os.Open(d.Path(filepath.FromSlash(f.Rel)))
	if err != nil {
		return "", err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	if err := w.tw.WriteHeader(w.header(f.Rel, size, st.ModTime())); err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.CopyBuffer(w.tw, io.TeeReader(countingReader{r: io.LimitReader(fh, size), p: w.p}, h), w.buf)
	if err != nil {
		return "", err
	}
	if n != size {
		return "", uerrf("读取 %s 时文件变短了（%d / %d 字节）", f.Rel, n, size)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	w.index = append(w.index, indexItem{Path: f.Rel, Size: size, SHA256: sum})
	w.p.AddFiles(1)
	return sum, nil
}

func (w *packageWriter) writeAll(d *datadir.Dir, fs *fileSet, info *PackageInfo) error {
	infoJSON, _ := json.MarshalIndent(info, "", "  ")
	if err := w.writeBytes(infoName, infoJSON, false); err != nil {
		return err
	}
	if err := w.writeBytes("lemon-note.json", fs.InstanceRaw, true); err != nil {
		return err
	}
	if err := w.writeBytes("config/secret.key", fs.SecretKey, true); err != nil {
		return err
	}
	sum, err := w.writeFile(d, fs.Snapshot)
	if err != nil {
		return fmt.Errorf("写入快照 %s 失败：%w", fs.Snapshot.Rel, err)
	}
	if fs.Snapshot.SHA != "" && sum != fs.Snapshot.SHA {
		return uerrf("快照 %s 在备份过程中内容变了（sha256 与 MANIFEST 不一致），本次备份作废", fs.Snapshot.Rel)
	}
	for _, s := range fs.Chain {
		if _, err := w.writeFile(d, s); err != nil {
			return fmt.Errorf("写入日志段 %s 失败：%w", s.Rel, err)
		}
	}
	for _, b := range fs.Blobs {
		sum, err := w.writeFile(d, b)
		if errors.Is(err, os.ErrNotExist) {
			w.p.Warnf("附件 %s 在备份过程中被删除，已跳过", b.SHA)
			continue
		}
		if err != nil {
			return fmt.Errorf("写入附件 %s 失败：%w", b.SHA, err)
		}
		if sum != b.SHA {
			w.p.Warnf("附件 %s 的内容与文件名（sha256）不符，数据目录里的这个文件可能已损坏；已按原样备份", b.SHA)
		}
	}
	if err := w.writeBytes("MANIFEST", fs.ManifestRaw, true); err != nil {
		return err
	}
	idx, _ := json.Marshal(packageIndex{Format: pkgFormat, Files: w.index})
	return w.writeBytes(indexName, idx, false)
}

// ---------------- 分卷上传 ----------------

// volumeWriter 把连续的字节流切成固定大小的分卷：每卷先写进缓存目录的临时文件，写满后上传（Put 需要精确大小），
// 所以本机最多只占用一个分卷大小的临时空间。写满的一卷要等到确认后面还有数据才作为 .NNNN 上传，
// 收尾时手里那一卷作为 .lnbak 最后上传，所以最后一卷永远不为空，而且 .lnbak 出现就说明整个包已经传完。
type volumeWriter struct {
	ctx     context.Context
	t       target.Target
	base    string
	volSize int64
	tmpDir  string
	p       *Progress

	f        *os.File
	n        int64
	uploaded []string // 已上传的分卷路径
	sizes    []int64  // 每卷大小（含最后一卷）
	total    int64
	done     bool
}

func (v *volumeWriter) open() error {
	if v.f != nil {
		return nil
	}
	// ~$ 开头：进程意外退出留下的残留会被启动时的临时文件清理删掉。
	f, err := os.OpenFile(filepath.Join(v.tmpDir, datadir.TempPrefix+"volume-"+ids.RandomHex(8)+".part"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("创建分卷临时文件失败：%w", err)
	}
	v.f = f
	v.n = 0
	return nil
}

func (v *volumeWriter) upload(name string) error {
	if _, err := v.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := v.t.Put(v.ctx, name, io.LimitReader(v.f, v.n), v.n); err != nil {
		if v.ctx.Err() != nil {
			return v.ctx.Err()
		}
		return wrapUser(fmt.Errorf("上传分卷 %s 失败：%w", name, err))
	}
	v.uploaded = append(v.uploaded, name)
	v.sizes = append(v.sizes, v.n)
	v.total += v.n
	if err := v.f.Truncate(0); err != nil {
		return err
	}
	if _, err := v.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	v.n = 0
	return nil
}

func (v *volumeWriter) Write(p []byte) (int, error) {
	if v.done {
		return 0, errors.New("分卷写入器已关闭")
	}
	if err := v.ctx.Err(); err != nil {
		return 0, err
	}
	if err := v.open(); err != nil {
		return 0, err
	}
	written := 0
	for len(p) > 0 {
		if v.n >= v.volSize {
			idx := len(v.uploaded) + 1
			if idx > 999999 {
				return written, uerr("备份包分卷数量超过上限，请把分卷大小调大")
			}
			if err := v.upload(partName(v.base, idx)); err != nil {
				return written, err
			}
		}
		k := int64(len(p))
		if room := v.volSize - v.n; k > room {
			k = room
		}
		n, err := v.f.Write(p[:k])
		v.n += int64(n)
		written += n
		p = p[n:]
		if err != nil {
			return written, fmt.Errorf("写分卷临时文件失败（缓存目录所在磁盘可能已满）：%w", err)
		}
	}
	return written, nil
}

// finish 上传最后一卷（.lnbak）。
func (v *volumeWriter) finish() error {
	if err := v.open(); err != nil {
		return err
	}
	if v.n == 0 {
		return errors.New("备份包内容为空")
	}
	if err := v.upload(tailName(v.base)); err != nil {
		return err
	}
	v.done = true
	v.cleanupTemp()
	return nil
}

func (v *volumeWriter) cleanupTemp() {
	if v.f != nil {
		name := v.f.Name()
		v.f.Close()
		os.Remove(name)
		v.f = nil
	}
}

// abort 放弃本次备份：删除临时文件和已经上传的分卷（尽力而为）。
func (v *volumeWriter) abort() {
	v.cleanupTemp()
	v.done = true
	if len(v.uploaded) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(v.ctx), 2*time.Minute)
	defer cancel()
	// 先删 .lnbak（让包立即变成「不完整」），再删前面的分卷。
	for i := len(v.uploaded) - 1; i >= 0; i-- {
		if err := v.t.Delete(ctx, v.uploaded[i]); err != nil {
			v.p.Warnf("清理未完成的分卷 %s 失败：%v（下次备份时会再清理）", v.uploaded[i], err)
		}
	}
}

// checkUploadedVolumes 列出目标上这个包的分卷，核对个数与大小。
func checkUploadedVolumes(ctx context.Context, t target.Target, base string, sizes []int64) error {
	list, err := t.List(ctx, base+".lnbak")
	if err != nil {
		return wrapUser(fmt.Errorf("上传后核对分卷失败：%w", err))
	}
	got := map[string]int64{}
	for _, e := range list {
		got[e.Path] = e.Size
	}
	for i, sz := range sizes {
		name := tailName(base)
		if i < len(sizes)-1 {
			name = partName(base, i+1)
		}
		have, ok := got[name]
		if !ok {
			return uerrf("上传后核对：目标上找不到分卷 %s", name)
		}
		if have != sz {
			return uerrf("上传后核对：分卷 %s 在目标上是 %d 字节，应为 %d 字节", name, have, sz)
		}
	}
	return nil
}
