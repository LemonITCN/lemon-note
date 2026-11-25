package cryptstream

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// encrypt 用不规则的写入长度加密，覆盖跨块边界的写入。
func encrypt(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewEncryptWriter(&out, key)
	if err != nil {
		t.Fatal(err)
	}
	steps := []int{1, 7, 1000, ChunkSize - 3, ChunkSize, 3 * ChunkSize, 12345}
	p := plain
	for i := 0; len(p) > 0; i++ {
		k := steps[i%len(steps)]
		if k > len(p) {
			k = len(p)
		}
		n, err := w.Write(p[:k])
		if err != nil || n != k {
			t.Fatalf("Write: n=%d err=%v", n, err)
		}
		p = p[k:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func decryptAll(key, ct []byte) ([]byte, error) {
	r, err := NewDecryptReader(bytes.NewReader(ct), key)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

var sizes = []int{0, 1, 100, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 2*ChunkSize + 17, 3*1024*1024 + 5}

func TestRoundTrip(t *testing.T) {
	key := testKey(t)
	for _, n := range sizes {
		plain := randBytes(t, n)
		ct := encrypt(t, key, plain)
		if int64(len(ct)) != EncryptedSize(int64(n)) {
			t.Fatalf("size %d: 密文长度 %d，EncryptedSize 给出 %d", n, len(ct), EncryptedSize(int64(n)))
		}
		got, err := decryptAll(key, ct)
		if err != nil {
			t.Fatalf("size %d: 解密失败 %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: 往返不一致", n)
		}
		// 一字节一字节地读，覆盖短读路径。
		r, err := NewDecryptReader(iotest.OneByteReader(bytes.NewReader(ct)), key)
		if err != nil {
			t.Fatal(err)
		}
		got, err = io.ReadAll(iotest.OneByteReader(r))
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: 逐字节读取失败 %v", n, err)
		}
	}
}

func TestEncryptedSizeTable(t *testing.T) {
	cases := map[int64]int64{
		-1:                -1,
		0:                 12 + 16,
		1:                 12 + 1 + 16,
		ChunkSize:         12 + ChunkSize + 16,
		ChunkSize + 1:     12 + ChunkSize + 1 + 32,
		10 * ChunkSize:    12 + 10*ChunkSize + 160,
		10*ChunkSize + 99: 12 + 10*ChunkSize + 99 + 176,
	}
	for in, want := range cases {
		if got := EncryptedSize(in); got != want {
			t.Errorf("EncryptedSize(%d)=%d，期望 %d", in, got, want)
		}
	}
}

func TestNonceUnique(t *testing.T) {
	key := testKey(t)
	a := encrypt(t, key, []byte("same"))
	b := encrypt(t, key, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("两次加密结果相同，nonce 前缀没有随机化")
	}
}

func TestTamperEveryByte(t *testing.T) {
	key := testKey(t)
	// 两块多一点，篡改任意一个字节都必须报错（头部、每块密文、每块标签）。
	plain := randBytes(t, ChunkSize+300)
	ct := encrypt(t, key, plain)
	for i := 0; i < len(ct); i++ {
		// 全部字节太多，抽样：头部全测，其余每 97 字节测一个，再加上每块的首尾。
		if i >= HeaderSize && i%97 != 0 && i != len(ct)-1 && i != HeaderSize+sealedChunk-1 && i != HeaderSize+sealedChunk {
			continue
		}
		bad := bytes.Clone(ct)
		bad[i] ^= 0x01
		got, err := decryptAll(key, bad)
		if err == nil {
			t.Fatalf("篡改第 %d 字节后仍解密成功（%d 字节）", i, len(got))
		}
	}
}

func TestTruncation(t *testing.T) {
	key := testKey(t)
	plain := randBytes(t, 3*ChunkSize+10)
	ct := encrypt(t, key, plain)
	cuts := []int{0, 3, HeaderSize - 1, HeaderSize, HeaderSize + 1, HeaderSize + 15,
		HeaderSize + sealedChunk, HeaderSize + sealedChunk + 1,
		HeaderSize + 2*sealedChunk, HeaderSize + 3*sealedChunk, len(ct) - 1}
	for _, c := range cuts {
		_, err := decryptAll(key, ct[:c])
		if err == nil {
			t.Fatalf("截断到 %d 字节后仍解密成功", c)
		}
	}
}

func TestTruncationAtChunkBoundaryExactMultiple(t *testing.T) {
	// 明文正好是整块的倍数时，去掉最后一整块，剩下的仍是整块，必须报错。
	key := testKey(t)
	plain := randBytes(t, 3*ChunkSize)
	ct := encrypt(t, key, plain)
	if len(ct) != HeaderSize+3*sealedChunk {
		t.Fatalf("密文长度意外：%d", len(ct))
	}
	_, err := decryptAll(key, ct[:HeaderSize+2*sealedChunk])
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("删掉最后一块应报 ErrCorrupt，实际 %v", err)
	}
}

func TestDropFinalEmptyChunk(t *testing.T) {
	// 空明文只有一个空的最后一块，删掉它必须报错。
	key := testKey(t)
	ct := encrypt(t, key, nil)
	if _, err := decryptAll(key, ct[:HeaderSize]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("删掉空的最后一块应报 ErrCorrupt，实际 %v", err)
	}
}

func TestMissingFinalChunkFromUnclosedWriter(t *testing.T) {
	// 没有 Close 的写入器只写出了非最后块，解密必须报错。
	key := testKey(t)
	var out bytes.Buffer
	w, err := NewEncryptWriter(&out, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(randBytes(t, 2*ChunkSize+1)); err != nil {
		t.Fatal(err)
	}
	// 此时已写出两块（都标记为非最后块），最后的 1 字节还在缓冲里。
	if _, err := decryptAll(key, out.Bytes()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("缺最后一块应报 ErrCorrupt，实际 %v", err)
	}
}

func TestReorderAndSwap(t *testing.T) {
	key := testKey(t)
	plain := randBytes(t, 3*ChunkSize+5)
	ct := encrypt(t, key, plain)
	c := func(i int) []byte { return ct[HeaderSize+i*sealedChunk : HeaderSize+(i+1)*sealedChunk] }
	// 交换第 0、1 块。
	var sw []byte
	sw = append(sw, ct[:HeaderSize]...)
	sw = append(sw, c(1)...)
	sw = append(sw, c(0)...)
	sw = append(sw, ct[HeaderSize+2*sealedChunk:]...)
	if _, err := decryptAll(key, sw); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("交换块应报 ErrCorrupt，实际 %v", err)
	}
	// 删掉中间一块。
	var del []byte
	del = append(del, ct[:HeaderSize+sealedChunk]...)
	del = append(del, ct[HeaderSize+2*sealedChunk:]...)
	if _, err := decryptAll(key, del); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("删掉中间块应报 ErrCorrupt，实际 %v", err)
	}
	// 用另一个文件（同口令）的块替换。
	other := encrypt(t, key, plain)
	mix := bytes.Clone(ct)
	copy(mix[HeaderSize+sealedChunk:], other[HeaderSize+sealedChunk:HeaderSize+2*sealedChunk])
	if _, err := decryptAll(key, mix); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("混入其他文件的块应报 ErrCorrupt，实际 %v", err)
	}
	// 尾部追加数据。
	if _, err := decryptAll(key, append(bytes.Clone(ct), 0)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("尾部追加数据应报 ErrCorrupt，实际 %v", err)
	}
	// 尾部追加一整块（重复最后一块）。
	if _, err := decryptAll(key, append(bytes.Clone(ct), ct[len(ct)-21:]...)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("尾部重复最后一块应报 ErrCorrupt，实际 %v", err)
	}
}

