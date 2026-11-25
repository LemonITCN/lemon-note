package datadir

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BlobRel 返回附件相对数据目录的路径。
func BlobRel(sha string) string {
	return "blobs/" + sha[:2] + "/" + sha
}

func validSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil && strings.ToLower(sha) == sha
}

// BlobPath 返回附件绝对路径。
func (d *Dir) BlobPath(sha string) (string, error) {
	if !validSHA(sha) {
		return "", errors.New("非法的附件哈希")
	}
	return filepath.Join(d.Root, filepath.FromSlash(BlobRel(sha))), nil
}

// HasBlob 判断附件是否存在。
func (d *Dir) HasBlob(sha string) bool {
	p, err := d.BlobPath(sha)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// PutBlob 把 r 流式写进附件仓库，返回 sha256 与大小。tmpDir 是缓存目录里的临时目录。
// 内容相同的附件只存一份。maxSize<=0 表示不限。
func (d *Dir) PutBlob(r io.Reader, tmpDir string, maxSize int64) (sha string, size int64, err error) {
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", 0, err
	}
	tmp := TempPath(tmpDir)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp)
	h := sha256.New()
	src := r
	if maxSize > 0 {
		src = io.LimitReader(r, maxSize+1)
	}
	n, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		f.Close()
		return "", 0, err
	}
	if maxSize > 0 && n > maxSize {
		f.Close()
		return "", 0, fmt.Errorf("文件超过大小上限 %d 字节", maxSize)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", 0, err
	}
	f.Close()
	sha = hex.EncodeToString(h.Sum(nil))
	dst, _ := d.BlobPath(sha)
	if _, err := os.Stat(dst); err == nil {
		return sha, n, nil
	}
	if err := MoveIntoPlace(tmp, dst, 0o644); err != nil {
		return "", 0, err
	}
	return sha, n, nil
}

// OpenBlob 打开附件。
func (d *Dir) OpenBlob(sha string) (*os.File, error) {
	p, err := d.BlobPath(sha)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// DeleteBlob 删除附件（只由附件回收任务调用）。
func (d *Dir) DeleteBlob(sha string) error {
	p, err := d.BlobPath(sha)
	if err != nil {
		return err
	}
	return RemoveRetry(p)
}

// ListBlobs 遍历全部附件哈希。
func (d *Dir) ListBlobs(fn func(sha string, size int64) error) error {
	root := d.BlobDir()
	shards, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, s := range shards {
		if !s.IsDir() || len(s.Name()) != 2 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, s.Name()))
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.IsDir() || !validSHA(f.Name()) {
				continue
			}
			var size int64
			if info, err := f.Info(); err == nil {
				size = info.Size()
			}
			if err := fn(f.Name(), size); err != nil {
				return err
			}
		}
	}
	return nil
}
