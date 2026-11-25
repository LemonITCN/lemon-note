package target

import (
	"context"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPConfig 是 SFTP 的配置。口令与私钥至少填一个。
type SFTPConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"` // 缺省 22
	Username   string `json:"username"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"` // PEM / OpenSSH 格式私钥内容
	Passphrase string `json:"passphrase"`  // 私钥口令
	// HostKeySHA256 是服务器主机密钥指纹，格式同 ssh-keygen -lf 的 SHA256:xxxx。
	// 没填时连接会失败并返回 *UnknownHostKeyError，界面请用户核对指纹后写进配置；绝不默认信任。
	HostKeySHA256 string `json:"host_key_sha256"`
	Path          string `json:"path"` // 备份根目录；相对路径相对登录后的默认目录，缺省为默认目录
}

// UnknownHostKeyError 表示没有配置主机密钥指纹。Fingerprint 是服务器实际出示的指纹（SHA256:xxxx），
// 请用户与服务器上 ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub 的输出核对后写进 host_key_sha256。
type UnknownHostKeyError struct {
	Fingerprint string
	KeyType     string
}

func (e *UnknownHostKeyError) Error() string {
	return fmt.Sprintf("尚未确认 SFTP 服务器的主机密钥，服务器出示的指纹是 %s（%s），请核对无误后确认", e.Fingerprint, e.KeyType)
}

// HostKeyMismatchError 表示服务器出示的主机密钥与配置的不一致，可能遭到中间人攻击，也可能是服务器重装过。
type HostKeyMismatchError struct {
	Expected string
	Got      string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("SFTP 服务器的主机密钥与已确认的不一致（已确认 %s，实际 %s），可能遭到中间人攻击，已拒绝连接；"+
		"如果服务器确实重装过，请核对新指纹后重新确认", e.Expected, e.Got)
}

type sftpTarget struct {
	cfg      SFTPConfig
	addr     string
	base     string // 备份根目录
	auth     []ssh.AuthMethod
	expected string // 规范化后的指纹（不含 SHA256: 前缀、不含 = 填充）

	mu     sync.Mutex
	conn   *ssh.Client
	client *sftp.Client
	posix  bool // 服务器支持 posix-rename@openssh.com
	dirs   sync.Map
}

func normalizeFingerprint(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 && strings.EqualFold(s[:7], "SHA256:") {
		s = s[7:]
	}
	return strings.TrimRight(s, "=")
}

func openSFTP(c SFTPConfig) (Target, error) {
	host := strings.TrimSpace(c.Host)
	if host == "" {
		return nil, errors.New("请填写 SFTP 服务器地址")
	}
	port := c.Port
	if port == 0 {
		port = 22
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("SFTP 端口 %d 不正确", c.Port)
	}
	if strings.TrimSpace(c.Username) == "" {
		return nil, errors.New("请填写 SFTP 用户名")
	}
	var auth []ssh.AuthMethod
	if strings.TrimSpace(c.PrivateKey) != "" {
		var signer ssh.Signer
		var err error
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			var pm *ssh.PassphraseMissingError
			if errors.As(err, &pm) {
				return nil, errors.New("SFTP 私钥有口令保护，请填写私钥口令")
			}
			if errors.Is(err, x509.IncorrectPasswordError) {
				return nil, errors.New("SFTP 私钥口令不正确")
			}
			return nil, errors.New("SFTP 私钥格式无法识别，请粘贴完整的私钥内容（-----BEGIN ... PRIVATE KEY-----）")
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		pw := c.Password
		auth = append(auth, ssh.Password(pw))
		// 有的服务器只开了键盘交互认证，用同一个口令回答。
		auth = append(auth, ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
			ans := make([]string, len(questions))
			for i := range ans {
				ans[i] = pw
			}
			return ans, nil
		}))
	}
	if len(auth) == 0 {
		return nil, errors.New("请填写 SFTP 口令或私钥")
	}
	base := strings.TrimSpace(c.Path)
	if strings.Contains(base, "\\") {
		return nil, errors.New("SFTP 备份目录请用 / 分隔")
	}
	if base == "" {
		base = "."
	}
	base = path.Clean(base)
	for _, seg := range strings.Split(base, "/") {
		if seg == ".." {
			return nil, errors.New("SFTP 备份目录不能包含 ..")
		}
	}
	return &sftpTarget{
		cfg:      c,
		addr:     net.JoinHostPort(host, strconv.Itoa(port)),
		base:     base,
		auth:     auth,
		expected: normalizeFingerprint(c.HostKeySHA256),
	}, nil
}

func (t *sftpTarget) hostKeyCallback(hostKeyErr *error) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		var err error
		if t.expected == "" {
			err = &UnknownHostKeyError{Fingerprint: fp, KeyType: key.Type()}
		} else if subtle.ConstantTimeCompare([]byte(normalizeFingerprint(fp)), []byte(t.expected)) != 1 {
			err = &HostKeyMismatchError{Expected: "SHA256:" + t.expected, Got: fp}
		}
		*hostKeyErr = err
		return err
	}
}

