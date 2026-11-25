package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// vfs 是导入包的只读视图：zip（可套一层 zip）或单个文件。zip 里的文件从不按原名落盘，
// 路径一律规范化，含 ..、绝对路径、盘符的条目与符号链接直接跳过；文件数、解压总量都有上限。
type vfs struct {
	lim     Limits
	warn    func(Warning)
	files   map[string]*vfile
	dirs    map[string]bool
	lower   map[string]string   // 小写路径 → 路径（大小写不敏感查找）
	byBase  map[string][]string // 小写文件名 → 路径
	closers []io.Closer
	temps   []string
	tmpDir  string
	read    atomic.Int64 // 实际解压出的字节数
	decl    uint64       // 声明的解压总量
	count   int
	nested  int
	root    string // 剥掉的单一顶层目录名
}

type vfile struct {
	path string
	zf   *zip.File
	disk string // 单文件导入时的磁盘路径
	size int64
}

var (
	errTooMany = errors.New("导入包里的文件太多")
	errTooBig  = errors.New("导入包解压后太大（可能是 zip 炸弹），已停止")
)

func newVFS(lim Limits, tmpDir string, warn func(Warning)) *vfs {
	return &vfs{lim: lim, tmpDir: tmpDir, warn: warn, files: map[string]*vfile{}, dirs: map[string]bool{}, lower: map[string]string{}, byBase: map[string][]string{}}
}

// isZip 按文件头判断是不是 zip。
func isZip(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, []byte("PK\x03\x04")) || bytes.Equal(head, []byte("PK\x05\x06"))
}

// openPackage 打开上传的导入包。name 是上传时的文件名（单文件导入时用作页面标题）。
// expandNested 为真时把 zip 里的 zip 也展开（Notion 大导出会分包）；否则只有外层全是 zip 时才展开。
func openPackage(p, name string, lim Limits, tmpDir string, warn func(Warning), expandNested bool) (*vfs, error) {
	v := newVFS(lim, tmpDir, warn)
	if !isZip(p) {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		n := files.CleanName(name)
		v.files[n] = &vfile{path: n, disk: p, size: st.Size()}
		v.index()
		return v, nil
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fail("无法打开 zip 文件，可能已损坏")
	}
	v.closers = append(v.closers, zr)
	if !expandNested {
		expandNested = onlyZips(&zr.Reader)
	}
	if err := v.addZip(&zr.Reader, 0, expandNested); err != nil {
		v.Close()
		return nil, err
	}
	v.index()
	return v, nil
}

func onlyZips(r *zip.Reader) bool {
	n := 0
	for _, f := range r.File {
		if f.FileInfo().IsDir() || junk(f.Name) {
			continue
		}
		if !strings.EqualFold(path.Ext(f.Name), ".zip") {
			return false
		}
		n++
	}
	return n > 0
}

// junk 是系统生成、不需要导入的文件。
func junk(name string) bool {
	n := strings.ReplaceAll(name, "\\", "/")
	base := path.Base(n)
	return strings.HasPrefix(n, "__MACOSX/") || strings.Contains(n, "/__MACOSX/") || base == ".DS_Store" || base == "Thumbs.db" || base == "desktop.ini"
}

// decodeName 处理非 UTF-8 的条目名（Windows 自带压缩常用 GBK）。
func decodeName(f *zip.File) string {
	if utf8.ValidString(f.Name) {
		return f.Name
	}
	if s, err := simplifiedchinese.GBK.NewDecoder().String(f.Name); err == nil && utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(f.Name, "_")
}

// cleanPath 规范化 zip 里的路径；不安全（绝对路径、盘符、..）时返回 false。
func cleanPath(name string) (string, bool) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || len(n) >= 2 && n[1] == ':' {
		return "", false
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return "", false
		}
	}
	if strings.ContainsAny(n, "\x00") {
		return "", false
	}
	c := path.Clean(n)
	if c == "." || c == "" {
		return "", true
	}
	if strings.HasPrefix(c, "../") || c == ".." {
		return "", false
	}
	return c, true
}

