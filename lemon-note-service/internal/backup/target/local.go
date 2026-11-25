package target

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// LocalConfig 是本机目录 / 已挂载 NAS 盘的配置。
type LocalConfig struct {
	// Path 是备份根目录的绝对路径。目录必须已经存在：NAS 没挂载时不会在本机磁盘上
	// 悄悄建一个同名目录把备份写进去。
	Path string `json:"path"`
}

type localTarget struct {
	dir string
}

func openLocal(c LocalConfig) (Target, error) {
	p := strings.TrimSpace(c.Path)
	if p == "" {
		return nil, errors.New("请填写备份目录")
	}
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("备份目录「%s」必须是绝对路径", p)
	}
	return &localTarget{dir: filepath.Clean(p)}, nil
}

// root 每次操作都重新打开根目录：NAS 重新挂载后旧句柄会失效。
// os.Root 保证所有访问都限制在根目录内，符号链接也逃不出去。
func (t *localTarget) root() (*os.Root, error) {
	info, err := os.Stat(t.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("备份目录「%s」不存在（如果是 NAS，请确认已经挂载）", t.dir)
		}
		return nil, fmt.Errorf("无法访问备份目录「%s」：%v", t.dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("「%s」不是目录", t.dir)
	}
	r, err := os.OpenRoot(t.dir)
	if err != nil {
		return nil, fmt.Errorf("无法打开备份目录「%s」：%v", t.dir, err)
	}
	return r, nil
}

func localErr(op, p string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s「%s」失败：没有权限", op, p)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("%s「%s」失败：%v", op, p, err)
}

func (t *localTarget) Put(ctx context.Context, p string, r io.Reader, size int64) (err error) {
	p, err = cleanFilePath(p)
	if err != nil {
		return err
	}
	if size < 0 {
		return errors.New("写入文件时必须给出文件大小")
	}
	root, err := t.root()
	if err != nil {
		return err
	}
	defer root.Close()
	dir := dirName(p)
	if dir != "" {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return localErr("创建目录", dir, err)
		}
	}
	tmp := joinKey(dir, tempName())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return localErr("创建临时文件", tmp, err)
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			root.Remove(tmp)
		}
	}()
	if _, err := io.Copy(f, ctxReader{ctx, newSizedReader(r, size)}); err != nil {
		if errors.Is(err, ErrSizeMismatch) || ctx.Err() != nil {
			return ctxErr(ctx, fmt.Errorf("写入「%s」失败：%w", p, err))
		}
		return localErr("写入", p, err)
	}
	if err := f.Sync(); err != nil {
		return localErr("写入", p, err)
	}
	if err := f.Close(); err != nil {
		return localErr("写入", p, err)
	}
	if err := renameRetry(root, tmp, p); err != nil {
		return localErr("保存", p, err)
	}
	ok = true
	syncRootDir(root, dir)
	return nil
}

// renameRetry 改名；Windows 上同步客户端、杀毒软件可能短暂占着目标文件，稍等重试。
func renameRetry(root *os.Root, from, to string) error {
	var err error
	delay := 50 * time.Millisecond
	for i := 0; i < 8; i++ {
		err = root.Rename(from, to)
		if err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
	return err
}

// syncRootDir fsync 目录，让改名落盘；Windows 不支持对目录 fsync，忽略错误。
func syncRootDir(root *os.Root, dir string) {
	if dir == "" {
		dir = "."
	}
	if d, err := root.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

func (t *localTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	root, err := t.root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(p)
	if err != nil {
		return nil, localErr("读取", p, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, localErr("读取", p, err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("读取「%s」失败：%w（不是普通文件）", p, ErrNotExist)
	}
	return f, nil
}

func (t *localTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	root, err := t.root()
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	info, err := root.Stat(p)
	if err != nil {
		return Entry{}, localErr("查询", p, err)
	}
	if !info.Mode().IsRegular() {
		return Entry{}, fmt.Errorf("查询「%s」失败：%w（不是普通文件）", p, ErrNotExist)
	}
	return Entry{Path: p, Size: info.Size(), ModTime: info.ModTime()}, nil
}

func (t *localTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	root, err := t.root()
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return localErr("删除", p, err)
	}
	if info.IsDir() {
		return fmt.Errorf("删除「%s」失败：这是一个目录", p)
	}
	var rerr error
	delay := 50 * time.Millisecond
	for i := 0; i < 6; i++ {
		rerr = root.Remove(p)
		if rerr == nil || errors.Is(rerr, fs.ErrNotExist) {
			return nil
		}
		if runtime.GOOS != "windows" {
			break
		}
		time.Sleep(delay)
		delay *= 2
	}
	return localErr("删除", p, rerr)
}

func (t *localTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	root, err := t.root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	start := prefixDir(prefix)
	if start == "" {
		start = "."
	}
	fsys := root.FS()
	var out []Entry
	err = fs.WalkDir(fsys, start, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == start && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			// 不在前缀范围内的子目录直接跳过。
			if p != start && !strings.HasPrefix(p+"/", prefix) && !strings.HasPrefix(prefix, p+"/") {
				return fs.SkipDir
			}
			return nil
		}
		if isTempName(d.Name()) {
			if info, err := d.Info(); err == nil && isStaleTemp(d.Name(), info.ModTime()) {
				root.Remove(p) // 上次中断留下的临时文件
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasPrefix(p, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // 遍历期间被删掉了
			}
			return err
		}
		out = append(out, Entry{Path: p, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, localErr("列出", prefix, err)
	}
	return out, nil
}

func (t *localTarget) Close() error { return nil }