// connect 返回可用的 SFTP 客户端，断线后自动重连。
func (t *sftpTarget) connect(ctx context.Context) (*sftp.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		return t.client, nil
	}
	var hostKeyErr error
	cfg := &ssh.ClientConfig{
		User:            t.cfg.Username,
		Auth:            t.auth,
		HostKeyCallback: t.hostKeyCallback(&hostKeyErr),
		Timeout:         20 * time.Second,
	}
	d := net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return nil, fmt.Errorf("连接 SFTP 服务器 %s 失败：%s", t.addr, netErrText(ctxErr(ctx, err)))
	}
	// 握手期间 ctx 取消时关闭连接。
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	raw.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewClientConn(raw, t.addr, cfg)
	stop()
	if err != nil {
		raw.Close()
		if hostKeyErr != nil {
			return nil, hostKeyErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, errors.New("连接 SFTP 失败：用户名、口令或私钥不正确")
		}
		return nil, fmt.Errorf("连接 SFTP 失败：%s", netErrText(err))
	}
	raw.SetDeadline(time.Time{})
	conn := ssh.NewClient(sc, chans, reqs)
	cli, err := sftp.NewClient(conn, sftp.UseConcurrentWrites(true), sftp.UseFstat(true))
	if err != nil {
		conn.Close()
		return nil, errors.New("连接 SFTP 失败：服务器没有开启 SFTP 子系统")
	}
	_, t.posix = cli.HasExtension("posix-rename@openssh.com")
	t.conn, t.client = conn, cli
	go func() {
		// 连接断开后清掉缓存的客户端，下次操作重连。
		conn.Wait()
		t.mu.Lock()
		if t.client == cli {
			t.client, t.conn = nil, nil
			t.dirs.Clear()
		}
		t.mu.Unlock()
		cli.Close()
	}()
	return cli, nil
}

// drop 关闭当前连接（ctx 取消时用来中断阻塞的 SFTP 调用）。
func (t *sftpTarget) drop(cli *sftp.Client) {
	t.mu.Lock()
	if t.client == cli && t.conn != nil {
		t.conn.Close()
		t.client, t.conn = nil, nil // 立即作废，下次操作重连
		t.dirs.Clear()
	}
	t.mu.Unlock()
}

// withClient 执行一次操作；ctx 取消时断开连接以中断阻塞调用（同一目标上并发的其他操作也会失败）。
// 连接已断开时作废缓存的连接；retry 为 true（幂等操作）时重连后再试一次。
func (t *sftpTarget) withClient(ctx context.Context, retry bool, fn func(*sftp.Client) error) error {
	for attempt := 0; ; attempt++ {
		cli, err := t.connect(ctx)
		if err != nil {
			return err
		}
		stop := context.AfterFunc(ctx, func() { t.drop(cli) })
		err = fn(cli)
		stop()
		if cerr := ctx.Err(); cerr != nil && err != nil {
			return cerr
		}
		if err != nil && errors.Is(err, errSFTPConnLost) {
			t.drop(cli)
			if retry && attempt == 0 {
				continue
			}
		}
		return err
	}
}

var errSFTPConnLost = errors.New("与 SFTP 服务器的连接已断开")

func (t *sftpTarget) full(rel string) string {
	if rel == "" {
		return t.base
	}
	return path.Join(t.base, rel)
}

func sftpErr(op, p string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s「%s」失败：没有权限", op, p)
	}
	if errors.Is(err, ErrSizeMismatch) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, err)
	}
	var se *sftp.StatusError
	if errors.As(err, &se) {
		return fmt.Errorf("%s「%s」失败：SFTP 服务器返回错误 %d（%s）", op, p, se.Code, se.Error())
	}
	if errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, errSFTPConnLost) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, errSFTPConnLost)
	}
	return fmt.Errorf("%s「%s」失败：%s", op, p, netErrText(err))
}

func (t *sftpTarget) mkdirAll(cli *sftp.Client, dir string) error {
	if _, ok := t.dirs.Load(dir); ok {
		return nil
	}
	if err := cli.MkdirAll(dir); err != nil {
		return err
	}
	t.dirs.Store(dir, true)
	return nil
}

