package engine

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/cryptstream"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/klauspost/compress/zstd"
)

// BackupInfo 是目标上的一个备份。
type BackupInfo struct {
	Name        string `json:"name"`                  // 全量包为 .lnbak 文件名，镜像为镜像目录名
	Mode        string `json:"mode"`                  // full | mirror
	Instance    string `json:"instance"`              // 实例 ID 前 8 位
	InstanceID  string `json:"instance_id,omitempty"` // 完整实例 ID（镜像能读到时才有）
	Current     bool   `json:"current"`               // 是否属于当前实例
	CreatedAt   int64  `json:"created_at"`            // 全量：备份时间；镜像：MANIFEST 写入时间
	Size        int64  `json:"size,omitempty"`        // 全量包全部分卷的总大小
	Volumes     int    `json:"volumes,omitempty"`
	Encrypted   bool   `json:"encrypted"`
	SnapshotSeq int64  `json:"snapshot_seq,omitempty"`
	Note        string `json:"note,omitempty"`
}

// ListBackups 列出目标上的全部 Lemon Note 备份（含其他实例的，便于换机器恢复），新的在前。
// key 用于读取加密镜像的 MANIFEST，可为 nil（此时只列名字）。
func ListBackups(ctx context.Context, t target.Target, key []byte, hdr *Header, currentInstanceID string) ([]BackupInfo, error) {
	cur := instanceShort(currentInstanceID)
	list, err := t.List(ctx, "lemon-note-")
	if err != nil {
		return nil, wrapUser(fmt.Errorf("列出目标上的备份失败：%w", err))
	}
	var out []BackupInfo
	for _, pk := range parsePackages(list) {
		if !pk.Complete() {
			continue
		}
		bi := BackupInfo{Name: tailName(pk.Base), Mode: ModeFull, Instance: pk.ID8, Current: cur != "" && pk.ID8 == cur,
			CreatedAt: pk.Time.UnixMilli(), Size: pk.Size(), Volumes: len(pk.Parts) + 1, Encrypted: hdr.Encrypted()}
		if err := pk.checkVolumes(); err != nil {
			bi.Note = err.Error()
		}
		out = append(out, bi)
	}
	mirrors := map[string]bool{}
	for _, e := range list {
		if m := pointerRe.FindStringSubmatch(e.Path); m != nil {
			mirrors[m[1]] = true
		}
	}
	if cur != "" && !mirrors[cur] {
		if _, err := t.Stat(ctx, mirrorDir(cur)+"/MANIFEST"); err == nil {
			mirrors[cur] = true
		}
	}
	for id8 := range mirrors {
		dir := mirrorDir(id8)
		bi := BackupInfo{Name: dir, Mode: ModeMirror, Instance: id8, Current: id8 == cur, Encrypted: hdr.Encrypted()}
		st, err := t.Stat(ctx, dir+"/MANIFEST")
		if err != nil {
			if errors.Is(err, target.ErrNotExist) {
				bi.Note = "镜像还没有完成第一次同步（缺少 MANIFEST）"
			} else {
				bi.Note = err.Error()
			}
			out = append(out, bi)
			continue
		}
		bi.CreatedAt = st.ModTime.UnixMilli()
		if hdr.Encrypted() && key == nil {
			bi.Note = "需要备份口令才能读取详细信息"
			out = append(out, bi)
			continue
		}
		raw, err := getBytes(ctx, t, dir+"/MANIFEST", key, 1<<20)
		if err != nil {
			bi.Note = err.Error()
			out = append(out, bi)
			continue
		}
		var man datadir.Manifest
		if json.Unmarshal(raw, &man) != nil {
			bi.Note = "镜像里的 MANIFEST 已损坏"
		} else {
			bi.InstanceID, bi.SnapshotSeq = man.InstanceID, man.SnapshotSeq
			if man.WrittenAt > 0 {
				bi.CreatedAt = man.WrittenAt
			}
		}
		out = append(out, bi)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// ---------------- 全量包读取 ----------------

// volumeReader 依次读取各个分卷，读完一卷再打开下一卷，并核对每卷读到的字节数。
type volumeReader struct {
	ctx   context.Context
	t     target.Target
	vols  []string
	sizes []int64
	i     int
	cur   io.ReadCloser
	n     int64
}

func (r *volumeReader) Read(b []byte) (int, error) {
	for {
		if r.i >= len(r.vols) {
			return 0, io.EOF
		}
		if r.cur == nil {
			rc, err := r.t.Get(r.ctx, r.vols[r.i])
			if err != nil {
				return 0, wrapUser(fmt.Errorf("读取分卷 %s 失败：%w", r.vols[r.i], err))
			}
			r.cur, r.n = rc, 0
		}
		n, err := r.cur.Read(b)
		r.n += int64(n)
		if err == io.EOF {
			r.cur.Close()
			r.cur = nil
			if r.n != r.sizes[r.i] {
				return n, uerrf("分卷 %s 读到 %d 字节，应为 %d 字节", r.vols[r.i], r.n, r.sizes[r.i])
			}
			r.i++
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, wrapUser(fmt.Errorf("读取分卷 %s 失败：%w", r.vols[r.i], err))
		}
		return n, nil
	}
}

func (r *volumeReader) Close() error {
	if r.cur != nil {
		r.cur.Close()
		r.cur = nil
	}
	return nil
}

type stackCloser struct {
	io.Reader
	closers []func()
}

func (s *stackCloser) Close() error {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
	return nil
}

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// openPackage 打开一个全量包，返回解密、解压后的 tar 流。p 统计读取的原始字节数。
func openPackage(ctx context.Context, t target.Target, name string, key []byte, p *Progress) (io.ReadCloser, *pkgFiles, error) {
	m := pkgRe.FindStringSubmatch(name)
	if m == nil || m[3] != "" {
		return nil, nil, uerrf("「%s」不是全量备份包的名字（应为 lemon-note-xxxxxxxx-YYYYMMDD-HHMMSS.lnbak）", name)
	}
	base := strings.TrimSuffix(name, ".lnbak")
	list, err := t.List(ctx, base+".lnbak")
	if err != nil {
		return nil, nil, wrapUser(fmt.Errorf("列出备份包分卷失败：%w", err))
	}
	pk := parsePackages(list)[base]
	if pk == nil || len(pk.Parts) == 0 && pk.Tail == nil {
		return nil, nil, ErrBackupNotFound
	}
	if err := pk.checkVolumes(); err != nil {
		return nil, nil, err
	}
	vols := pk.Volumes()
	sizes := make([]int64, len(vols))
	for i := range vols {
		if i < len(vols)-1 {
			sizes[i] = pk.Parts[i+1].Size
		} else {
			sizes[i] = pk.Tail.Size
		}
	}
	vr := &volumeReader{ctx: ctx, t: t, vols: vols, sizes: sizes}
	var raw io.Reader = vr
	if p != nil {
		raw = countingReader{r: vr, p: p}
	}
	br := bufio.NewReaderSize(raw, 256<<10)
	magic, err := br.Peek(4)
	if err != nil {
		vr.Close()
		return nil, nil, uerrf("读取备份包 %s 失败：%v", name, err)
	}
	var src io.Reader = br
	switch {
	case bytes.Equal(magic, []byte("LNBK")):
		if key == nil {
			vr.Close()
			return nil, nil, ErrNeedPassword
		}
		dr, err := cryptstream.NewDecryptReader(br, key)
		if err != nil {
			vr.Close()
			return nil, nil, decryptErr(name, err)
		}
		src = dr
	case bytes.Equal(magic, zstdMagic):
	default:
		vr.Close()
		return nil, nil, uerrf("「%s」不是 Lemon Note 备份包", name)
	}
	zr, err := zstd.NewReader(src, zstd.WithDecoderConcurrency(2))
	if err != nil {
		vr.Close()
		return nil, nil, err
	}
	return &stackCloser{Reader: zr, closers: []func(){func() { vr.Close() }, zr.Close}}, pk, nil
}

// scannedFile 是从全量包里读出的一个数据目录文件。
type scannedFile struct {
	Size int64
	SHA  string
}

type scanResult struct {
	info        *PackageInfo
	instanceRaw []byte
	manifestRaw []byte
	files       map[string]scannedFile
	blobs       map[string]int64 // 附件 sha（文件名）→ 大小
	badBlobs    []string         // 内容与文件名不符的附件
	bytes       int64
}

// scanPackage 读完整个 tar 流：校验条目名与顺序，计算每个文件的 sha256，核对包尾索引。
// dest 非空时把数据目录文件写到 dest（lemon-note.json 只留在内存里，由调用方最后写入）；
// writeBlobs 为 false 时附件只计算哈希不落盘（校验用）。
func scanPackage(ctx context.Context, r io.Reader, dest string, writeBlobs bool, p *Progress) (*scanResult, error) {
	tr := tar.NewReader(r)
	res := &scanResult{files: map[string]scannedFile{}, blobs: map[string]int64{}}
	var index *packageIndex
	first := true
	dirs := newDirSet()
	defer dirs.sync()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, packageReadErr(err)
		}
		if index != nil {
			return nil, uerr("备份包在索引之后还有内容，可能被篡改")
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, uerrf("备份包里有不支持的条目类型：%s", hdr.Name)
		}
		name := hdr.Name
		if first {
			first = false
			if name != infoName {
				return nil, uerr("备份包缺少包信息条目，可能不是 Lemon Note 备份包")
			}
		}
		if _, dup := res.files[name]; dup {
			return nil, uerrf("备份包里有重复的条目：%s", name)
		}
		readSmall := func(limit int64) ([]byte, error) {
			if hdr.Size > limit {
				return nil, uerrf("备份包里的 %s 太大", name)
			}
			b, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
			if err != nil {
				return nil, packageReadErr(err)
			}
			if int64(len(b)) != hdr.Size {
				return nil, uerrf("备份包里的 %s 不完整", name)
			}
			return b, nil
		}
		record := func(b []byte) {
			sum := sha256.Sum256(b)
			res.files[name] = scannedFile{Size: int64(len(b)), SHA: hex.EncodeToString(sum[:])}
			res.bytes += int64(len(b))
			p.AddFiles(1)
		}
		switch {
		case name == infoName:
			b, err := readSmall(1 << 20)
			if err != nil {
				return nil, err
			}
			var info PackageInfo
			if json.Unmarshal(b, &info) != nil {
				return nil, uerr("备份包的包信息已损坏")
			}
			if info.Format > pkgFormat {
				return nil, uerr("备份包的格式版本高于本程序支持的版本，请升级程序")
			}
			res.info = &info
			p.SetTotal(info.Files, info.Bytes)
		case name == indexName:
			b, err := readSmall(512 << 20)
			if err != nil {
				return nil, err
			}
			var idx packageIndex
			if json.Unmarshal(b, &idx) != nil {
				return nil, uerr("备份包的文件索引已损坏")
			}
			index = &idx
		case name == "lemon-note.json":
			b, err := readSmall(1 << 20)
			if err != nil {
				return nil, err
			}
			res.instanceRaw = b
			record(b)
		case name == "MANIFEST" || name == "config/secret.key":
			b, err := readSmall(1 << 20)
			if err != nil {
				return nil, err
			}
			if name == "MANIFEST" {
				res.manifestRaw = b
			} else if len(b) != 32 {
				return nil, uerr("备份包里的 config/secret.key 长度不对")
			}
			if dest != "" {
				if _, err := writeFileSynced(filepath.Join(dest, filepath.FromSlash(name)), bytes.NewReader(b), nil, dirs); err != nil {
					return nil, err
				}
			}
			record(b)
		default:
			kind, ok := mirrorManaged(name)
			if !ok {
				return nil, uerrf("备份包里有无法识别的文件：%s", name)
			}
			h := sha256.New()
			var src io.Reader = io.TeeReader(countingReader{r: io.LimitReader(tr, hdr.Size), p: p}, h)
			var n int64
			if dest != "" && (kind != fileBlob || writeBlobs) {
				n, err = writeFileSynced(filepath.Join(dest, filepath.FromSlash(name)), src, buf, dirs)
			} else {
				n, err = io.CopyBuffer(io.Discard, src, buf)
			}
			if err != nil {
				return nil, packageReadErr(err)
			}
			if n != hdr.Size {
				return nil, uerrf("备份包里的 %s 不完整", name)
			}
			sum := hex.EncodeToString(h.Sum(nil))
			res.files[name] = scannedFile{Size: n, SHA: sum}
			res.bytes += n
			p.AddFiles(1)
			if kind == fileBlob {
				sha := name[strings.LastIndex(name, "/")+1:]
				res.blobs[sha] = n
				if sum != sha {
					res.badBlobs = append(res.badBlobs, sha)
				}
			}
		}
	}
	// 读到 tar 结尾后把解压流读完：zstd 校验整帧、解密端核对最后一块，确认包没有被截断或追加。
	if extra, err := io.Copy(io.Discard, r); err != nil {
		return nil, packageReadErr(err)
	} else if extra > 0 {
		return nil, uerr("备份包结尾有多余的数据，可能被篡改")
	}
	if res.info == nil {
		return nil, uerr("备份包缺少包信息条目")
	}
	if index == nil {
		return nil, uerr("备份包不完整：缺少末尾的文件索引（文件可能被截断）")
	}
	if len(index.Files) != len(res.files) {
		return nil, uerrf("备份包不完整：索引记录了 %d 个文件，实际读到 %d 个", len(index.Files), len(res.files))
	}
	for _, f := range index.Files {
		got, ok := res.files[f.Path]
		if !ok {
			return nil, uerrf("备份包不完整：缺少 %s", f.Path)
		}
		if got.Size != f.Size || got.SHA != f.SHA256 {
			return nil, uerrf("备份包里的 %s 与索引记录的大小或 sha256 不一致，文件已损坏", f.Path)
		}
	}
	if res.instanceRaw == nil || res.manifestRaw == nil {
		return nil, uerr("备份包里缺少 lemon-note.json 或 MANIFEST")
	}
	if _, ok := res.files["config/secret.key"]; !ok {
		return nil, uerr("备份包里缺少 config/secret.key")
	}
	return res, nil
}

