// Package target 实现备份目标驱动：本机目录 / NAS、S3 兼容、腾讯云 COS、阿里云 OSS、
// WebDAV、SFTP、百度网盘。上层备份引擎只通过 Target 接口读写目标。
//
// 路径约定：
//   - 接口里的 path 一律是相对目标根目录（含配置里的 prefix / 根目录）的相对路径，用 / 分隔；
//   - 拒绝空路径、绝对路径、反斜杠、冒号、控制字符、空段（a//b）、. 与 .. 段、结尾的 /；
//   - 以 ~$ 开头的文件名留给本包写临时文件，Put 会拒绝，List 会跳过。
//
// Put 的 size 必须与 r 实际能读出的字节数完全一致，多了少了都会报错，
// 而且会在最后一个字节交给后端之前发现，保证不会以正式文件名留下截断或混入多余数据的文件。
package target

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNotExist 表示目标上没有这个文件。
var ErrNotExist = errors.New("文件不存在")

// ErrSizeMismatch 表示 Put 时数据长度与声明的 size 不一致。
var ErrSizeMismatch = errors.New("数据长度与声明的大小不一致")

// Entry 是目标上的一个文件。
type Entry struct {
	Path    string // 相对目标根目录，用 / 分隔
	Size    int64
	ModTime time.Time
}

// Target 是一个备份目标。所有方法都可以并发调用。
type Target interface {
	// Put 写入完整文件；支持的后端先写临时名再改名，不会留下半个文件冒充成品。
	Put(ctx context.Context, path string, r io.Reader, size int64) error
	// Get 读取文件；不存在返回 ErrNotExist。读到的数据长度不对时 Read 会返回错误。
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	// List 递归列出路径以 prefix 开头的文件（不含目录），按字符串前缀匹配；prefix 为空列出全部。
	// 目录不存在时返回空列表。
	List(ctx context.Context, prefix string) ([]Entry, error)
	// Delete 删除文件，不存在视为成功。
	Delete(ctx context.Context, path string) error
	// Stat 查询文件；不存在返回 ErrNotExist。
	Stat(ctx context.Context, path string) (Entry, error)
	Close() error
}

// Deps 是打开目标时需要的外部依赖。
type Deps struct {
	// HTTPClient 用于 HTTP 类目标；为 nil 时使用内置的默认客户端。
	// S3 / COS 与 WebDAV 只使用它的 Transport，OSS 与百度网盘使用整个客户端。
	HTTPClient *http.Client
	// SaveConfig 在百度网盘刷新令牌后回写新配置（明文 JSON，保留原配置里本包不认识的字段）。
	// 为 nil 时不会主动刷新令牌，令牌过期后需要重新授权。
	SaveConfig func(newConfigJSON []byte) error
}

// 目标类型。
const (
	KindLocal  = "local"
	KindS3     = "s3"
	KindCOS    = "cos"
	KindOSS    = "oss"
	KindWebDAV = "webdav"
	KindSFTP   = "sftp"
	KindBaidu  = "baidu"
)

// Open 按类型和配置打开一个目标。只校验配置、不连网（SFTP、百度网盘等在第一次操作时才连接）。
func Open(kind string, configJSON []byte, d Deps) (Target, error) {
	switch kind {
	case KindLocal:
		var c LocalConfig
		if err := decodeConfig(configJSON, &c); err != nil {
			return nil, err
		}
		return openLocal(c)
	case KindS3:
		var c S3Config
		if err := decodeConfig(configJSON, &c); err != nil {
			return nil, err
		}
		return openS3(c, d)
	case KindCOS:
		var c COSConfig
		if err := decodeConfig(configJSON, &c); err != nil {
			return nil, err
		}
		return openCOS(c, d)
	case KindOSS:
		var c OSSConfig
		if err := decodeConfig(configJSON, &c); err != nil {
			return nil, err
		}
		return openOSS(c, d)
	case KindWebDAV:
		var c WebDAVConfig
		if err := decodeConfig(configJSON, &c); err != nil {
			return nil, err
		}
		return openWebDAV(c, d)
	case KindSFTP:
		var c SFTPConfig
		if err := decodeConfig(configJSON, &c); err != nil {
			return nil, err
		}
		return openSFTP(c)
	case KindBaidu:
		var c BaiduConfig
		if err := decodeConfig(configJSON, &c); err != nil {
			return nil, err
		}
		return openBaidu(c, configJSON, d)
	default:
		return nil, fmt.Errorf("不支持的备份目标类型「%s」", kind)
	}
}

func decodeConfig(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("备份目标配置为空")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("备份目标配置格式不正确：%v", err)
	}
	return nil
}

