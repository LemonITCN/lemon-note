package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret 生成 20 字节的 TOTP 密钥（base32）。
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

// TOTPURL 生成验证器 App 扫码用的 otpauth 链接。
func TOTPURL(issuer, account, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code)
}

var usedCodes sync.Map // userID → 最近使用的计数器，防止同一个验证码被重放

// VerifyTOTP 校验 6 位验证码（允许前后各一个时间窗），同一计数器只能用一次。
func VerifyTOTP(userID, secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return false
	}
	now := uint64(time.Now().Unix() / 30)
	for _, d := range []int64{0, -1, 1} {
		c := uint64(int64(now) + d)
		if hmac.Equal([]byte(hotp(key, c)), []byte(code)) {
			if last, ok := usedCodes.Load(userID); ok && last.(uint64) >= c {
				return false
			}
			usedCodes.Store(userID, c)
			return true
		}
	}
	return false
}

// NewRecoveryCodes 生成 10 个一次性恢复码，返回明文与哈希。
func NewRecoveryCodes() (plain []string, hashes []string) {
	for i := 0; i < 10; i++ {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		s := strings.ToLower(b32.EncodeToString(b))[:10]
		code := s[:5] + "-" + s[5:]
		plain = append(plain, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return
}

// HashRecoveryCode 恢复码本身是高熵随机串，sha256 即可。
func HashRecoveryCode(code string) string {
	code = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	sum := sha256.Sum256([]byte("lemon-note-recovery:" + code))
	return hex.EncodeToString(sum[:])
}
