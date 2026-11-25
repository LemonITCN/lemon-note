package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
)

// mirrorPointer 是目标根目录下的镜像指针，列出备份时用它发现镜像，不用遍历镜像目录。
type mirrorPointer struct {
	Format     int    `json:"format"`
	App        string `json:"app"`
	InstanceID string `json:"instance_id"`
	Dir        string `json:"dir"`
}

// mirrorManaged 判断镜像目录里的相对路径是不是由镜像同步管理的不可变文件（只有这些文件会被删除）。
func mirrorManaged(rel string) (kind int, ok bool) {
	switch {
	case strings.HasPrefix(rel, "journal/"):
		name := strings.TrimPrefix(rel, "journal/")
		if strings.Contains(name, "/") {
			return 0, false
		}
		_, _, ok := datadir.ParseSegmentName(name)
		return fileSegment, ok
	case strings.HasPrefix(rel, "snapshots/"):
		name := strings.TrimPrefix(rel, "snapshots/")
		return fileSnapshot, snapshotNameOK(name)
	case strings.HasPrefix(rel, "blobs/"):
		parts := strings.Split(rel, "/")
		if len(parts) != 3 || len(parts[1]) != 2 || !validSHA(parts[2]) || parts[2][:2] != parts[1] {
			return 0, false
		}
		return fileBlob, true
	}
	return 0, false
}

// validSnapshotRel 校验 MANIFEST 里的快照路径（来自备份，不可信）：只能是 snapshots/<16 位数字>.db。
func validSnapshotRel(rel string) bool {
	return strings.HasPrefix(rel, "snapshots/") && snapshotNameOK(strings.TrimPrefix(rel, "snapshots/"))
}

