package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// 文件类别。
const (
	fileMeta = iota
	fileSnapshot
	fileSegment
	fileBlob
)

// srcFile 是数据目录里的一个不可变文件。
type srcFile struct {
	Rel  string // 相对数据目录，用 / 分隔
	Size int64
	Kind int
	SHA  string // 已知的 sha256（附件是文件名，快照来自 MANIFEST），未知为空
}

// fileSet 是备份租约内收集到的一致文件集合。
type fileSet struct {
	Info        *datadir.Instance
	InstanceRaw []byte // lemon-note.json 原文
	ManifestRaw []byte // MANIFEST 原文（读取时刻的版本，之后数据目录里的 MANIFEST 可能被替换）
	Manifest    *datadir.Manifest
	SecretKey   []byte

	Snapshot  srcFile   // MANIFEST 指向的快照
	Chain     []srcFile // 快照之后的连续日志段
	LastSeq   int64     // 链末尾序号
	Snapshots []srcFile // 数据目录里全部快照（镜像用）
	Segments  []srcFile // 数据目录里全部日志段（镜像用）
	Blobs     []srcFile
	BlobBytes int64
}

// collectFiles 读取 MANIFEST 并列出一致的文件集合。调用方必须已经持有备份租约并 Flush 过。
func collectFiles(inst *persist.Instance) (*fileSet, error) {
	d := inst.Dir
	fs := &fileSet{}
	var err error
	if fs.InstanceRaw, err = os.ReadFile(d.InstancePath()); err != nil {
		return nil, fmt.Errorf("读取 lemon-note.json 失败：%w", err)
	}
	var info datadir.Instance
	if err := json.Unmarshal(fs.InstanceRaw, &info); err != nil {
		return nil, uerr("数据目录里的 lemon-note.json 已损坏")
	}
	fs.Info = &info
	if fs.ManifestRaw, err = os.ReadFile(d.ManifestPath()); err != nil {
		return nil, fmt.Errorf("读取 MANIFEST 失败：%w", err)
	}
	var man datadir.Manifest
	if err := json.Unmarshal(fs.ManifestRaw, &man); err != nil {
		return nil, uerr("数据目录里的 MANIFEST 已损坏")
	}
	if man.InstanceID != info.InstanceID {
		return nil, uerr("MANIFEST 与 lemon-note.json 不属于同一个实例")
	}
	if !validSnapshotRel(man.Snapshot) {
		return nil, uerrf("MANIFEST 里的快照路径「%s」不合法", man.Snapshot)
	}
	fs.Manifest = &man
	if fs.SecretKey, err = os.ReadFile(d.SecretKeyPath()); err != nil {
		return nil, fmt.Errorf("读取 config/secret.key 失败：%w", err)
	}
	if len(fs.SecretKey) != 32 {
		return nil, uerr("数据目录里的 config/secret.key 长度不对")
	}
	snapInfo, err := os.Stat(d.Path(filepath.FromSlash(man.Snapshot)))
	if err != nil {
		return nil, uerrf("MANIFEST 指向的快照 %s 不可用：%v", man.Snapshot, err)
	}
	fs.Snapshot = srcFile{Rel: man.Snapshot, Size: snapInfo.Size(), Kind: fileSnapshot, SHA: man.SnapshotSHA256}
	segs, err := d.ListSegments()
	if err != nil {
		return nil, fmt.Errorf("列出日志段失败：%w", err)
	}
	chain, last, gap := datadir.ChainAfter(segs, man.SnapshotSeq)
	if gap {
		return nil, uerrf("数据目录的日志在序号 %d 之后不连续，可能有日志段丢失；请先在系统状态页处理（例如立即写一个新快照）后再备份", last)
	}
	for _, s := range chain {
		fs.Chain = append(fs.Chain, srcFile{Rel: s.Name, Size: s.Size, Kind: fileSegment})
	}
	fs.LastSeq = last
	for _, s := range segs {
		fs.Segments = append(fs.Segments, srcFile{Rel: s.Name, Size: s.Size, Kind: fileSegment})
	}
	snaps, err := d.ListSnapshots()
	if err != nil {
		return nil, fmt.Errorf("列出快照失败：%w", err)
	}
	for _, seq := range snaps {
		rel := datadir.SnapshotName(seq)
		f := srcFile{Rel: rel, Kind: fileSnapshot}
		if rel == man.Snapshot {
			f = fs.Snapshot
		} else if st, err := os.Stat(d.Path(filepath.FromSlash(rel))); err == nil {
			f.Size = st.Size()
		} else {
			continue
		}
		fs.Snapshots = append(fs.Snapshots, f)
	}
	err = d.ListBlobs(func(sha string, size int64) error {
		fs.Blobs = append(fs.Blobs, srcFile{Rel: datadir.BlobRel(sha), Size: size, Kind: fileBlob, SHA: sha})
		fs.BlobBytes += size
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("列出附件失败：%w", err)
	}
	return fs, nil
}

// validateSource 预先校验快照 sha256 与日志段链（血统、完整性），避免把已经损坏的数据传上去。
func validateSource(ctx context.Context, inst *persist.Instance, fs *fileSet) error {
	if fs.Manifest.SnapshotSize > 0 && fs.Snapshot.Size != fs.Manifest.SnapshotSize {
		return uerrf("快照 %s 的大小与 MANIFEST 记录的不一致", fs.Snapshot.Rel)
	}
	if fs.Manifest.SnapshotSHA256 != "" {
		sum, _, err := fileSHA256(inst.Dir.Path(filepath.FromSlash(fs.Snapshot.Rel)))
		if err != nil {
			return fmt.Errorf("读取快照失败：%w", err)
		}
		if sum != fs.Manifest.SnapshotSHA256 {
			return uerrf("快照 %s 的 sha256 与 MANIFEST 记录的不一致（快照文件可能已损坏）", fs.Snapshot.Rel)
		}
	}
	for _, s := range fs.Chain {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := readSegmentHeader(inst.Dir.Path(filepath.FromSlash(s.Rel)))
		if err != nil {
			return uerrf("日志段 %s 校验失败：%v", s.Rel, err)
		}
		if hdr.Lineage != fs.Manifest.Lineage {
			return uerrf("日志段 %s 属于另一份数据（血统不一致）", s.Rel)
		}
	}
	return nil
}

func readSegmentHeader(path string) (store.SegmentHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return store.SegmentHeader{}, err
	}
	defer f.Close()
	return store.ReadSegment(f, nil)
}

// collectValidated 收集文件集合并预校验；快照或日志段损坏时先让实例写一个新快照（新快照之后的链很短甚至为空），再试一次。
func collectValidated(ctx context.Context, inst *persist.Instance, p *Progress) (*fileSet, error) {
	fs, err := collectFiles(inst)
	if err == nil {
		err = validateSource(ctx, inst, fs)
	}
	if err == nil {
		return fs, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	p.Warnf("数据目录里的快照或日志段有问题（%v），先写一个新快照再备份", err)
	if serr := inst.Snapshot(ctx); serr != nil {
		return nil, uerrf("%v；尝试写新快照也失败了：%v", err, serr)
	}
	fs, err2 := collectFiles(inst)
	if err2 == nil {
		err2 = validateSource(ctx, inst, fs)
	}
	if err2 != nil {
		return nil, err2
	}
	return fs, nil
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

// isNotExist 兼容目标驱动与本地文件系统的「不存在」。
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
