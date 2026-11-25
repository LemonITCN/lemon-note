package target

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sftpServer struct {
	addr    string
	host    string
	port    int
	hostKey ssh.Signer
	root    string
}

// startSFTP 起一个进程内 SSH 服务器，只提供 sftp 子系统，工作目录是临时目录。
func startSFTP(t *testing.T, password string, userKey ssh.PublicKey) *sftpServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "backup" && string(pass) == password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if userKey != nil && c.User() == "backup" && string(key.Marshal()) == string(userKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(c, cfg, root)
		}
	}()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return &sftpServer{addr: ln.Addr().String(), host: host, port: port, hostKey: hostKey, root: root}
}

func serveSSH(c net.Conn, cfg *ssh.ServerConfig, root string) {
	defer c.Close()
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range creqs {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(root))
					if err == nil {
						srv.Serve()
						srv.Close()
					}
					ch.Close()
				}
			}
		}()
	}
}

func (s *sftpServer) config(fp string) SFTPConfig {
	return SFTPConfig{Host: s.host, Port: s.port, Username: "backup", Password: "s3cret 口令", HostKeySHA256: fp, Path: "backups/notes"}
}

func openSFTPT(t *testing.T, c SFTPConfig) Target {
	t.Helper()
	b, _ := json.Marshal(c)
	tg, err := Open(KindSFTP, b, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tg.Close() })
	return tg
}

func TestSFTPSuite(t *testing.T) {
	s := startSFTP(t, "s3cret 口令", nil)
	tg := openSFTPT(t, s.config(ssh.FingerprintSHA256(s.hostKey.PublicKey())))
	runSuite(t, tg, suiteOpts{bigSize: 3<<20 + 11})
	if _, err := os.Stat(filepath.Join(s.root, "backups", "notes", "dir", "sub", "c.bin")); err != nil {
		t.Fatalf("文件不在配置的目录下：%v", err)
	}
	filepath.WalkDir(s.root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tempPrefix) {
			t.Errorf("残留临时文件：%s", p)
		}
		return nil
	})
}

func TestSFTPRenameWithoutPosixExtension(t *testing.T) {
	s := startSFTP(t, "s3cret 口令", nil)
	tg := openSFTPT(t, s.config(ssh.FingerprintSHA256(s.hostKey.PublicKey())))
	st := tg.(*sftpTarget)
	if _, err := st.connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	if !st.posix {
		t.Error("测试服务器支持 posix-rename@openssh.com，客户端应识别出来")
	}
	st.posix = false // 模拟只支持 SFTP v3 rename 的服务器
	st.mu.Unlock()
	mustPut(t, tg, "x/a.txt", []byte("one"))
	mustPut(t, tg, "x/a.txt", []byte("two"))
	if got := mustGet(t, tg, "x/a.txt"); string(got) != "two" {
		t.Fatalf("不支持 posix-rename 时覆盖写失败：%q", got)
	}
}

func TestSFTPUnknownHostKey(t *testing.T) {
	s := startSFTP(t, "s3cret 口令", nil)
	tg := openSFTPT(t, s.config(""))
	err := Probe(context.Background(), tg)
	var uk *UnknownHostKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("未配置主机密钥时应返回 *UnknownHostKeyError，实际 %v", err)
	}
	want := ssh.FingerprintSHA256(s.hostKey.PublicKey())
	if uk.Fingerprint != want || !strings.HasPrefix(uk.Fingerprint, "SHA256:") {
		t.Fatalf("指纹 %q，期望 %q", uk.Fingerprint, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("错误信息里应带指纹：%v", err)
	}
	// 用户确认后（指纹写进配置，前缀与 = 填充可省略）就能连上。
	tg2 := openSFTPT(t, s.config(strings.TrimPrefix(want, "SHA256:")+"="))
	if err := Probe(context.Background(), tg2); err != nil {
		t.Fatalf("确认指纹后应能连上：%v", err)
	}
}