func (t *sftpTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	if size < 0 {
		return errors.New("写入文件时必须给出文件大小")
	}
	final := t.full(p)
	dir := path.Dir(final)
	tmp := path.Join(dir, tempName())
	err = t.withClient(ctx, false, func(cli *sftp.Client) error {
		if err := t.mkdirAll(cli, dir); err != nil {
			return sftpErr("创建目录", dirName(p), err)
		}
		f, err := cli.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			t.dirs.Delete(dir)
			return sftpErr("创建临时文件", p, err)
		}
		ok := false
		defer func() {
			if !ok {
				f.Close()
				if rerr := cli.Remove(tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
					// 连接已断开（或 ctx 取消时被断开）：重新连上再清理临时文件。
					t.removeLater(ctx, tmp)
				}
			}
		}()
		_ = f.Chmod(0o600)
		n, err := f.ReadFrom(ctxReader{ctx, newSizedReader(r, size)})
		if err != nil {
			return sftpErr("上传", p, err)
		}
		if n != size {
			return fmt.Errorf("上传「%s」失败：%w", p, ErrSizeMismatch)
		}
		// 关闭前核对服务器上的大小（并发写入出错时可能留下空洞）。
		st, err := f.Stat()
		if err != nil {
			return sftpErr("上传", p, err)
		}
		if st.Size() != size {
			return fmt.Errorf("上传「%s」失败：服务器上的文件大小 %d 与实际 %d 不一致", p, st.Size(), size)
		}
		if err := f.Close(); err != nil {
			return sftpErr("上传", p, err)
		}
		if t.posix {
			err = cli.PosixRename(tmp, final) // 原子覆盖
		} else {
			err = cli.Rename(tmp, final)
			if err != nil {
				// SFTP v3 的 rename 不覆盖已存在的文件：先删再改名（中间有短暂空档，但不会出现半个文件）。
				if _, serr := cli.Stat(final); serr == nil {
					if rerr := cli.Remove(final); rerr == nil {
						err = cli.Rename(tmp, final)
					}
				}
			}
		}
		if err != nil {
			return sftpErr("保存", p, err)
		}
		ok = true
		return nil
	})
	return err
}

// removeLater 用独立的短超时上下文重连并删除临时文件（尽力而为）。
func (t *sftpTarget) removeLater(ctx context.Context, tmp string) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = t.withClient(c, true, func(cli *sftp.Client) error { return cli.Remove(tmp) })
}

type sftpReadCloser struct {
	f    *sftp.File
	stop func() bool
}

func (s *sftpReadCloser) Read(p []byte) (int, error) { return s.f.Read(p) }
func (s *sftpReadCloser) Close() error {
	s.stop()
	return s.f.Close()
}

func (t *sftpTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	var rc io.ReadCloser
	err = t.withClient(ctx, true, func(cli *sftp.Client) error {
		f, err := cli.Open(t.full(p))
		if err != nil {
			return sftpErr("读取", p, err)
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return sftpErr("读取", p, err)
		}
		if !st.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("读取「%s」失败：%w（不是普通文件）", p, ErrNotExist)
		}
		// 读取期间 ctx 取消时断开连接，中断阻塞的读取；Close 时解除。
		stop := context.AfterFunc(ctx, func() { t.drop(cli) })
		rc = newCheckedReadCloser(&sftpReadCloser{f: f, stop: stop}, st.Size())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}

func (t *sftpTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = t.withClient(ctx, true, func(cli *sftp.Client) error {
		st, err := cli.Stat(t.full(p))
		if err != nil {
			return sftpErr("查询", p, err)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("查询「%s」失败：%w（不是普通文件）", p, ErrNotExist)
		}
		e = Entry{Path: p, Size: st.Size(), ModTime: st.ModTime()}
		return nil
	})
	return e, err
}

func (t *sftpTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	return t.withClient(ctx, true, func(cli *sftp.Client) error {
		st, err := cli.Lstat(t.full(p))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return sftpErr("删除", p, err)
		}
		if st.IsDir() {
			return fmt.Errorf("删除「%s」失败：这是一个目录", p)
		}
		if err := cli.Remove(t.full(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return sftpErr("删除", p, err)
		}
		return nil
	})
}

func (t *sftpTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	var out []Entry
	err = t.withClient(ctx, true, func(cli *sftp.Client) error {
		var walk func(rel string) error
		walk = func(rel string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			infos, err := cli.ReadDirContext(ctx, t.full(rel))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			for _, fi := range infos {
				name := fi.Name()
				if name == "." || name == ".." || name == "" {
					continue
				}
				child := joinKey(rel, name)
				if fi.IsDir() {
					if strings.HasPrefix(child+"/", prefix) || strings.HasPrefix(prefix, child+"/") {
						if err := walk(child); err != nil {
							return err
						}
					}
					continue
				}
				if isTempName(name) {
					if isStaleTemp(name, fi.ModTime()) {
						cli.Remove(t.full(child)) // 上次中断留下的临时文件
					}
					continue
				}
				if !fi.Mode().IsRegular() || !strings.HasPrefix(child, prefix) {
					continue
				}
				if _, err := cleanPath(child); err != nil {
					continue
				}
				out = append(out, Entry{Path: child, Size: fi.Size(), ModTime: fi.ModTime()})
			}
			return nil
		}
		if err := walk(prefixDir(prefix)); err != nil {
			return sftpErr("列出", prefix, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (t *sftpTarget) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		t.client.Close()
		err := t.conn.Close()
		t.client, t.conn = nil, nil
		return err
	}
	return nil
}