// Probe 写一个探测文件、查询、列出、读回比对，最后删除，用于界面「测试连接」。
func Probe(ctx context.Context, t Target) error {
	name := "lemon-note-probe-" + randomHex(8)
	data := []byte("lemon-note backup probe " + randomHex(32) + "\n")
	if err := t.Put(ctx, name, bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("写入测试文件失败：%w", err)
	}
	cleanup := func() {
		// 用独立的短超时上下文清理，ctx 已取消时也尽量删掉。
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = t.Delete(c, name)
	}
	e, err := t.Stat(ctx, name)
	if err != nil {
		cleanup()
		return fmt.Errorf("查询测试文件失败：%w", err)
	}
	if e.Size != int64(len(data)) {
		cleanup()
		return fmt.Errorf("测试文件大小不一致：写入 %d 字节，目标报告 %d 字节", len(data), e.Size)
	}
	list, err := t.List(ctx, name)
	if err != nil {
		cleanup()
		return fmt.Errorf("列出文件失败：%w", err)
	}
	found := false
	for _, it := range list {
		if it.Path == name {
			found = true
			break
		}
	}
	if !found {
		cleanup()
		return errors.New("列出文件时没有找到刚写入的测试文件")
	}
	rc, err := t.Get(ctx, name)
	if err != nil {
		cleanup()
		return fmt.Errorf("读取测试文件失败：%w", err)
	}
	got, err := io.ReadAll(io.LimitReader(rc, int64(len(data))+1))
	rc.Close()
	if err != nil {
		cleanup()
		return fmt.Errorf("读取测试文件失败：%w", err)
	}
	if !bytes.Equal(got, data) {
		cleanup()
		return errors.New("读回的测试文件内容与写入的不一致")
	}
	if err := t.Delete(ctx, name); err != nil {
		return fmt.Errorf("删除测试文件失败：%w", err)
	}
	if _, err := t.Stat(ctx, name); !errors.Is(err, ErrNotExist) {
		if err == nil {
			return errors.New("删除测试文件后它仍然存在")
		}
		return fmt.Errorf("删除后查询测试文件失败：%w", err)
	}
	return nil
}

// ---------------- 路径校验 ----------------

// tempPrefix 是本包写临时文件用的文件名前缀（与数据目录一致，同步盘不会上传 ~$ 开头的文件）。
const tempPrefix = "~$"

func isTempName(name string) bool { return strings.HasPrefix(name, tempPrefix) }

// staleTempAge 之前的临时文件视为上次中断留下的残留，List 时顺手删掉。
const staleTempAge = 48 * time.Hour

func isStaleTemp(name string, mtime time.Time) bool {
	return isTempName(name) && strings.HasSuffix(name, ".tmp") && !mtime.IsZero() && time.Since(mtime) > staleTempAge
}

func tempName() string { return tempPrefix + randomHex(8) + ".tmp" }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func checkSegment(seg, whole string) error {
	if seg == "" {
		return fmt.Errorf("路径「%s」不合法：不能有空的路径段", whole)
	}
	if seg == "." || seg == ".." {
		return fmt.Errorf("路径「%s」不合法：不能包含 . 或 ..", whole)
	}
	return nil
}

func checkChars(p string) error {
	for _, r := range p {
		switch {
		case r == '\\':
			return fmt.Errorf("路径「%s」不合法：不能包含反斜杠", p)
		case r == ':':
			return fmt.Errorf("路径「%s」不合法：不能包含冒号", p)
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("路径「%s」不合法：不能包含控制字符", p)
		}
	}
	if !utf8.ValidString(p) {
		return fmt.Errorf("路径「%s」不合法：不是有效的 UTF-8", p)
	}
	return nil
}

// cleanPath 校验文件的相对路径，合法时原样返回。
func cleanPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("路径不能为空")
	}
	if err := checkChars(p); err != nil {
		return "", err
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("路径「%s」不合法：必须是相对路径", p)
	}
	if strings.HasSuffix(p, "/") {
		return "", fmt.Errorf("路径「%s」不合法：不能以 / 结尾", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if err := checkSegment(seg, p); err != nil {
			return "", err
		}
	}
	return p, nil
}

// cleanFilePath 在 cleanPath 基础上拒绝与临时文件同名的路径（用于 Put）。
func cleanFilePath(p string) (string, error) {
	p, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	if isTempName(baseName(p)) {
		return "", fmt.Errorf("路径「%s」不合法：文件名不能以 %s 开头", p, tempPrefix)
	}
	return p, nil
}

// cleanPrefix 校验 List 的前缀：可以为空，可以以 / 结尾（表示目录），也可以是文件名前缀。
func cleanPrefix(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if _, err := cleanPath(strings.TrimSuffix(p, "/")); err != nil {
		return "", err
	}
	return p, nil
}

