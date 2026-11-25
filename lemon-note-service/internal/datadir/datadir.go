// Package datadir 管理数据目录：实例信息、MANIFEST、占用锁、日志段与快照文件命名、附件存储。
// 数据目录里只出现「写后不改」的文件，以及原子替换的 MANIFEST 与占用心跳，可以安全地放进同步盘。
package datadir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const FormatVersion = 1

// Instance 对应 lemon-note.json，初始化时写一次。
type Instance struct {
	Format     int    `json:"format"`
	InstanceID string `json:"instance_id"`
	Engine     string `json:"engine"`
	CreatedAt  int64  `json:"created_at"`
	App        string `json:"app"`
}

// Manifest 指向当前快照。
type Manifest struct {
	Format         int    `json:"format"`
	InstanceID     string `json:"instance_id"`
	Lineage        string `json:"lineage"`
	Snapshot       string `json:"snapshot"`
	SnapshotSeq    int64  `json:"snapshot_seq"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
	SnapshotSize   int64  `json:"snapshot_size"`
	SchemaVersion  int    `json:"schema_version"`
	WrittenAt      int64  `json:"written_at"`
	WriterHost     string `json:"writer_host"`
}

// Dir 是一个数据目录。
type Dir struct {
	Root string
}

var ErrNotInitialized = errors.New("数据目录尚未初始化")

func Open(root string) (*Dir, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建数据目录 %s: %w", abs, err)
	}
	return &Dir{Root: abs}, nil
}

func (d *Dir) Path(parts ...string) string {
	return filepath.Join(append([]string{d.Root}, parts...)...)
}

func (d *Dir) InstancePath() string  { return d.Path("lemon-note.json") }
func (d *Dir) ManifestPath() string  { return d.Path("MANIFEST") }
func (d *Dir) SecretKeyPath() string { return d.Path("config", "secret.key") }
func (d *Dir) JournalDir() string    { return d.Path("journal") }
func (d *Dir) SnapshotDir() string   { return d.Path("snapshots") }
func (d *Dir) BlobDir() string       { return d.Path("blobs") }
func (d *Dir) LockDir() string       { return d.Path("locks") }

// ReadInstance 读取实例信息，未初始化返回 ErrNotInitialized。
func (d *Dir) ReadInstance() (*Instance, error) {
	b, err := os.ReadFile(d.InstancePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	var in Instance
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("lemon-note.json 损坏: %w", err)
	}
	if in.Format > FormatVersion {
		return nil, fmt.Errorf("数据目录格式版本 %d 高于本程序支持的 %d，请升级程序", in.Format, FormatVersion)
	}
	return &in, nil
}

func (d *Dir) WriteInstance(in *Instance) error {
	b, _ := json.MarshalIndent(in, "", "  ")
	return WriteFileAtomic(d.InstancePath(), b, 0o644)
}

func (d *Dir) ReadManifest() (*Manifest, error) {
	b, err := os.ReadFile(d.ManifestPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("MANIFEST 损坏: %w", err)
	}
	return &m, nil
}

func (d *Dir) WriteManifest(m *Manifest) error {
	m.Format = FormatVersion
	m.WrittenAt = time.Now().UnixMilli()
	m.WriterHost, _ = os.Hostname()
	b, _ := json.MarshalIndent(m, "", "  ")
	return WriteFileAtomic(d.ManifestPath(), b, 0o644)
}

// SnapshotName 返回快照文件相对路径。
func SnapshotName(seq int64) string {
	return "snapshots/" + fmt.Sprintf("%016d", seq) + ".db"
}

// Segment 是一个日志段文件。
type Segment struct {
	First, Last int64
	Name        string // 相对数据目录
	Size        int64
}

// SegmentName 返回日志段文件相对路径。
func SegmentName(first, last int64) string {
	return fmt.Sprintf("journal/%016d-%016d.lnj", first, last)
}

// ParseSegmentName 解析日志段文件名（不含目录）。
func ParseSegmentName(name string) (first, last int64, ok bool) {
	if !strings.HasSuffix(name, ".lnj") {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimSuffix(name, ".lnj"), "-", 2)
	if len(parts) != 2 || len(parts[0]) != 16 || len(parts[1]) != 16 {
		return 0, 0, false
	}
	f, err1 := strconv.ParseInt(parts[0], 10, 64)
	l, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || l < f {
		return 0, 0, false
	}
	return f, l, true
}

// ListSegments 按 seq 顺序列出日志段。
func (d *Dir) ListSegments() ([]Segment, error) {
	entries, err := os.ReadDir(d.JournalDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var segs []Segment
	for _, e := range entries {
		if e.IsDir() || IsTempName(e.Name()) {
			continue
		}
		f, l, ok := ParseSegmentName(e.Name())
		if !ok {
			continue
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		segs = append(segs, Segment{First: f, Last: l, Name: "journal/" + e.Name(), Size: size})
	}
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].First != segs[j].First {
			return segs[i].First < segs[j].First
		}
		return segs[i].Last > segs[j].Last
	})
	return segs, nil
}

// ChainAfter 从 seq 之后挑出一条连续、不重叠的日志段链。
// 合并过程中可能同时存在大段与被它覆盖的小段，优先选覆盖范围大的。
// 返回链与链末尾 seq；遇到缺口时停止，gap 为 true。
func ChainAfter(segs []Segment, seq int64) (chain []Segment, last int64, gap bool) {
	last = seq
	for {
		var best *Segment
		for i := range segs {
			s := &segs[i]
			if s.First <= last+1 && s.Last > last {
				if s.First != last+1 {
					continue // 部分重叠的段不能用，重放必须从 last+1 开始
				}
				if best == nil || s.Last > best.Last {
					best = s
				}
			}
		}
		if best == nil {
			for _, s := range segs {
				if s.First > last+1 {
					return chain, last, true
				}
			}
			return chain, last, false
		}
		chain = append(chain, *best)
		last = best.Last
	}
}

// ListSnapshots 列出快照的 seq（升序）。
func (d *Dir) ListSnapshots() ([]int64, error) {
	entries, err := os.ReadDir(d.SnapshotDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || IsTempName(n) || !strings.HasSuffix(n, ".db") || len(n) != 19 {
			continue
		}
		if v, err := strconv.ParseInt(strings.TrimSuffix(n, ".db"), 10, 64); err == nil {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// ConflictCopies 找出同步盘制造的冲突副本（例如 "MANIFEST-主机名"、"owner (1).json"）。
func (d *Dir) ConflictCopies() []string {
	var out []string
	check := func(dir string, isOurs func(string) bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || IsTempName(e.Name()) {
				continue
			}
			if !isOurs(e.Name()) {
				rel, _ := filepath.Rel(d.Root, filepath.Join(dir, e.Name()))
				out = append(out, rel)
			}
		}
	}
	rootKnown := map[string]bool{"lemon-note.json": true, "MANIFEST": true, ".DS_Store": true, "desktop.ini": true, "Thumbs.db": true, "Icon\r": true}
	check(d.Root, func(n string) bool { return rootKnown[n] })
	check(d.JournalDir(), func(n string) bool { _, _, ok := ParseSegmentName(n); return ok || n == ".DS_Store" })
	check(d.SnapshotDir(), func(n string) bool { return (strings.HasSuffix(n, ".db") && len(n) == 19) || n == ".DS_Store" })
	check(d.LockDir(), func(n string) bool { return n == "owner.json" || n == ".DS_Store" })
	return out
}
