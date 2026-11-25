// Package ids 生成全系统统一的主键：26 位小写 ULID（时间有序、全局唯一）。
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New 返回一个新的 26 位小写 ULID。
func New() string {
	mu.Lock()
	defer mu.Unlock()
	id := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
	return strings.ToLower(id.String())
}

// Valid 判断字符串是不是合法的 ID。
func Valid(s string) bool {
	if len(s) != 26 {
		return false
	}
	_, err := ulid.ParseStrict(strings.ToUpper(s))
	return err == nil
}

// RandomHex 返回 n 字节随机数的十六进制串，用于令牌、文件名等。
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// RandomToken 返回 URL 安全的随机令牌（base62），长度为 n 个字符。
func RandomToken(n int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	buf := make([]byte, n*2)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	j := 0
	for i := 0; i < n; {
		if j >= len(buf) {
			if _, err := rand.Read(buf); err != nil {
				panic(err)
			}
			j = 0
		}
		v := buf[j]
		j++
		if v >= 248 { // 拒绝采样，避免取模偏差
			continue
		}
		b[i] = alphabet[int(v)%62]
		i++
	}
	return string(b)
}