func (v *vfs) addZip(r *zip.Reader, depth int, expand bool) error {
	var nested []*zip.File
	for _, f := range r.File {
		if junk(f.Name) {
			continue
		}
		name := decodeName(f)
		clean, ok := cleanPath(name)
		if !ok {
			v.warn(Warning{Code: "unsafe_path", Path: name, Message: "路径不安全（绝对路径或含 ..），已跳过"})
			continue
		}
		if clean == "" {
			continue
		}
		mode := f.Mode()
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			v.addDir(clean)
			continue
		}
		if mode&os.ModeSymlink != 0 {
			v.warn(Warning{Code: "symlink", Path: clean, Message: "符号链接不导入，已跳过"})
			continue
		}
		if mode&os.ModeType != 0 {
			v.warn(Warning{Code: "unsafe_path", Path: clean, Message: "不是普通文件，已跳过"})
			continue
		}
		v.count++
		if v.lim.MaxEntries > 0 && v.count > v.lim.MaxEntries {
			return fail("导入包里的文件超过 %d 个，已停止", v.lim.MaxEntries)
		}
		v.decl += f.UncompressedSize64
		if v.lim.MaxTotalBytes > 0 && v.decl > uint64(v.lim.MaxTotalBytes) {
			return errTooBig
		}
		if expand && depth == 0 && strings.EqualFold(path.Ext(clean), ".zip") {
			nested = append(nested, f)
			continue
		}
		if _, dup := v.files[clean]; dup {
			continue
		}
		v.files[clean] = &vfile{path: clean, zf: f, size: int64(f.UncompressedSize64)}
		v.addDir(path.Dir(clean))
	}
	for _, f := range nested {
		v.nested++
		if v.lim.MaxNestedZips > 0 && v.nested > v.lim.MaxNestedZips {
			return fail("导入包里套的 zip 超过 %d 个，已停止", v.lim.MaxNestedZips)
		}
		tmp, err := v.extractTemp(f)
		if err != nil {
			return err
		}
		zr, err := zip.OpenReader(tmp)
		if err != nil {
			v.warn(Warning{Code: "bad_zip", Path: decodeName(f), Message: "内层 zip 无法打开，已跳过"})
			continue
		}
		v.closers = append(v.closers, zr)
		if err := v.addZip(&zr.Reader, depth+1, false); err != nil {
			return err
		}
	}
	return nil
}

func (v *vfs) addDir(d string) {
	for d != "." && d != "" && d != "/" && !v.dirs[d] {
		v.dirs[d] = true
		d = path.Dir(d)
	}
}

// extractTemp 把内层 zip 解到缓存目录的临时文件（计入解压总量）。
func (v *vfs) extractTemp(f *zip.File) (string, error) {
	if err := os.MkdirAll(v.tmpDir, 0o700); err != nil {
		return "", err
	}
	out, err := os.CreateTemp(v.tmpDir, "~$nested-*.zip")
	if err != nil {
		return "", err
	}
	v.temps = append(v.temps, out.Name())
	rc, err := f.Open()
	if err != nil {
		out.Close()
		return "", fail("内层 zip 读取失败")
	}
	_, err = io.Copy(out, v.counted(rc))
	rc.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, errTooBig) {
			return "", err
		}
		return "", fail("内层 zip 读取失败")
	}
	return out.Name(), nil
}

// index 建查找索引；顶层只有一个目录时记下它（Obsidian 库名）但不剥掉，由各来源自己决定。
func (v *vfs) index() {
	v.lower = map[string]string{}
	v.byBase = map[string][]string{}
	for p := range v.files {
		v.lower[strings.ToLower(p)] = p
		b := strings.ToLower(path.Base(p))
		v.byBase[b] = append(v.byBase[b], p)
	}
	for _, l := range v.byBase {
		sort.Strings(l)
	}
}