func TestWrongKeyAndPassword(t *testing.T) {
	salt := NewSalt()
	if len(salt) != 16 {
		t.Fatalf("盐长度 %d", len(salt))
	}
	key := DeriveKey("正确的口令", salt)
	if len(key) != 32 {
		t.Fatalf("密钥长度 %d", len(key))
	}
	if !bytes.Equal(key, DeriveKey("正确的口令", salt)) {
		t.Fatal("同口令同盐派生结果不同")
	}
	wrong := DeriveKey("错误的口令", salt)
	ct := encrypt(t, key, []byte("重要笔记"))
	if _, err := decryptAll(wrong, ct); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("错误口令应报 ErrCorrupt，实际 %v", err)
	}
	check := KeyCheck(key)
	if len(check) != 64 {
		t.Fatalf("校验值长度 %d", len(check))
	}
	if !CheckKey(key, check) {
		t.Fatal("正确口令校验失败")
	}
	if CheckKey(wrong, check) {
		t.Fatal("错误口令校验通过")
	}
	if CheckKey(key, "not-hex") {
		t.Fatal("非法校验值通过")
	}
	if bytes.Equal(DeriveKey("正确的口令", NewSalt()), key) {
		t.Fatal("不同盐派生出相同密钥")
	}
}

func TestHeaderErrors(t *testing.T) {
	key := testKey(t)
	if _, err := NewDecryptReader(bytes.NewReader([]byte("PK\x03\x04 not ours")), key); !errors.Is(err, ErrNotEncrypted) {
		t.Fatalf("魔数错误应报 ErrNotEncrypted，实际 %v", err)
	}
	if _, err := NewDecryptReader(bytes.NewReader(nil), key); !errors.Is(err, ErrNotEncrypted) {
		t.Fatalf("空输入应报 ErrNotEncrypted，实际 %v", err)
	}
	ct := encrypt(t, key, []byte("x"))
	ct[4] = 2
	if _, err := NewDecryptReader(bytes.NewReader(ct), key); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("版本错误应报 ErrUnsupportedVersion，实际 %v", err)
	}
	if _, err := NewEncryptWriter(io.Discard, make([]byte, 16)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("短密钥应报 ErrBadKey，实际 %v", err)
	}
	if _, err := NewDecryptReader(bytes.NewReader(ct), make([]byte, 31)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("短密钥应报 ErrBadKey，实际 %v", err)
	}
}

func TestWriterAfterClose(t *testing.T) {
	key := testKey(t)
	w, err := NewEncryptWriter(io.Discard, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("重复 Close 应返回 nil，实际 %v", err)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Close 后写入应报 ErrClosed，实际 %v", err)
	}
}

type failWriter struct{ n int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("磁盘满")
	}
	f.n--
	return len(p), nil
}

func TestWriterPropagatesError(t *testing.T) {
	key := testKey(t)
	w, err := NewEncryptWriter(&failWriter{n: 1}, key) // 只允许写出头部
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(randBytes(t, 2*ChunkSize)); err == nil {
		t.Fatal("下层写入失败应向上返回错误")
	}
	if err := w.Close(); err == nil {
		t.Fatal("出错后 Close 应返回错误")
	}
}

func TestReaderStopsAfterError(t *testing.T) {
	key := testKey(t)
	ct := encrypt(t, key, randBytes(t, 2*ChunkSize+1))
	ct[len(ct)-1] ^= 1
	r, err := NewDecryptReader(bytes.NewReader(ct), key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("应报 ErrCorrupt，实际 %v", err)
	}
	if n, err2 := r.Read(make([]byte, 10)); n != 0 || !errors.Is(err2, ErrCorrupt) {
		t.Fatalf("出错后再读应继续报错，实际 n=%d err=%v", n, err2)
	}
}