func packageReadErr(err error) error {
	var ue *Error
	if errors.As(err, &ue) {
		return err
	}
	switch {
	case errors.Is(err, cryptstream.ErrCorrupt):
		return uerr("备份包解密失败：文件已损坏、被篡改或不完整")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return uerr("备份包不完整（数据提前结束，文件可能被截断）")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return uerrf("读取备份包失败（文件可能已损坏）：%v", err)
}

// dirSet 记录恢复时写过的目录（并发安全），写完后统一 fsync，让改名落盘。
type dirSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func newDirSet() *dirSet { return &dirSet{m: map[string]bool{}} }

func (s *dirSet) ensure(dir string) error {
	s.mu.Lock()
	ok := s.m[dir]
	s.mu.Unlock()
	if ok {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.mu.Lock()
	s.m[dir] = true
	s.mu.Unlock()
	return nil
}

func (s *dirSet) sync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for d := range s.m {
		datadir.SyncDir(d)
	}
}

// writeFileSynced 把 r 写到 path：同目录临时文件 → fsync → 改名，返回写入的字节数。
func writeFileSynced(path string, r io.Reader, buf []byte, dirs *dirSet) (int64, error) {
	dir := filepath.Dir(path)
	if err := dirs.ensure(dir); err != nil {
		return 0, err
	}
	tmp := datadir.TempPath(dir)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	if buf == nil {
		buf = make([]byte, 256<<10)
	}
	n, err := io.CopyBuffer(f, r, buf)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = datadir.RenameRetry(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return n, err
	}
	return n, nil
}

// ---------------- 恢复 ----------------

// RestoreOptions 是恢复参数。
type RestoreOptions struct {
	// TempDir 放重建工作库、打开检查用的临时文件；为空用系统临时目录。
	TempDir string
	// SkipOpenCheck 为 true 时不做最后的离线打开检查（只在测试里用）。
	SkipOpenCheck bool
}

// RestoreResult 是恢复结果。
type RestoreResult struct {
	Dir             string   `json:"dir"`
	Backup          string   `json:"backup"`
	InstanceID      string   `json:"instance_id"`
	Engine          string   `json:"engine"`
	Lineage         string   `json:"lineage"`
	PreviousLineage string   `json:"previous_lineage"`
	Seq             int64    `json:"seq"`
	Files           int64    `json:"files"`
	Bytes           int64    `json:"bytes"`
	Blobs           int      `json:"blobs"`
	MissingBlobs    []string `json:"missing_blobs,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Hint            string   `json:"hint,omitempty"` // 恢复完成后的操作提示
}

// ignorableNames 是判断目录是否为空时忽略的系统文件。
var ignorableNames = map[string]bool{".DS_Store": true, "desktop.ini": true, "Thumbs.db": true}

// prepareDest 确认恢复目录是新的空目录，返回失败时的清理函数。
func prepareDest(dest string) (func(), error) {
	if !filepath.IsAbs(dest) {
		return nil, uerrf("恢复目录「%s」必须是绝对路径", dest)
	}
	dest = filepath.Clean(dest)
	info, err := os.Stat(dest)
	switch {
	case err == nil:
		if !info.IsDir() {
			return nil, uerrf("「%s」已存在且不是目录", dest)
		}
		entries, err := os.ReadDir(dest)
		if err != nil {
			return nil, uerrf("无法读取目录「%s」：%v", dest, err)
		}
		for _, e := range entries {
			if !ignorableNames[e.Name()] {
				return nil, uerrf("恢复目录「%s」不是空目录（发现 %s），请选择一个新的空目录", dest, e.Name())
			}
		}
		return func() {
			entries, _ := os.ReadDir(dest)
			for _, e := range entries {
				if !ignorableNames[e.Name()] {
					os.RemoveAll(filepath.Join(dest, e.Name()))
				}
			}
		}, nil
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return nil, uerrf("无法创建恢复目录「%s」：%v", dest, err)
		}
		return func() { os.RemoveAll(dest) }, nil
	default:
		return nil, uerrf("无法访问「%s」：%v", dest, err)
	}
}

// Restore 把目标上的一个备份恢复到新的空目录 dest，生成新的血统并做完整性与离线打开检查。
// 任何一步失败都会清掉 dest 里已经写入的内容。
func Restore(ctx context.Context, t target.Target, name, dest string, key []byte, opt RestoreOptions, p *Progress) (*RestoreResult, error) {
	dest = filepath.Clean(dest)
	cleanup, err := prepareDest(dest)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	var res *RestoreResult
	if mirrorRe.MatchString(name) {
		res, err = restoreMirror(ctx, t, name, dest, key, opt, p)
	} else {
		res, err = restoreFull(ctx, t, name, dest, key, opt, p)
	}
	if err != nil {
		return nil, err
	}
	ok = true
	res.Backup = name
	res.Dir = dest
	return res, nil
}

func restoreFull(ctx context.Context, t target.Target, name, dest string, key []byte, opt RestoreOptions, p *Progress) (*RestoreResult, error) {
	p.SetPhase("下载并解开备份包 " + name)
	rc, _, err := openPackage(ctx, t, name, key, nil)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	sr, err := scanPackage(ctx, rc, dest, true, p)
	if err != nil {
		return nil, err
	}
	for _, b := range sr.badBlobs {
		p.Warnf("附件 %s 的内容与文件名（sha256）不符（备份时数据目录里的文件就已损坏）", b)
	}
	return finalizeRestore(ctx, dest, sr.instanceRaw, int64(len(sr.files)), sr.bytes, opt, p)
}

func restoreMirror(ctx context.Context, t target.Target, dir, dest string, key []byte, opt RestoreOptions, p *Progress) (*RestoreResult, error) {
	p.SetPhase("读取镜像 " + dir)
	ms, err := readMirror(ctx, t, dir, key)
	if err != nil {
		return nil, err
	}
	dirs := newDirSet()
	if _, err := writeFileSynced(filepath.Join(dest, "MANIFEST"), bytes.NewReader(ms.manifestRaw), nil, dirs); err != nil {
		return nil, err
	}
	if _, err := writeFileSynced(filepath.Join(dest, "config", "secret.key"), bytes.NewReader(ms.secretKey), nil, dirs); err != nil {
		return nil, err
	}
	var total int64
	files := []srcFile{ms.snapshot}
	files = append(files, ms.chain...)
	files = append(files, ms.blobs...)
	for _, f := range files {
		if key != nil {
			total += plainSizeOf(f.Size)
		} else {
			total += f.Size
		}
	}
	p.SetTotal(int64(len(files)), total)
	p.SetPhase(fmt.Sprintf("下载快照、日志段与附件（%d 个文件）", len(files)))
	var mu sync.Mutex
	var bytesDone int64
	err = parallel(ctx, 4, len(files), func(ctx context.Context, i int) error {
		f := files[i]
		want := f.SHA
		sum, n, err := downloadFile(ctx, t, dir+"/"+f.Rel, filepath.Join(dest, filepath.FromSlash(f.Rel)), key, p, dirs)
		if err != nil {
			return err
		}
		if want != "" && sum != want {
			if f.Kind == fileBlob {
				p.Warnf("附件 %s 的内容与文件名（sha256）不符，镜像里的这个文件可能已损坏", f.SHA)
			} else {
				return uerrf("镜像里的快照 %s 与 MANIFEST 记录的 sha256 不一致，快照已损坏", f.Rel)
			}
		}
		mu.Lock()
		bytesDone += n
		mu.Unlock()
		p.AddFiles(1)
		return nil
	})
	if err != nil {
		return nil, err
	}
	dirs.sync()
	return finalizeRestore(ctx, dest, ms.instanceRaw, int64(len(files)+3), bytesDone, opt, p)
}

// mirrorState 是从目标上读到的镜像内容清单。
type mirrorState struct {
	dir         string
	instanceRaw []byte
	manifestRaw []byte
	secretKey   []byte
	man         *datadir.Manifest
	listing     map[string]int64 // 镜像目录内相对路径 → 目标上的大小
	snapshot    srcFile
	chain       []srcFile
	blobs       []srcFile
}

// readMirror 读镜像的元数据与文件清单，检查 MANIFEST 指向的快照与之后的日志段齐全。
func readMirror(ctx context.Context, t target.Target, dir string, key []byte) (*mirrorState, error) {
	if !mirrorRe.MatchString(dir) {
		return nil, uerrf("「%s」不是镜像目录名", dir)
	}
	ms := &mirrorState{dir: dir, listing: map[string]int64{}}
	var err error
	if ms.manifestRaw, err = getBytes(ctx, t, dir+"/MANIFEST", key, 1<<20); err != nil {
		if errors.Is(err, target.ErrNotExist) {
			return nil, uerrf("镜像 %s 不存在或还没有完成第一次同步（缺少 MANIFEST）", dir)
		}
		return nil, wrapUser(fmt.Errorf("读取镜像 MANIFEST 失败：%w", err))
	}
	var man datadir.Manifest
	if json.Unmarshal(ms.manifestRaw, &man) != nil || !validSnapshotRel(man.Snapshot) {
		return nil, uerr("镜像里的 MANIFEST 已损坏")
	}
	ms.man = &man
	if ms.instanceRaw, err = getBytes(ctx, t, dir+"/lemon-note.json", key, 1<<20); err != nil {
		return nil, wrapUser(fmt.Errorf("读取镜像里的 lemon-note.json 失败：%w", err))
	}
	if ms.secretKey, err = getBytes(ctx, t, dir+"/config/secret.key", key, 1<<10); err != nil {
		return nil, wrapUser(fmt.Errorf("读取镜像里的 config/secret.key 失败：%w", err))
	}
	if len(ms.secretKey) != 32 {
		return nil, uerr("镜像里的 config/secret.key 长度不对")
	}
	list, err := t.List(ctx, dir+"/")
	if err != nil {
		return nil, wrapUser(fmt.Errorf("列出镜像文件失败：%w", err))
	}
	var segs []datadir.Segment
	for _, e := range list {
		rel := strings.TrimPrefix(e.Path, dir+"/")
		ms.listing[rel] = e.Size
		kind, ok := mirrorManaged(rel)
		if !ok {
			continue
		}
		switch kind {
		case fileSegment:
			f, l, _ := datadir.ParseSegmentName(strings.TrimPrefix(rel, "journal/"))
			segs = append(segs, datadir.Segment{First: f, Last: l, Name: rel, Size: e.Size})
		case fileBlob:
			sha := rel[strings.LastIndex(rel, "/")+1:]
			ms.blobs = append(ms.blobs, srcFile{Rel: rel, Size: e.Size, Kind: fileBlob, SHA: sha})
		}
	}
	snapSize, ok := ms.listing[man.Snapshot]
	if !ok {
		return nil, uerrf("镜像不完整：缺少 MANIFEST 指向的快照 %s", man.Snapshot)
	}
	if man.SnapshotSize > 0 && snapSize != storedSize(man.SnapshotSize, key) {
		return nil, uerrf("镜像里的快照 %s 大小不对，可能上传不完整", man.Snapshot)
	}
	ms.snapshot = srcFile{Rel: man.Snapshot, Size: snapSize, Kind: fileSnapshot, SHA: man.SnapshotSHA256}
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].First != segs[j].First {
			return segs[i].First < segs[j].First
		}
		return segs[i].Last > segs[j].Last
	})
	chain, last, gap := datadir.ChainAfter(segs, man.SnapshotSeq)
	if gap {
		return nil, uerrf("镜像里的日志在序号 %d 之后不连续，有日志段缺失", last)
	}
	for _, s := range chain {
		ms.chain = append(ms.chain, srcFile{Rel: s.Name, Size: s.Size, Kind: fileSegment})
	}
	return ms, nil
}

// downloadFile 下载目标上的一个文件到本地 dst（可选解密），返回明文 sha256 与字节数。
// dst 为空时只计算哈希不落盘。
func downloadFile(ctx context.Context, t target.Target, src, dst string, key []byte, p *Progress, dirs *dirSet) (string, int64, error) {
	rc, err := t.Get(ctx, src)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, wrapUser(fmt.Errorf("下载 %s 失败：%w", src, err))
	}
	defer rc.Close()
	var r io.Reader = rc
	if key != nil {
		dr, err := cryptstream.NewDecryptReader(rc, key)
		if err != nil {
			return "", 0, decryptErr(src, err)
		}
		r = dr
	}
	h := sha256.New()
	r = io.TeeReader(countingReader{r: r, p: p}, h)
	var n int64
	if dst == "" {
		n, err = io.Copy(io.Discard, r)
	} else {
		n, err = writeFileSynced(dst, r, nil, dirs)
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		if key != nil && errors.Is(err, cryptstream.ErrCorrupt) {
			return "", 0, decryptErr(src, err)
		}
		return "", 0, wrapUser(fmt.Errorf("下载 %s 失败：%w", src, err))
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// finalizeRestore 在 dest 里已经有 MANIFEST、快照、日志段、附件、config/secret.key 的前提下：
// 用快照 + 日志段重建一个临时库（校验快照 sha256、日志段完整性与血统、序号连续），检查附件是否齐全，
// 再生成新的血统、写出包含全部变更的新快照与新 MANIFEST、删掉旧的快照与日志段，最后写 lemon-note.json 并离线打开检查一次。
//
// 换血统是为了让旧的缓存目录（按实例 ID 存放的工作库）不会被误用：如果沿用原血统，旧工作库序号更高时，
// 启动流程会以工作库为准把刚恢复的数据覆盖掉。
func finalizeRestore(ctx context.Context, dest string, instanceRaw []byte, files, bytesN int64, opt RestoreOptions, p *Progress) (*RestoreResult, error) {
	var info datadir.Instance
	if err := json.Unmarshal(instanceRaw, &info); err != nil || info.InstanceID == "" {
		return nil, uerr("备份里的 lemon-note.json 已损坏")
	}
	if info.Format > datadir.FormatVersion {
		return nil, uerr("备份的数据格式版本高于本程序支持的版本，请升级程序后再恢复")
	}
	d, err := datadir.Open(dest)
	if err != nil {
		return nil, err
	}
	man, err := d.ReadManifest()
	if err != nil {
		return nil, uerrf("备份里的 MANIFEST 不可用：%v", err)
	}
	if man.InstanceID != info.InstanceID {
		return nil, uerr("备份里的 MANIFEST 与 lemon-note.json 不属于同一个实例")
	}
	key, err := os.ReadFile(d.SecretKeyPath())
	if err != nil || len(key) != 32 {
		return nil, uerr("备份里的 config/secret.key 缺失或长度不对")
	}
	work, err := os.MkdirTemp(opt.TempDir, "lemon-note-restore-")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败：%w", err)
	}
	defer os.RemoveAll(work)
	p.SetPhase("用快照与日志重建数据库并校验")
	st, last, err := rebuildFromDir(ctx, d, man, work)
	if err != nil {
		return nil, err
	}
	stClosed := false
	defer func() {
		if !stClosed {
			st.Close()
		}
	}()
	p.Logf("快照序号 %d，重放日志到序号 %d", man.SnapshotSeq, last)
	p.SetPhase("检查附件是否齐全")
	blobCount := 0
	missing, badSize, err := checkBlobRefs(ctx, st, func(sha string) (int64, bool) {
		path, err := d.BlobPath(sha)
		if err != nil {
			return 0, false
		}
		fi, err := os.Stat(path)
		if err != nil {
			if sha == emptySHA {
				// 镜像不上传空附件：按需重建。
				if _, werr := writeFileSynced(path, bytes.NewReader(nil), nil, newDirSet()); werr == nil {
					return 0, true
				}
			}
			return 0, false
		}
		return fi.Size(), true
	})
	if err != nil {
		return nil, err
	}
	_ = d.ListBlobs(func(string, int64) error { blobCount++; return nil })
	for _, m := range missing {
		p.Warnf("数据里引用的附件 %s 在备份里找不到，恢复后这个附件会无法打开", m)
	}
	for _, b := range badSize {
		p.Warnf("附件 %s 的大小与数据库记录的不一致，可能已损坏", b)
	}
	// 备份时还在进行中的执行记录（包括产生这份备份的那一次）：最终结果不在这份数据里，如实标注。
	// 直接改临时库，不进日志：下面马上会写出包含全部内容的新快照。
	now := time.Now().UnixMilli()
	if _, err := st.ExecLocal(ctx, "UPDATE backup_runs SET status = ?, error = ?, finished_at = ? WHERE status = ?",
		StatusWarning, "这份数据是从备份恢复的：备份时这条记录还在进行中，它的最终结果没有包含在备份里", now, StatusRunning); err != nil {
		return nil, err
	}
	if _, err := st.ExecLocal(ctx, "UPDATE backup_targets SET last_status = ? WHERE last_status = ?", StatusWarning, StatusRunning); err != nil {
		return nil, err
	}
	p.SetPhase("生成新的数据血统并写出新快照")
	newLineage := ids.New()
	if err := st.SetMeta(ctx, "lineage", newLineage); err != nil {
		return nil, err
	}
	if err := st.SetMeta(ctx, "instance_id", info.InstanceID); err != nil {
		return nil, err
	}
	snapTmp := filepath.Join(work, "snapshot.db")
	seq, err := st.ExportSnapshot(ctx, snapTmp)
	if err != nil {
		return nil, fmt.Errorf("写新快照失败：%w", err)
	}
	st.Close()
	stClosed = true
	if seq != last {
		return nil, uerrf("新快照序号 %d 与重放结果 %d 不一致", seq, last)
	}
	sum, size, err := fileSHA256(snapTmp)
	if err != nil {
		return nil, err
	}
	oldSnaps, _ := d.ListSnapshots()
	oldSegs, _ := d.ListSegments()
	rel := datadir.SnapshotName(seq)
	if err := datadir.MoveIntoPlace(snapTmp, d.Path(filepath.FromSlash(rel)), 0o644); err != nil {
		return nil, fmt.Errorf("写入新快照失败：%w", err)
	}
	newMan := &datadir.Manifest{InstanceID: info.InstanceID, Lineage: newLineage, Snapshot: rel, SnapshotSeq: seq,
		SnapshotSHA256: sum, SnapshotSize: size, SchemaVersion: store.SchemaVersion}
	if err := d.WriteManifest(newMan); err != nil {
		return nil, fmt.Errorf("写入 MANIFEST 失败：%w", err)
	}
	// 旧血统的快照与日志段已经全部并入新快照，删掉它们（留着会在重放时因血统不一致报错）。
	for _, s := range oldSnaps {
		if s != seq {
			_ = datadir.RemoveRetry(d.Path(filepath.FromSlash(datadir.SnapshotName(s))))
		}
	}
	for _, s := range oldSegs {
		_ = datadir.RemoveRetry(d.Path(filepath.FromSlash(s.Name)))
	}
	for _, sub := range []string{"journal", "snapshots", "blobs", "locks"} {
		if err := os.MkdirAll(d.Path(sub), 0o755); err != nil {
			return nil, err
		}
	}
	// 实例文件最后写：只有它存在，才算一个完整的数据目录。
	if err := datadir.WriteFileAtomic(d.InstancePath(), instanceRaw, 0o644); err != nil {
		return nil, err
	}
	if !opt.SkipOpenCheck && info.Engine == string(store.SQLite) {
		p.SetPhase("离线打开恢复出的数据目录做最后检查")
		if err := openCheck(ctx, dest, opt.TempDir, seq); err != nil {
			return nil, err
		}
	}
	res := &RestoreResult{InstanceID: info.InstanceID, Engine: info.Engine, Lineage: newLineage, PreviousLineage: man.Lineage,
		Seq: seq, Files: files, Bytes: bytesN, Blobs: blobCount, MissingBlobs: missing, Warnings: p.Warnings()}
	p.Logf("恢复完成：序号 %d，附件 %d 个，新血统 %s", seq, blobCount, newLineage)
	return res, nil
}

// rebuildFromDir 把 d 里 MANIFEST 指向的快照复制到 work（核对 sha256），再按顺序重放之后的日志段（核对完整性与血统）。
func rebuildFromDir(ctx context.Context, d *datadir.Dir, man *datadir.Manifest, work string) (*store.Store, int64, error) {
	if !validSnapshotRel(man.Snapshot) {
		return nil, 0, uerrf("MANIFEST 里的快照路径「%s」不合法", man.Snapshot)
	}
	local := filepath.Join(work, "work.db")
	sum, size, err := copyFile(d.Path(filepath.FromSlash(man.Snapshot)), local)
	if err != nil {
		return nil, 0, uerrf("快照 %s 不可用：%v", man.Snapshot, err)
	}
	if man.SnapshotSHA256 != "" && sum != man.SnapshotSHA256 {
		return nil, 0, uerrf("快照 %s 的 sha256 与 MANIFEST 记录的不一致，快照已损坏", man.Snapshot)
	}
	if man.SnapshotSize > 0 && size != man.SnapshotSize {
		return nil, 0, uerrf("快照 %s 的大小与 MANIFEST 记录的不一致", man.Snapshot)
	}
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: local})
	if err != nil {
		return nil, 0, uerrf("快照 %s 无法打开：%v", man.Snapshot, err)
	}
	ok := false
	defer func() {
		if !ok {
			st.Close()
		}
	}()
	seq, err := st.Seq(ctx)
	if err != nil {
		return nil, 0, err
	}
	if seq != man.SnapshotSeq {
		return nil, 0, uerrf("快照序号不一致：MANIFEST 记录 %d，快照里是 %d", man.SnapshotSeq, seq)
	}
	segs, err := d.ListSegments()
	if err != nil {
		return nil, 0, err
	}
	chain, last, gap := datadir.ChainAfter(segs, seq)
	if gap {
		return nil, 0, uerrf("日志在序号 %d 之后不连续，有日志段缺失", last)
	}
	for _, seg := range chain {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		hdr, entries, err := store.ReadSegmentFile(d.Path(filepath.FromSlash(seg.Name)))
		if err != nil {
			return nil, 0, uerrf("日志段 %s 已损坏：%v", seg.Name, err)
		}
		if hdr.Lineage != man.Lineage {
			return nil, 0, uerrf("日志段 %s 属于另一份数据（血统不一致）", seg.Name)
		}
		if hdr.First != seg.First || hdr.Last != seg.Last {
			return nil, 0, uerrf("日志段 %s 的内容与文件名记录的序号范围不一致", seg.Name)
		}
		if err := st.ApplyEntries(ctx, entries); err != nil {
			return nil, 0, uerrf("重放日志段 %s 失败：%v", seg.Name, err)
		}
	}
	got, err := st.Seq(ctx)
	if err != nil {
		return nil, 0, err
	}
	if got != last {
		return nil, 0, uerrf("重放后序号是 %d，应为 %d", got, last)
	}
	ok = true
	return st, last, nil
}

func copyFile(src, dst string) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// checkBlobRefs 核对 files 表引用的附件：has 返回附件在备份里的大小。已删除（在回收站之外）的文件行不计缺失。
func checkBlobRefs(ctx context.Context, st *store.Store, has func(sha string) (int64, bool)) (missing, badSize []string, err error) {
	rows, err := st.W.QueryContext(ctx, "SELECT sha256, size, deleted_at FROM files")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var sha string
		var size int64
		var deleted sql.NullInt64
		if err := rows.Scan(&sha, &size, &deleted); err != nil {
			return nil, nil, err
		}
		if sha == "" || seen[sha] {
			continue
		}
		got, ok := has(sha)
		if !ok {
			if !deleted.Valid {
				seen[sha] = true
				missing = append(missing, sha)
			}
			continue
		}
		seen[sha] = true
		if got != size {
			badSize = append(badSize, sha)
		}
	}
	return missing, badSize, rows.Err()
}

// openCheck 用 persist 的正常启动流程离线打开恢复出的数据目录（独立的临时缓存目录），确认能从快照重建且序号正确。
func openCheck(ctx context.Context, dir, tempRoot string, wantSeq int64) error {
	cache, err := os.MkdirTemp(tempRoot, "lemon-note-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cache)
	inst, err := persist.Open(ctx, persist.Config{DataDir: dir, CacheDir: cache, HostID: "restore-check-" + ids.RandomHex(8),
		Version: version.Version, Logger: slog.New(slog.DiscardHandler), SealInterval: time.Hour, SnapshotInterval: 24 * time.Hour})
	if err != nil {
		return uerrf("恢复出的数据目录无法打开：%v", err)
	}
	st := inst.Status(ctx)
	cerr := inst.Close(ctx)
	_ = datadir.RemoveRetry(filepath.Join(dir, "locks", "owner.json"))
	if st.Seq != wantSeq {
		return uerrf("恢复出的数据目录打开后序号是 %d，应为 %d", st.Seq, wantSeq)
	}
	if cerr != nil {
		return uerrf("关闭恢复出的数据目录失败：%v", cerr)
	}
	return nil
}