func snapshotNameOK(name string) bool {
	if len(name) != 19 || !strings.HasSuffix(name, ".db") {
		return false
	}
	for _, c := range name[:16] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// emptySHA 是空内容的 sha256。
const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type mirrorUpload struct {
	f       srcFile
	wantSHA string
}

// backupMirror 把数据目录增量同步到目标上的镜像目录。
//
// 顺序：附件 → 日志段 → 快照 → config/secret.key、lemon-note.json → MANIFEST → 删除源端已不存在的文件。
// 目标上原来的 MANIFEST 指向的文件在新 MANIFEST 上传之前不会被删，所以镜像在任何时刻都能当数据目录用。
func (e *Engine) backupMirror(ctx context.Context, p *Progress, t target.Target, key []byte, workers int) (*runResult, error) {
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
	dir := mirrorDir(id8)
	join := func(rel string) string { return dir + "/" + rel }

	p.SetPhase("读取目标上的镜像 " + dir)
	list, err := t.List(ctx, dir+"/")
	if err != nil {
		return nil, wrapUser(fmt.Errorf("列出目标上的镜像失败：%w", err))
	}
	existing := map[string]int64{}
	for _, en := range list {
		existing[strings.TrimPrefix(en.Path, dir+"/")] = en.Size
	}
	if _, ok := existing["lemon-note.json"]; ok {
		raw, err := getBytes(ctx, t, join("lemon-note.json"), key, 1<<20)
		if err != nil {
			return nil, wrapUser(fmt.Errorf("读取镜像里的 lemon-note.json 失败：%w", err))
		}
		var other datadir.Instance
		if json.Unmarshal(raw, &other) != nil {
			return nil, uerr("镜像里的 lemon-note.json 已损坏")
		}
		if other.InstanceID != fs.Info.InstanceID {
			return nil, uerrf("目标上的镜像目录 %s 属于另一个实例（%s），不能覆盖", dir, other.InstanceID)
		}
	}
	var targetMan *datadir.Manifest
	var targetManRaw []byte
	if _, ok := existing["MANIFEST"]; ok {
		raw, err := getBytes(ctx, t, join("MANIFEST"), key, 1<<20)
		var m datadir.Manifest
		switch {
		case err == nil && json.Unmarshal(raw, &m) == nil:
			targetMan, targetManRaw = &m, raw
		case err == nil || isCorruptErr(err):
			p.Warnf("镜像里的 MANIFEST 已损坏，将重新上传")
		default:
			return nil, wrapUser(fmt.Errorf("读取镜像里的 MANIFEST 失败：%w", err))
		}
	}
	// 只有目标上的 MANIFEST 可读、而且血统与当前数据一致时，才相信镜像里同名同大小的快照与日志段就是当前数据的。
	// 附件按内容寻址，任何情况下同名同大小都可以直接沿用。
	trusted := targetMan != nil && targetMan.Lineage == fs.Manifest.Lineage
	switch {
	case targetMan != nil && !trusted:
		// 同名的快照与日志段内容不同（数据从备份恢复过）：先删掉旧 MANIFEST，让镜像在替换期间明确处于「不完整」状态，
		// 而不是指向内容对不上的文件。
		p.Warnf("目标上的镜像来自另一份数据（血统不同，可能是从备份恢复过），将整体替换快照与日志段")
		if err := t.Delete(ctx, join("MANIFEST")); err != nil {
			return nil, wrapUser(fmt.Errorf("删除镜像里旧的 MANIFEST 失败：%w", err))
		}
		targetMan, targetManRaw = nil, nil
	case targetMan == nil:
		for rel := range existing {
			if k, ok := mirrorManaged(rel); ok && k != fileBlob {
				p.Logf("镜像里没有可用的 MANIFEST，无法确认已有的快照与日志段属于当前数据，将全部重新上传")
				break
			}
		}
	}
	lineageChanged := !trusted

	needs := func(f srcFile) bool {
		have, ok := existing[f.Rel]
		return !ok || have != storedSize(f.Size, key)
	}
	var blobs, segs, snaps []mirrorUpload
	var upBytes int64
	for _, b := range fs.Blobs {
		// 空附件不上传（百度网盘等不接受空文件）：恢复时按需重建，内容就是空的。
		if b.Size == 0 {
			continue
		}
		if needs(b) {
			blobs = append(blobs, mirrorUpload{f: b, wantSHA: b.SHA})
			upBytes += b.Size
		}
	}
	for _, s := range fs.Segments {
		if lineageChanged || needs(s) {
			segs = append(segs, mirrorUpload{f: s})
			upBytes += s.Size
		}
	}
	for _, s := range fs.Snapshots {
		upload := lineageChanged || needs(s)
		if s.Rel == fs.Snapshot.Rel {
			// 当前快照：除非目标上的 MANIFEST 已经指向同一个快照且 sha256 相同（说明那份是我们传上去并核对过的），否则重传。
			sameAsTarget := targetMan != nil && targetMan.Snapshot == s.Rel && targetMan.SnapshotSHA256 == fs.Manifest.SnapshotSHA256
			upload = upload || !sameAsTarget
			if upload {
				snaps = append(snaps, mirrorUpload{f: s, wantSHA: fs.Manifest.SnapshotSHA256})
				upBytes += s.Size
			}
			continue
		}
		if upload {
			snaps = append(snaps, mirrorUpload{f: s})
			upBytes += s.Size
		}
	}
	type metaFile struct {
		rel  string
		data []byte
	}
	var metas []metaFile
	for _, m := range []metaFile{{"config/secret.key", fs.SecretKey}, {"lemon-note.json", fs.InstanceRaw}} {
		have, ok := existing[m.rel]
		if !ok || have != storedSize(int64(len(m.data)), key) {
			metas = append(metas, m)
		}
	}
	manifestChanged := targetManRaw == nil || !bytes.Equal(targetManRaw, fs.ManifestRaw)
	total := int64(len(blobs) + len(segs) + len(snaps) + len(metas))
	if manifestChanged {
		total++
	}
	p.Logf("源端：附件 %d 个、日志段 %d 个、快照 %d 个；需要上传 %d 个文件（%s）",
		len(fs.Blobs), len(fs.Segments), len(fs.Snapshots), total, humanBytes(upBytes))
	p.SetTotal(total, upBytes)

	var res runResult
	res.Artifact = dir
	var resMu sync.Mutex
	upload := func(ctx context.Context, u mirrorUpload) error {
		n, err := putFile(ctx, t, join(u.f.Rel), e.inst.Dir.Path(filepath.FromSlash(u.f.Rel)), key, u.wantSHA, p)
		var hm *errHashMismatch
		if errors.As(err, &hm) {
			if u.f.Kind == fileBlob {
				p.Warnf("附件 %s 的内容与文件名（sha256）不符，数据目录里的这个文件可能已损坏；已按原样同步", u.f.SHA)
				err = nil
			} else {
				return uerrf("快照 %s 在同步过程中内容变了（sha256 与 MANIFEST 不一致），本次同步作废", u.f.Rel)
			}
		}
		if err != nil {
			if isNotExist(err) && u.f.Kind == fileBlob {
				p.Warnf("附件 %s 在同步过程中被删除，已跳过", u.f.SHA)
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return wrapUser(fmt.Errorf("上传 %s 失败：%w", u.f.Rel, err))
		}
		p.AddFiles(1)
		resMu.Lock()
		res.Files++
		res.Bytes += storedSize(n, key)
		resMu.Unlock()
		return nil
	}
	if len(blobs) > 0 {
		p.SetPhase(fmt.Sprintf("上传附件（%d 个）", len(blobs)))
		if err := parallel(ctx, workers, len(blobs), func(ctx context.Context, i int) error { return upload(ctx, blobs[i]) }); err != nil {
			return nil, err
		}
	}
	if len(segs) > 0 {
		p.SetPhase(fmt.Sprintf("上传日志段（%d 个）", len(segs)))
		if err := parallel(ctx, workers, len(segs), func(ctx context.Context, i int) error { return upload(ctx, segs[i]) }); err != nil {
			return nil, err
		}
	}
	if len(snaps) > 0 {
		p.SetPhase(fmt.Sprintf("上传快照（%d 个）", len(snaps)))
		for _, s := range snaps {
			if err := upload(ctx, s); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range metas {
		if err := putBytes(ctx, t, join(m.rel), m.data, key); err != nil {
			return nil, wrapUser(fmt.Errorf("上传 %s 失败：%w", m.rel, err))
		}
		p.AddFiles(1)
		p.AddBytes(int64(len(m.data)))
		res.Files++
		res.Bytes += storedSize(int64(len(m.data)), key)
	}
	if manifestChanged {
		p.SetPhase("上传 MANIFEST")
		if err := putBytes(ctx, t, join("MANIFEST"), fs.ManifestRaw, key); err != nil {
			return nil, wrapUser(fmt.Errorf("上传 MANIFEST 失败：%w", err))
		}
		p.AddFiles(1)
		res.Files++
		res.Bytes += storedSize(int64(len(fs.ManifestRaw)), key)
	}

	// 新 MANIFEST 已就位：删除源端已经不存在的文件（只动日志段、快照、附件这三类）。
	src := map[string]bool{}
	for _, group := range [][]srcFile{fs.Blobs, fs.Segments, fs.Snapshots} {
		for _, f := range group {
			src[f.Rel] = true
		}
	}
	var stale []string
	for rel := range existing {
		if _, ok := mirrorManaged(rel); ok && !src[rel] {
			stale = append(stale, rel)
		}
	}
	if len(stale) > 0 {
		p.SetPhase(fmt.Sprintf("删除源端已不存在的文件（%d 个）", len(stale)))
		var delMu sync.Mutex
		deleted := 0
		err := parallel(ctx, workers, len(stale), func(ctx context.Context, i int) error {
			if err := t.Delete(ctx, join(stale[i])); err != nil {
				return wrapUser(fmt.Errorf("删除 %s 失败：%w", stale[i], err))
			}
			delMu.Lock()
			deleted++
			delMu.Unlock()
			return nil
		})
		p.Logf("已删除 %d 个文件", deleted)
		if err != nil {
			p.Warnf("删除镜像里多余的文件没有全部完成：%v（不影响镜像可用，下次会再清理）", err)
		}
	}
	if _, err := t.Stat(ctx, pointerName(id8)); errors.Is(err, target.ErrNotExist) {
		b, _ := json.MarshalIndent(mirrorPointer{Format: 1, App: "lemon-note", InstanceID: fs.Info.InstanceID, Dir: dir}, "", "  ")
		if err := putBytes(ctx, t, pointerName(id8), b, nil); err != nil {
			p.Warnf("写入镜像指针 %s 失败：%v", pointerName(id8), err)
		}
	} else if err != nil {
		p.Warnf("查询镜像指针 %s 失败：%v", pointerName(id8), err)
	}
	p.Logf("镜像已同步到序号 %d：上传 %d 个文件（%s），删除 %d 个多余文件", fs.LastSeq, res.Files, humanBytes(res.Bytes), len(stale))
	return &res, nil
}

// parallel 用 n 个并发执行 fn(0..count-1)，第一个错误会取消其余任务。
func parallel(ctx context.Context, n, count int, fn func(ctx context.Context, i int) error) error {
	if n < 1 {
		n = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		next     int
	)
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if firstErr != nil || next >= count {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()
				if err := fn(ctx, i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}