func TestSFTPHostKeyMismatch(t *testing.T) {
	s := startSFTP(t, "s3cret 口令", nil)
	other := startSFTP(t, "x", nil)
	tg := openSFTPT(t, s.config(ssh.FingerprintSHA256(other.hostKey.PublicKey())))
	_, err := tg.List(context.Background(), "")
	var mm *HostKeyMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("主机密钥不一致时应返回 *HostKeyMismatchError，实际 %v", err)
	}
	if mm.Got != ssh.FingerprintSHA256(s.hostKey.PublicKey()) {
		t.Fatalf("实际指纹不对：%q", mm.Got)
	}
	if _, err := os.Stat(filepath.Join(s.root, "backups")); err == nil {
		t.Fatal("主机密钥不一致时不能执行任何操作")
	}
}

func TestSFTPWrongPassword(t *testing.T) {
	s := startSFTP(t, "s3cret 口令", nil)
	c := s.config(ssh.FingerprintSHA256(s.hostKey.PublicKey()))
	c.Password = "wrong-password"
	err := Probe(context.Background(), openSFTPT(t, c))
	if err == nil || !strings.Contains(err.Error(), "不正确") {
		t.Fatalf("口令错误提示不对：%v", err)
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Fatal("错误信息泄露了口令")
	}
}

func TestSFTPPrivateKey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	s := startSFTP(t, "unused-password", sshPub)
	fp := ssh.FingerprintSHA256(s.hostKey.PublicKey())

	block, err := ssh.MarshalPrivateKey(priv, "backup")
	if err != nil {
		t.Fatal(err)
	}
	c := SFTPConfig{Host: s.host, Port: s.port, Username: "backup", PrivateKey: string(pem.EncodeToMemory(block)), HostKeySHA256: fp}
	if err := Probe(context.Background(), openSFTPT(t, c)); err != nil {
		t.Fatalf("私钥登录失败：%v", err)
	}

	encBlock, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "backup", []byte("钥匙口令"))
	if err != nil {
		t.Fatal(err)
	}
	c.PrivateKey = string(pem.EncodeToMemory(encBlock))
	c.Passphrase = "钥匙口令"
	if err := Probe(context.Background(), openSFTPT(t, c)); err != nil {
		t.Fatalf("带口令的私钥登录失败：%v", err)
	}
	c.Passphrase = ""
	b, _ := json.Marshal(c)
	if _, err := Open(KindSFTP, b, Deps{}); err == nil || !strings.Contains(err.Error(), "私钥口令") {
		t.Fatalf("缺私钥口令提示不对：%v", err)
	}
	c.Passphrase = "错的"
	b, _ = json.Marshal(c)
	if _, err := Open(KindSFTP, b, Deps{}); err == nil || !strings.Contains(err.Error(), "口令不正确") {
		t.Fatalf("私钥口令错误提示不对：%v", err)
	}
}

func TestSFTPConfigErrors(t *testing.T) {
	bad := []SFTPConfig{
		{Host: "", Username: "u", Password: "p"},
		{Host: "h", Username: "", Password: "p"},
		{Host: "h", Username: "u"},
		{Host: "h", Username: "u", Password: "p", Port: 70000},
		{Host: "h", Username: "u", Password: "p", Path: "../etc"},
		{Host: "h", Username: "u", PrivateKey: "not a key"},
	}
	for _, c := range bad {
		b, _ := json.Marshal(c)
		if _, err := Open(KindSFTP, b, Deps{}); err == nil {
			t.Errorf("配置 %+v 应报错", c)
		}
	}
}

func TestSFTPReconnectAfterDrop(t *testing.T) {
	s := startSFTP(t, "s3cret 口令", nil)
	tg := openSFTPT(t, s.config(ssh.FingerprintSHA256(s.hostKey.PublicKey())))
	mustPut(t, tg, "r.txt", []byte("1"))
	st := tg.(*sftpTarget)
	st.mu.Lock()
	conn := st.conn
	st.mu.Unlock()
	conn.Close()
	conn.Wait()
	// 断线后幂等操作自动重连重试，第一次就要成功。
	if _, err := tg.Stat(context.Background(), "r.txt"); err != nil {
		t.Fatalf("断线后没有自动重连：%v", err)
	}
	if got := mustGet(t, tg, "r.txt"); string(got) != "1" {
		t.Fatalf("重连后读取不对：%q", got)
	}
}
