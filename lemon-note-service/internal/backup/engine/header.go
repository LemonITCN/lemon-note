package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/cryptstream"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
)

// Header 是目标根目录下 lemon-note-backup.json 的内容。同一个目标上的全部备份共用一份加密参数：
// 只要目标上已经有备份，就不能悄悄换口令或开关加密（否则旧备份会无法解密或混用两种格式）。
type Header struct {
	Format     int               `json:"format"`
	App        string            `json:"app"`
	CreatedAt  int64             `json:"created_at"`
	Encryption *HeaderEncryption `json:"encryption"` // nil 表示不加密
}

// HeaderEncryption 记录加密算法、口令派生参数、盐和口令校验值。口令本身不写在目标上。
type HeaderEncryption struct {
	Cipher       string `json:"cipher"`         // aes-256-gcm-chunked：见 cryptstream
	ChunkSize    int    `json:"chunk_size"`     // 每块明文字节数
	KDF          string `json:"kdf"`            // argon2id
	KDFTime      int    `json:"kdf_time"`       // 迭代次数
	KDFMemoryKiB int    `json:"kdf_memory_kib"` // 内存（KiB）
	KDFThreads   int    `json:"kdf_threads"`    // 并行度
	Salt         string `json:"salt"`           // base64
	KeyCheck     string `json:"key_check"`      // HMAC-SHA256(key, ...) 的十六进制
}

const (
	headerFormat = 1
	cipherName   = "aes-256-gcm-chunked"
	kdfName      = "argon2id"
	// 与 cryptstream.DeriveKey 一致的参数。
	kdfTime      = 3
	kdfMemoryKiB = 64 * 1024
	kdfThreads   = 2
)

// Encrypted 表示目标上的备份是否加密。
func (h *Header) Encrypted() bool { return h != nil && h.Encryption != nil }

func newHeader(password string) (*Header, []byte) {
	h := &Header{Format: headerFormat, App: "lemon-note", CreatedAt: time.Now().UnixMilli()}
	if password == "" {
		return h, nil
	}
	salt := cryptstream.NewSalt()
	key := cryptstream.DeriveKey(password, salt)
	h.Encryption = &HeaderEncryption{Cipher: cipherName, ChunkSize: cryptstream.ChunkSize, KDF: kdfName, KDFTime: kdfTime,
		KDFMemoryKiB: kdfMemoryKiB, KDFThreads: kdfThreads, Salt: base64.StdEncoding.EncodeToString(salt),
		KeyCheck: cryptstream.KeyCheck(key)}
	return h, key
}

// Key 用口令派生密钥并核对校验值；不加密的目标返回 nil。
func (h *Header) Key(password string) ([]byte, error) {
	if !h.Encrypted() {
		return nil, nil
	}
	e := h.Encryption
	if e.Cipher != cipherName || e.KDF != kdfName || e.KDFTime != kdfTime || e.KDFMemoryKiB != kdfMemoryKiB ||
		e.KDFThreads != kdfThreads || (e.ChunkSize != 0 && e.ChunkSize != cryptstream.ChunkSize) {
		return nil, uerr("目标上的备份使用了本程序不支持的加密参数，可能是更新版本的程序写的，请升级程序")
	}
	if password == "" {
		return nil, ErrNeedPassword
	}
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil || len(salt) < 8 {
		return nil, uerr("目标上的 lemon-note-backup.json 已损坏（盐无效）")
	}
	key := cryptstream.DeriveKey(password, salt)
	if !cryptstream.CheckKey(key, e.KeyCheck) {
		return nil, ErrWrongPassword
	}
	return key, nil
}

// ReadHeader 读取目标头；目标上没有时返回 nil, nil。
func ReadHeader(ctx context.Context, t target.Target) (*Header, error) {
	data, err := getBytes(ctx, t, HeaderName, nil, 1<<20)
	if errors.Is(err, target.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapUser(fmt.Errorf("读取 %s 失败：%w", HeaderName, err))
	}
	var h Header
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, uerrf("目标上的 %s 已损坏，无法识别", HeaderName)
	}
	if h.Format > headerFormat {
		return nil, uerrf("目标上的 %s 格式版本 %d 高于本程序支持的版本，请升级程序", HeaderName, h.Format)
	}
	return &h, nil
}

func writeHeader(ctx context.Context, t target.Target, h *Header) error {
	b, _ := json.MarshalIndent(h, "", "  ")
	if err := putBytes(ctx, t, HeaderName, b, nil); err != nil {
		return wrapUser(fmt.Errorf("写入 %s 失败：%w", HeaderName, err))
	}
	return nil
}