// prefixDir 返回前缀所在的目录（相对根目录，"" 表示根目录），只需要遍历这个目录。
func prefixDir(prefix string) string {
	i := strings.LastIndex(prefix, "/")
	if i < 0 {
		return ""
	}
	return prefix[:i]
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func dirName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// cleanRootPrefix 校验配置里的 prefix（对象存储、WebDAV、百度网盘的子目录），
// 去掉首尾的 /，返回 "" 或 "a/b" 形式。
func cleanRootPrefix(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if _, err := cleanPath(p); err != nil {
		return "", fmt.Errorf("配置里的子目录（prefix）不合法：%v", err)
	}
	return p, nil
}

func joinKey(prefix, rel string) string {
	if prefix == "" {
		return rel
	}
	if rel == "" {
		return prefix + "/"
	}
	return prefix + "/" + rel
}

// ---------------- 读写辅助 ----------------

// sizedReader 保证从 r 恰好读出 size 字节。最后一段数据交出去之前先探测 r 是否还有多余数据，
// 所以后端收到完整长度时，数据长度一定是对的；长度不对时后端收不全数据，不会提交文件。
type sizedReader struct {
	r      io.Reader
	remain int64
	err    error
}

func newSizedReader(r io.Reader, size int64) *sizedReader {
	return &sizedReader{r: r, remain: size}
}

func (s *sizedReader) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.remain == 0 {
		// size 为 0 或已经交完：再确认一次没有多余数据。
		if err := s.checkEOF(); err != nil {
			s.err = err
			return 0, err
		}
		s.err = io.EOF
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if int64(len(p)) < s.remain {
		n, err := s.r.Read(p)
		s.remain -= int64(n)
		if err == io.EOF {
			if s.remain > 0 {
				s.err = fmt.Errorf("%w：数据提前结束，还差 %d 字节", ErrSizeMismatch, s.remain)
				return 0, s.err
			}
			err = nil
		}
		if err != nil {
			s.err = err
		}
		return n, err
	}
	// 这是最后一段：读满后先探测多余数据，再交出去。
	last := p[:s.remain]
	n, err := io.ReadFull(s.r, last)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			err = fmt.Errorf("%w：数据提前结束，还差 %d 字节", ErrSizeMismatch, s.remain-int64(n))
		}
		s.err = err
		return 0, err
	}
	if err := s.checkEOF(); err != nil {
		s.err = err
		return 0, err
	}
	s.remain = 0
	s.err = io.EOF
	return n, nil
}

func (s *sizedReader) checkEOF() error {
	var b [1]byte
	for {
		n, err := s.r.Read(b[:])
		if n > 0 {
			return fmt.Errorf("%w：数据比声明的长", ErrSizeMismatch)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// emptyCheck 在声明大小为 0 时确认 r 确实没有数据（有的后端对空请求体根本不会去读 r）。
func emptyCheck(r io.Reader) error {
	if _, err := newSizedReader(r, 0).Read(nil); err != io.EOF {
		return err
	}
	return nil
}

// ctxReader 在上下文取消后让读取失败，用于本地拷贝循环。
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// checkedReadCloser 在读到 EOF 时核对总长度，长度不对返回错误，防止把被截断的数据当完整文件。
type checkedReadCloser struct {
	rc   io.ReadCloser
	want int64
	got  int64
	err  error
}

func newCheckedReadCloser(rc io.ReadCloser, want int64) io.ReadCloser {
	if want < 0 {
		return rc
	}
	return &checkedReadCloser{rc: rc, want: want}
}

func (c *checkedReadCloser) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.rc.Read(p)
	c.got += int64(n)
	if c.got > c.want {
		c.err = fmt.Errorf("读取到的数据比文件大小（%d 字节）长", c.want)
		return 0, c.err
	}
	if err == io.EOF && c.got != c.want {
		c.err = fmt.Errorf("读取中断：文件应有 %d 字节，只读到 %d 字节", c.want, c.got)
		return n, c.err
	}
	if err != nil {
		c.err = err
	}
	return n, err
}

func (c *checkedReadCloser) Close() error { return c.rc.Close() }

// ---------------- HTTP 辅助 ----------------

var defaultTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	t.ResponseHeaderTimeout = 5 * time.Minute
	t.ExpectContinueTimeout = 1 * time.Second
	return t
}()

var defaultHTTPClient = &http.Client{Transport: defaultTransport}

func httpClient(d Deps) *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return defaultHTTPClient
}

func httpTransport(d Deps) http.RoundTripper {
	if d.HTTPClient != nil && d.HTTPClient.Transport != nil {
		return d.HTTPClient.Transport
	}
	return defaultTransport
}

// netErrText 把网络错误转成给用户看的中文说明，去掉 URL（URL 里可能带令牌）。
func netErrText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.Canceled) {
		return "操作已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "操作超时"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Sprintf("无法解析域名 %s", dnsErr.Name)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return "网络超时"
		}
		if opErr.Op == "dial" {
			return "无法连接服务器，请检查地址、端口和网络"
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "网络超时"
	}
	msg := err.Error()
	if strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") {
		return "服务器证书校验失败：" + msg
	}
	return msg
}

// ctxErr 在上下文已取消时直接返回取消错误，避免把取消说成网络故障。
func ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// sleepCtx 等待 d，ctx 取消时提前返回。
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// boolDefault 读取可选布尔配置，缺省时取 def。
func boolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
