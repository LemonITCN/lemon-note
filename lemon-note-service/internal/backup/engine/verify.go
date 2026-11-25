package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/cryptstream"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
)

// VerifyOptions 是校验参数。
type VerifyOptions struct {
	// FullHash 只对镜像有效：下载全部附件核对 sha256（全量包总是完整读一遍）。
	FullHash bool `json:"full_hash"`
	// Sample 只对镜像有效：不做全量哈希时抽查的附件个数，默认 16。
	Sample int `json:"sample"`
	// TempDir 放校验时重建数据库用的临时文件；为空用系统临时目录。
	TempDir string `json:"-"`
}

// VerifyResult 是校验结果。OK 为 false 时 Problems 说明了哪里有问题。
type VerifyResult struct {
	Backup   string   `json:"backup"`
	Mode     string   `json:"mode"`
	OK       bool     `json:"ok"`
	Files    int64    `json:"files"`
	Bytes    int64    `json:"bytes"`
	Seq      int64    `json:"seq"`
	Blobs    int      `json:"blobs"`
	Hashed   int      `json:"hashed"` // 核对过内容哈希的附件个数
	Problems []string `json:"problems,omitempty"`
}

func (r *VerifyResult) problem(format string, a ...any) {
	if len(r.Problems) < 200 {
		r.Problems = append(r.Problems, fmt.Sprintf(format, a...))
	}
}

// Verify 校验目标上的一个备份。
// 全量包：完整读一遍（解密、解压），逐文件核对 sha256 与包尾索引，再用快照 + 日志段重建数据库，核对附件齐全。
// 镜像：核对 MANIFEST 指向的快照与之后的日志段齐全、大小正确，下载它们重建数据库，核对数据库引用的附件都在、大小正确，
// 再抽查（或全部）附件的内容哈希。
// 备份有问题时返回 OK=false 的结果；只有无法开始校验（目标连不上、口令不对、操作被取消）时返回错误。
func Verify(ctx context.Context, t target.Target, name string, key []byte, opt VerifyOptions, p *Progress) (*VerifyResult, error) {
	work, err := os.MkdirTemp(opt.TempDir, "lemon-note-verify-")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败：%w", err)
	}
	defer os.RemoveAll(work)
	if mirrorRe.MatchString(name) {
		return verifyMirror(ctx, t, name, key, opt, work, p)
	}
	return verifyFull(ctx, t, name, key, work, p)
}

// fatal 判断错误是否应该中止校验而不是记为备份问题。
func fatal(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, ErrNeedPassword) || errors.Is(err, ErrWrongPassword) || errors.Is(err, ErrBackupNotFound)
}

func verifyFull(ctx context.Context, t target.Target, name string, key []byte, work string, p *Progress) (*VerifyResult, error) {
	res := &VerifyResult{Backup: name, Mode: ModeFull}
	p.SetPhase("完整读取备份包 " + name)
	rc, _, err := openPackage(ctx, t, name, key, nil)
	if err != nil {
		if fatal(ctx, err) {
			return nil, err
		}
		res.problem("%v", err)
		return res, nil
	}
	sr, err := scanPackage(ctx, rc, work, false, p)
	rc.Close()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		res.problem("%v", err)
		return res, nil
	}
	res.Files, res.Bytes, res.Blobs, res.Hashed = int64(len(sr.files)), sr.bytes, len(sr.blobs), len(sr.blobs)
	for _, b := range sr.badBlobs {
		res.problem("附件 %s 的内容与文件名（sha256）不符", b)
	}
	var man datadir.Manifest
	if json.Unmarshal(sr.manifestRaw, &man) != nil {
		res.problem("备份包里的 MANIFEST 已损坏")
		return res, nil
	}
	var info datadir.Instance
	if json.Unmarshal(sr.instanceRaw, &info) != nil || info.InstanceID != man.InstanceID {
		res.problem("备份包里的 lemon-note.json 与 MANIFEST 不属于同一个实例")
	}
	verifyDatabase(ctx, work, &man, res, func(sha string) (int64, bool) {
		n, ok := sr.blobs[sha]
		return n, ok
	}, p)
	res.OK = len(res.Problems) == 0 && ctx.Err() == nil
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return res, nil
}

// verifyDatabase 用 work 目录里的快照与日志段重建数据库，核对附件引用。has 返回附件在备份里的明文大小。
func verifyDatabase(ctx context.Context, work string, man *datadir.Manifest, res *VerifyResult, has func(string) (int64, bool), p *Progress) {
	p.SetPhase("用快照与日志重建数据库并核对附件引用")
	d, err := datadir.Open(work)
	if err != nil {
		res.problem("%v", err)
		return
	}
	dbDir := filepath.Join(work, "~$db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		res.problem("%v", err)
		return
	}
	st, last, err := rebuildFromDir(ctx, d, man, dbDir)
	if err != nil {
		res.problem("%v", err)
		return
	}
	defer st.Close()
	res.Seq = last
	missing, badSize, err := checkBlobRefs(ctx, st, has)
	if err != nil {
		res.problem("读取附件引用失败：%v", err)
		return
	}
	for _, m := range missing {
		res.problem("数据里引用的附件 %s 在备份里找不到", m)
	}
	for _, b := range badSize {
		res.problem("附件 %s 的大小与数据库记录的不一致", b)
	}
	p.Logf("数据库重建到序号 %d；缺失附件 %d 个，大小不符 %d 个", last, len(missing), len(badSize))
}