func sameHeader(a, b *Header) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Encrypted() != b.Encrypted() {
		return false
	}
	if !a.Encrypted() {
		return true
	}
	return a.Encryption.Salt == b.Encryption.Salt && a.Encryption.KeyCheck == b.Encryption.KeyCheck
}

// hasBackupFiles 判断目标上是否已经有 Lemon Note 的备份文件（不算目标头）。
func hasBackupFiles(ctx context.Context, t target.Target) (bool, error) {
	for _, prefix := range []string{"lemon-note-", "mirror-"} {
		list, err := t.List(ctx, prefix)
		if err != nil {
			return false, wrapUser(fmt.Errorf("列出目标上的文件失败：%w", err))
		}
		for _, e := range list {
			if e.Path != HeaderName {
				return true, nil
			}
		}
	}
	return false, nil
}

// prepareBackupKey 在备份前核对目标头与当前加密设置，必要时写目标头，返回加密密钥（不加密为 nil）。
//
// 规则：目标上已经有备份时，加密开关与口令必须和目标头一致，否则拒绝执行（不同口令的备份混在一起，
// 镜像里会出现用旧口令加密、大小却对得上的文件，恢复时才发现解不开）。目标上还没有任何备份时，按当前设置重写目标头。
func prepareBackupKey(ctx context.Context, t target.Target, enabled bool, password string, p *Progress) ([]byte, *Header, error) {
	if enabled && password == "" {
		return nil, nil, uerr("已开启加密，但没有设置备份口令")
	}
	if !enabled {
		password = ""
	}
	h, err := ReadHeader(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	if h != nil {
		if h.Encrypted() == enabled {
			if !enabled {
				return nil, h, nil
			}
			key, err := h.Key(password)
			if err == nil {
				return key, h, nil
			}
			if !errors.Is(err, ErrWrongPassword) {
				return nil, nil, err
			}
		}
		has, err := hasBackupFiles(ctx, t)
		if err != nil {
			return nil, nil, err
		}
		if has {
			switch {
			case h.Encrypted() && !enabled:
				return nil, nil, uerr("目标上已有加密的备份，但当前设置没有开启加密。请恢复原来的加密设置，或者换一个空的目录（子目录）存放新备份")
			case !h.Encrypted() && enabled:
				return nil, nil, uerr("目标上已有未加密的备份，不能直接改成加密。请换一个空的目录（子目录）存放加密备份，或者先清空该目标")
			default:
				return nil, nil, uerr("当前设置的备份口令与目标上已有备份的口令不一致。请改回原来的口令，或者换一个空的目录（子目录）存放新备份")
			}
		}
		p.Logf("目标上还没有备份，按当前加密设置重写 %s", HeaderName)
	} else {
		// 目标头缺失：如果目标上已有备份文件，加密时无法确认它们用的是哪个口令，拒绝执行。
		if enabled {
			has, err := hasBackupFiles(ctx, t)
			if err != nil {
				return nil, nil, err
			}
			if has {
				return nil, nil, uerrf("目标上已有备份文件，却缺少 %s，无法确认它们的加密口令。请换一个空的目录（子目录）存放加密备份", HeaderName)
			}
		}
	}
	nh, key := newHeader(password)
	if err := writeHeader(ctx, t, nh); err != nil {
		return nil, nil, err
	}
	// 回读确认：两个实例同时初始化同一个目标时，以最后写入的为准。
	got, err := ReadHeader(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	if got == nil {
		return nil, nil, uerrf("写入 %s 后读不到它，目标可能不可靠", HeaderName)
	}
	if !sameHeader(got, nh) {
		if got.Encrypted() != enabled {
			return nil, nil, uerr("另一个程序刚刚用不同的加密设置初始化了这个目标，请检查是否有多个实例在备份到同一个目标")
		}
		key, err = got.Key(password)
		if err != nil {
			return nil, nil, err
		}
		return key, got, nil
	}
	return key, nh, nil
}

// checkHeaderUnchanged 在备份结束前确认目标头没有被别人改掉（否则刚写的数据用的密钥已经对不上）。
func checkHeaderUnchanged(ctx context.Context, t target.Target, h *Header) error {
	got, err := ReadHeader(ctx, t)
	if err != nil {
		return err
	}
	if !sameHeader(got, h) {
		return uerrf("备份过程中目标上的 %s 被修改或删除了，本次备份可能无法解密，请检查是否有其他程序在使用这个目标", HeaderName)
	}
	return nil
}

// OpenKey 为恢复、校验、列出备份准备密钥：按目标头判断是否加密，加密时用口令派生并核对。
func OpenKey(ctx context.Context, t target.Target, password string) ([]byte, *Header, error) {
	h, err := ReadHeader(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	if !h.Encrypted() {
		return nil, h, nil
	}
	key, err := h.Key(password)
	if err != nil {
		return nil, h, err
	}
	return key, h, nil
}

// ---------------- 读写辅助 ----------------

// getBytes 读取一个小文件（可选解密），超过 limit 字节报错。
func getBytes(ctx context.Context, t target.Target, path string, key []byte, limit int64) ([]byte, error) {
	rc, err := t.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var r io.Reader = rc
	if key != nil {
		dr, err := cryptstream.NewDecryptReader(rc, key)
		if err != nil {
			return nil, decryptErr(path, err)
		}
		r = dr
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		if key != nil {
			return nil, decryptErr(path, err)
		}
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, uerrf("「%s」太大，不像是 Lemon Note 写的文件", path)
	}
	return data, nil
}

func decryptErr(path string, err error) error {
	switch {
	case errors.Is(err, cryptstream.ErrNotEncrypted):
		return &Error{msg: fmt.Sprintf("「%s」不是加密文件，但目标设置为加密，目标上的文件可能被替换过", path), err: err}
	case errors.Is(err, cryptstream.ErrCorrupt):
		return &Error{msg: fmt.Sprintf("「%s」解密失败：文件已损坏、被篡改或不完整", path), err: err}
	case errors.Is(err, cryptstream.ErrUnsupportedVersion):
		return &Error{msg: fmt.Sprintf("「%s」的加密格式版本不受支持，请升级程序", path), err: err}
	}
	return err
}

// isCorruptErr 判断是不是文件内容损坏（解密失败、不是加密文件），而不是读取本身失败（网络、权限）。
func isCorruptErr(err error) bool {
	return errors.Is(err, cryptstream.ErrCorrupt) || errors.Is(err, cryptstream.ErrNotEncrypted)
}

// putBytes 写一个小文件（可选加密）。
func putBytes(ctx context.Context, t target.Target, path string, data []byte, key []byte) error {
	if key == nil {
		return t.Put(ctx, path, bytes.NewReader(data), int64(len(data)))
	}
	var buf bytes.Buffer
	w, err := cryptstream.NewEncryptWriter(&buf, key)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return t.Put(ctx, path, &buf, int64(buf.Len()))
}

// storedSize 返回明文 size 字节的文件在目标上的大小。
func storedSize(size int64, key []byte) int64 {
	if key == nil {
		return size
	}
	return cryptstream.EncryptedSize(size)
}

// errHashMismatch 表示上传 / 下载的内容与预期的 sha256 不一致。
type errHashMismatch struct {
	path      string
	want, got string
}

func (e *errHashMismatch) Error() string {
	return fmt.Sprintf("「%s」的内容校验不一致（期望 sha256 %s…，实际 %s…）", e.path, short(e.want), short(e.got))
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// putFile 把本地文件上传到目标（可选加密），同时计算 sha256；wantSHA 非空时核对。
// 返回上传的明文字节数。
func putFile(ctx context.Context, t target.Target, dst, src string, key []byte, wantSHA string, p *Progress) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	h := sha256.New()
	var r io.Reader = io.TeeReader(f, h)
	if p != nil {
		r = countingReader{r: r, p: p}
	}
	if key == nil {
		if err := t.Put(ctx, dst, r, size); err != nil {
			return 0, err
		}
	} else {
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			ew, err := cryptstream.NewEncryptWriter(pw, key)
			if err == nil {
				_, err = io.Copy(ew, r)
				if err == nil {
					err = ew.Close()
				}
			}
			pw.CloseWithError(err)
			done <- err
		}()
		perr := t.Put(ctx, dst, pr, cryptstream.EncryptedSize(size))
		pr.CloseWithError(errors.New("上传已结束"))
		gerr := <-done
		if perr != nil {
			return 0, perr
		}
		if gerr != nil {
			return 0, gerr
		}
	}
	if wantSHA != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA) {
			return size, &errHashMismatch{path: dst, want: wantSHA, got: got}
		}
	}
	return size, nil
}
