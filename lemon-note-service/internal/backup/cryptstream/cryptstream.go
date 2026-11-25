// Package cryptstream 给备份文件做流式加密。
//
// 文件格式（版本 1）：
//
//	头部 12 字节：魔数 "LNBK"（4）+ 版本 1（1）+ 随机 nonce 前缀（7）
//	之后是若干块：每块明文 64 KB（最后一块可更短，可为空），用 AES-256-GCM 加密，
//	密文 = 明文长度 + 16 字节认证标签。
//
// 每块的 12 字节 nonce = 7 字节前缀 + 4 字节大端块序号 + 1 字节「是否最后一块」标志（0 或 1），
// 附加数据（AAD）是 12 字节文件头。这样：
//   - 块被替换、重排：序号进 nonce，认证失败；
//   - 文件被截断在块边界：最后读到的那块是按「非最后一块」加密的，按最后一块解密会认证失败；
//   - 文件尾部被追加数据：最后一块按「非最后一块」解密会认证失败；
//   - 不同文件之间换块：nonce 前缀随机且进了 AAD，认证失败。
//
// 除最后一块外，每块密文长度固定为 64 KB + 16，所以解密端不需要长度字段就能切块。
// 空明文也会写出一个空的最后一块（16 字节），因此密文长度永远是 EncryptedSize(明文长度)。
package cryptstream

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	// ChunkSize 是每块明文的大小。
	ChunkSize = 64 * 1024
	// HeaderSize 是文件头长度：魔数 4 + 版本 1 + nonce 前缀 7。
	HeaderSize = 12

	magic        = "LNBK"
	version      = 1
	prefixSize   = 7
	tagSize      = 16
	sealedChunk  = ChunkSize + tagSize
	maxChunks    = 1 << 32 // 块序号是 32 位
	keySize      = 32
	saltSize     = 16
	keyCheckInfo = "lemon-note-backup-check"
)

var (
	// ErrNotEncrypted 表示数据不是本程序加密的备份文件（魔数不对）。
	ErrNotEncrypted = errors.New("不是加密的备份文件")
	// ErrUnsupportedVersion 表示加密格式版本不认识，可能是更新版本的程序写的。
	ErrUnsupportedVersion = errors.New("加密备份文件的格式版本不受支持，请升级程序")
	// ErrCorrupt 表示认证失败：数据被篡改、损坏、截断，或口令不正确。
	ErrCorrupt = errors.New("解密失败：备份文件已损坏、被篡改或不完整，也可能是口令不正确")
	// ErrBadKey 表示密钥长度不是 32 字节。
	ErrBadKey = errors.New("加密密钥必须是 32 字节")
	// ErrTooLarge 表示单个文件超过格式上限（2^32 块，约 256 TB）。
	ErrTooLarge = errors.New("文件太大，超出加密格式上限")
	// ErrClosed 表示对已关闭的加密写入器继续写入。
	ErrClosed = errors.New("加密写入器已关闭")
)

// DeriveKey 用 argon2id（t=3, m=64 MB, p=2）从口令和盐派生 32 字节密钥。
func DeriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, keySize)
}

// NewSalt 生成 16 字节随机盐。
func NewSalt() []byte {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		panic(err) // crypto/rand 在受支持的平台上不会失败
	}
	return salt
}

// KeyCheck 返回 HMAC-SHA256(key, "lemon-note-backup-check") 的十六进制，
// 写进目标根目录的 lemon-note-backup.json，不解密任何文件也能校验口令。
func KeyCheck(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(keyCheckInfo))
	return hex.EncodeToString(m.Sum(nil))
}

// CheckKey 用常量时间比较校验 key 与记录的校验值是否一致。
func CheckKey(key []byte, check string) bool {
	want, err := hex.DecodeString(check)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(keyCheckInfo))
	return hmac.Equal(m.Sum(nil), want)
}

// EncryptedSize 返回 plain 字节明文加密后的精确长度；plain < 0 时返回 -1。
func EncryptedSize(plain int64) int64 {
	if plain < 0 {
		return -1
	}
	chunks := (plain + ChunkSize - 1) / ChunkSize
	if chunks == 0 {
		chunks = 1 // 空明文也有一个空的最后一块
	}
	return HeaderSize + plain + chunks*tagSize
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(nonce *[12]byte, prefix []byte, seq uint64, last bool) {
	copy(nonce[:prefixSize], prefix)
	binary.BigEndian.PutUint32(nonce[prefixSize:prefixSize+4], uint32(seq))
	if last {
		nonce[11] = 1
	} else {
		nonce[11] = 0
	}
}

// ---------------- 加密 ----------------

type encryptWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	header [HeaderSize]byte
	wrote  bool // 头部是否已写出
	buf    []byte
	out    []byte
	seq    uint64
	nonce  [12]byte
	err    error
	closed bool
}