func verifyMirror(ctx context.Context, t target.Target, dir string, key []byte, opt VerifyOptions, work string, p *Progress) (*VerifyResult, error) {
	res := &VerifyResult{Backup: dir, Mode: ModeMirror}
	p.SetPhase("读取镜像 " + dir + " 的文件清单")
	ms, err := readMirror(ctx, t, dir, key)
	if err != nil {
		if fatal(ctx, err) {
			return nil, err
		}
		res.problem("%v", err)
		return res, nil
	}
	var info datadir.Instance
	if json.Unmarshal(ms.instanceRaw, &info) != nil || info.InstanceID != ms.man.InstanceID {
		res.problem("镜像里的 lemon-note.json 与 MANIFEST 不属于同一个实例")
	}
	// 下载快照与日志段到临时目录。
	dirs := newDirSet()
	if _, err := writeFileSynced(filepath.Join(work, "MANIFEST"), bytes.NewReader(ms.manifestRaw), nil, dirs); err != nil {
		return nil, err
	}
	files := append([]srcFile{ms.snapshot}, ms.chain...)
	var total int64
	for _, f := range files {
		total += f.Size
	}
	p.SetTotal(int64(len(files)), total)
	p.SetPhase(fmt.Sprintf("下载快照与日志段（%d 个文件）", len(files)))
	for _, f := range files {
		sum, n, err := downloadFile(ctx, t, dir+"/"+f.Rel, filepath.Join(work, filepath.FromSlash(f.Rel)), key, p, dirs)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			res.problem("%v", err)
			return res, nil
		}
		res.Files++
		res.Bytes += n
		p.AddFiles(1)
		if f.SHA != "" && sum != f.SHA {
			res.problem("快照 %s 的 sha256 与 MANIFEST 记录的不一致", f.Rel)
			return res, nil
		}
	}
	blobSizes := map[string]int64{}
	for _, b := range ms.blobs {
		blobSizes[b.SHA] = b.Size
	}
	res.Blobs = len(ms.blobs)
	// 目标上的大小是加密后的大小：数据库记录的明文大小换算后再比。
	verifyDatabase(ctx, work, ms.man, res, func(sha string) (int64, bool) {
		n, ok := blobSizes[sha]
		if !ok {
			if sha == emptySHA {
				return 0, true // 镜像不上传空附件，恢复时重建
			}
			return 0, false
		}
		if key != nil {
			return plainSizeOf(n), true
		}
		return n, true
	}, p)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// 附件内容哈希：全部或抽查。
	check := ms.blobs
	if !opt.FullHash {
		n := opt.Sample
		if n <= 0 {
			n = 16
		}
		if n < len(check) {
			perm := rand.Perm(len(check))[:n]
			picked := make([]srcFile, 0, n)
			for _, i := range perm {
				picked = append(picked, check[i])
			}
			check = picked
		}
	}
	if len(check) > 0 {
		var sz int64
		for _, b := range check {
			sz += b.Size
		}
		p.SetTotal(int64(len(check)), sz)
		p.SetPhase(fmt.Sprintf("核对附件内容（%d 个）", len(check)))
		var mu sync.Mutex
		err := parallel(ctx, 4, len(check), func(ctx context.Context, i int) error {
			b := check[i]
			sum, _, err := downloadFile(ctx, t, dir+"/"+b.Rel, "", key, p, nil)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				mu.Lock()
				res.problem("%v", err)
				mu.Unlock()
				return nil
			}
			mu.Lock()
			res.Hashed++
			if sum != b.SHA {
				res.problem("附件 %s 的内容与文件名（sha256）不符", b.SHA)
			}
			mu.Unlock()
			p.AddFiles(1)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	res.OK = len(res.Problems) == 0
	return res, nil
}

// plainSizeOf 由加密文件大小反推明文大小（与 cryptstream.EncryptedSize 互逆）。
func plainSizeOf(enc int64) int64 {
	const chunk = cryptstream.ChunkSize
	const tag = 16
	body := enc - cryptstream.HeaderSize
	if body < tag {
		return -1
	}
	// 每块密文 chunk+tag，最后一块 1..chunk+tag（空明文时为 tag）。
	full := body / (chunk + tag)
	rest := body % (chunk + tag)
	if rest == 0 {
		// 恰好整块：最后一块是满块
		return full * chunk
	}
	if rest < tag {
		return -1
	}
	return full*chunk + rest - tag
}
