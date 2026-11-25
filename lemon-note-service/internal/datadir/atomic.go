package datadir

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
)

// TempPrefix 是写入数据目录时临时文件的前缀。OneDrive、Dropbox 都不会上传 ~$ 开头的文件，
// 因此同步盘只会看到原子改名之后的完整文件。
const TempPrefix = "~$"

// IsTempName 判断是不是本程序写数据目录时留下的临时文件。
func IsTempName(name string) bool {
	return strings.HasPrefix(name, TempPrefix)
}

// TempPath 在 dir 下生成一个临时文件路径。
func TempPath(dir string) string {
	return filepath.Join(dir, TempPrefix+ids.RandomHex(8)+".tmp")
}

// WriteFileAtomic 原子写入：临时文件 → fsync → 改名 → fsync 目录。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return WriteAtomic(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteAtomic 用回调写内容，其余同 WriteFileAtomic。
func WriteAtomic(path string, perm os.FileMode, write func(w io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := TempPath(dir)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := write(f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := RenameRetry(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	ok = true
	SyncDir(dir)
	return nil
}

// MoveIntoPlace 把一个已经写好并 fsync 过的文件放到目标位置。
// 同一文件系统直接改名；跨文件系统先复制到目标目录的临时名再改名。
func MoveIntoPlace(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		SyncDir(filepath.Dir(dst))
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := WriteAtomic(dst, perm, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	}); err != nil {
		return err
	}
	in.Close()
	return os.Remove(src)
}

// RenameRetry 改名；Windows 上同步客户端可能正占着目标文件，短暂重试。
func RenameRetry(from, to string) error {
	var err error
	delay := 50 * time.Millisecond
	for i := 0; i < 8; i++ {
		err = os.Rename(from, to)
		if err == nil {
			return nil
		}
		if runtime.GOOS != "windows" && !errors.Is(err, os.ErrPermission) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
	return fmt.Errorf("改名失败（目标文件可能被同步盘占用）: %w", err)
}

// RemoveRetry 删除文件，不存在视为成功；Windows 上短暂重试。
func RemoveRetry(path string) error {
	var err error
	delay := 50 * time.Millisecond
	for i := 0; i < 6; i++ {
		err = os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(delay)
		delay *= 2
	}
	return err
}

// FsyncFile 打开文件并 fsync。
func FsyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// CleanupTemps 删除 dir 下超过 maxAge 的临时文件（递归一层子目录）。
func CleanupTemps(dir string, maxAge time.Duration) {
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && strings.Count(strings.TrimPrefix(p, dir), string(filepath.Separator)) > 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if IsTempName(d.Name()) {
			if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
				os.Remove(p)
			}
		}
		return nil
	})
}