// NewEncryptWriter 返回一个加密写入器：写入的明文按格式加密后写到 w。
// 必须调用 Close 才会写出最后一块；没有 Close 的密文会被解密端判定为截断。
// Close 不会关闭 w。
func NewEncryptWriter(w io.Writer, key []byte) (io.WriteCloser, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	ew := &encryptWriter{
		w:    w,
		aead: aead,
		buf:  make([]byte, 0, ChunkSize),
		out:  make([]byte, 0, sealedChunk),
	}
	copy(ew.header[:4], magic)
	ew.header[4] = version
	if _, err := rand.Read(ew.header[5:]); err != nil {
		return nil, err
	}
	return ew, nil
}

func (e *encryptWriter) writeHeader() error {
	if e.wrote {
		return nil
	}
	if _, err := e.w.Write(e.header[:]); err != nil {
		return err
	}
	e.wrote = true
	return nil
}

func (e *encryptWriter) seal(last bool) error {
	if e.seq >= maxChunks {
		return ErrTooLarge
	}
	chunkNonce(&e.nonce, e.header[5:], e.seq, last)
	e.out = e.aead.Seal(e.out[:0], e.nonce[:], e.buf, e.header[:])
	if _, err := e.w.Write(e.out); err != nil {
		return err
	}
	e.seq++
	e.buf = e.buf[:0]
	return nil
}

func (e *encryptWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, ErrClosed
	}
	if e.err != nil {
		return 0, e.err
	}
	if err := e.writeHeader(); err != nil {
		e.err = err
		return 0, err
	}
	n := 0
	for len(p) > 0 {
		// 缓冲区满了而且还有数据，才能确定它不是最后一块。
		if len(e.buf) == ChunkSize {
			if err := e.seal(false); err != nil {
				e.err = err
				return n, err
			}
		}
		k := copy(e.buf[len(e.buf):ChunkSize], p)
		e.buf = e.buf[:len(e.buf)+k]
		p = p[k:]
		n += k
	}
	return n, nil
}

// Close 写出最后一块（可能为空）。重复调用返回第一次的结果。
func (e *encryptWriter) Close() error {
	if e.closed {
		return e.err
	}
	e.closed = true
	if e.err != nil {
		return e.err
	}
	if err := e.writeHeader(); err != nil {
		e.err = err
		return err
	}
	if err := e.seal(true); err != nil {
		e.err = err
		return err
	}
	return nil
}

// ---------------- 解密 ----------------

type decryptReader struct {
	r      *bufio.Reader
	aead   cipher.AEAD
	header [HeaderSize]byte
	in     []byte
	plain  []byte // 已认证、尚未交给调用方的明文
	seq    uint64
	nonce  [12]byte
	done   bool // 已认证最后一块
	err    error
}

// NewDecryptReader 读取并检查文件头，返回明文读取器。
//
// 重要：解密是逐块进行的，每块都先认证再交出明文，但截断、缺最后一块这类问题
// 只有读到文件末尾才能发现。所以只有 Read 最终返回 io.EOF 时，读到的数据才是完整的；
// 中途返回任何其他错误时，调用方必须丢弃已经读到的全部数据（例如删除恢复出的临时文件），
// 绝不能把部分明文当作完整文件使用。
func NewDecryptReader(r io.Reader, key []byte) (io.Reader, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	d := &decryptReader{
		r:    bufio.NewReaderSize(r, sealedChunk+1),
		aead: aead,
		in:   make([]byte, sealedChunk),
	}
	if _, err := io.ReadFull(d.r, d.header[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, ErrNotEncrypted
		}
		return nil, err
	}
	if string(d.header[:4]) != magic {
		return nil, ErrNotEncrypted
	}
	if d.header[4] != version {
		return nil, ErrUnsupportedVersion
	}
	return d, nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(d.plain) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}

// next 读入并认证下一块。
func (d *decryptReader) next() error {
	n, err := io.ReadFull(d.r, d.in)
	var last bool
	switch {
	case err == nil:
		// 读满一整块：后面没有数据了它才是最后一块。
		if _, perr := d.r.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return perr
		}
	case err == io.ErrUnexpectedEOF:
		// 不满一整块，只可能是最后一块。
		last = true
	case err == io.EOF:
		// 上一块不是最后一块，数据却没了。
		return ErrCorrupt
	default:
		return err
	}
	if n < tagSize {
		return ErrCorrupt
	}
	if d.seq >= maxChunks {
		return ErrCorrupt
	}
	chunkNonce(&d.nonce, d.header[5:], d.seq, last)
	plain, oerr := d.aead.Open(d.in[:0], d.nonce[:], d.in[:n], d.header[:])
	if oerr != nil {
		return ErrCorrupt
	}
	d.seq++
	d.plain = plain
	d.done = last
	return nil
}