// singleRoot 返回唯一的顶层目录名（所有文件都在它下面时），否则返回空。
func (v *vfs) singleRoot() string {
	root := ""
	for p := range v.files {
		i := strings.IndexByte(p, '/')
		if i < 0 {
			return ""
		}
		if root == "" {
			root = p[:i]
		} else if p[:i] != root {
			return ""
		}
	}
	return root
}

// stripRoot 去掉统一的顶层目录前缀。
func (v *vfs) stripRoot(root string) {
	if root == "" {
		return
	}
	pre := root + "/"
	nf := map[string]*vfile{}
	for p, f := range v.files {
		np := strings.TrimPrefix(p, pre)
		f.path = np
		nf[np] = f
	}
	v.files = nf
	nd := map[string]bool{}
	for d := range v.dirs {
		if d == root {
			continue
		}
		nd[strings.TrimPrefix(d, pre)] = true
	}
	v.dirs = nd
	v.root = root
	v.index()
}

// Close 关闭 zip 并删除临时文件。
func (v *vfs) Close() {
	for _, c := range v.closers {
		c.Close()
	}
	for _, t := range v.temps {
		os.Remove(t)
	}
	v.closers, v.temps = nil, nil
}

// get 精确查找，找不到再大小写不敏感查找。
func (v *vfs) get(p string) *vfile {
	if f := v.files[p]; f != nil {
		return f
	}
	if real, ok := v.lower[strings.ToLower(p)]; ok {
		return v.files[real]
	}
	return nil
}

// paths 返回全部文件路径（排序后）。
func (v *vfs) paths() []string {
	out := make([]string, 0, len(v.files))
	for p := range v.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type countingReader struct {
	r   io.Reader
	v   *vfs
	err error
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.r.Read(p)
	if n > 0 {
		total := c.v.read.Add(int64(n))
		if c.v.lim.MaxTotalBytes > 0 && total > c.v.lim.MaxTotalBytes {
			c.err = errTooBig
			return n, errTooBig
		}
	}
	return n, err
}

func (v *vfs) counted(r io.Reader) io.Reader { return &countingReader{r: r, v: v} }

type readCloser struct {
	io.Reader
	io.Closer
}

// open 打开文件内容（计入解压总量）。
func (v *vfs) open(f *vfile) (io.ReadCloser, error) {
	if f.disk != "" {
		fh, err := os.Open(f.disk)
		if err != nil {
			return nil, err
		}
		return readCloser{Reader: v.counted(fh), Closer: fh}, nil
	}
	rc, err := f.zf.Open()
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", f.path, err)
	}
	return readCloser{Reader: v.counted(rc), Closer: rc}, nil
}

// readText 读文本文件（超过上限报错）。
func (v *vfs) readText(f *vfile) ([]byte, error) {
	if v.lim.MaxTextBytes > 0 && f.size > v.lim.MaxTextBytes {
		return nil, errTextTooBig
	}
	rc, err := v.open(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	lim := v.lim.MaxTextBytes
	if lim <= 0 {
		lim = 1 << 40
	}
	b, err := io.ReadAll(io.LimitReader(rc, lim+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > lim {
		return nil, errTextTooBig
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		if d, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b); err == nil && utf8.Valid(d) {
			b = d
		} else {
			b = bytes.ToValidUTF8(b, []byte("�"))
		}
	}
	return b, nil
}

var errTextTooBig = errors.New("文本文件太大")

// abortError 表示必须终止整个导入的错误（例如 Notion 密钥失效）。
type abortError struct{ msg string }

func (e *abortError) Error() string    { return e.msg }
func (e *abortError) UserFacing() bool { return true }

// fatal 判断错误是否必须终止整个导入（zip 炸弹、密钥失效等）。
func fatal(err error) bool {
	var ae *abortError
	return errors.Is(err, errTooBig) || errors.As(err, &ae)
}
