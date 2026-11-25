// Package fracidx 实现分数索引（与 rocicorp/fractional-indexing 同算法），
// 用字符串排序键表达兄弟节点顺序，插入任意两个键之间都不需要改动其他行。
// 前端 TS 实现必须与这里逐字节一致，两边共用 testdata/vectors.json 测试向量。
package fracidx

import (
	"errors"
	"strings"
)

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var errInvalid = errors.New("fracidx: invalid order key")

func midpoint(a, b string, hasB bool) (string, error) {
	zero := digits[0:1]
	if hasB && a >= b {
		return "", errors.New("fracidx: a >= b")
	}
	if strings.HasSuffix(a, zero) || (hasB && strings.HasSuffix(b, zero)) {
		return "", errors.New("fracidx: trailing zero")
	}
	if hasB {
		n := 0
		for {
			ca := byte('0')
			if n < len(a) {
				ca = a[n]
			}
			if n >= len(b) || ca != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			rest, err := midpoint(a[min(n, len(a)):], b[n:], true)
			if err != nil {
				return "", err
			}
			return b[:n] + rest, nil
		}
	}
	digitA := 0
	if a != "" {
		digitA = strings.IndexByte(digits, a[0])
	}
	digitB := len(digits)
	if hasB {
		digitB = strings.IndexByte(digits, b[0])
	}
	if digitB-digitA > 1 {
		mid := (digitA + digitB + 1) / 2 // 与 JS Math.round(0.5*(a+b)) 一致
		return digits[mid : mid+1], nil
	}
	if hasB && len(b) > 1 {
		return b[:1], nil
	}
	rest, err := midpoint(tail(a), "", false)
	if err != nil {
		return "", err
	}
	return digits[digitA:digitA+1] + rest, nil
}

func tail(s string) string {
	if s == "" {
		return ""
	}
	return s[1:]
}

func integerLength(head byte) (int, error) {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2, nil
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2, nil
	}
	return 0, errInvalid
}

func integerPart(key string) (string, error) {
	if key == "" {
		return "", errInvalid
	}
	l, err := integerLength(key[0])
	if err != nil {
		return "", err
	}
	if l > len(key) {
		return "", errInvalid
	}
	return key[:l], nil
}

var smallestInteger = "A" + strings.Repeat("0", 26)

// Validate 检查排序键是否合法。
func Validate(key string) error {
	if key == smallestInteger {
		return errInvalid
	}
	i, err := integerPart(key)
	if err != nil {
		return err
	}
	f := key[len(i):]
	if strings.HasSuffix(f, "0") {
		return errInvalid
	}
	for j := 0; j < len(key); j++ {
		if strings.IndexByte(digits, key[j]) < 0 {
			return errInvalid
		}
	}
	return nil
}

func incrementInteger(x string) (string, bool, error) {
	if _, err := integerLength(x[0]); err != nil {
		return "", false, err
	}
	head := x[0]
	digs := []byte(x[1:])
	carry := true
	for i := len(digs) - 1; carry && i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) + 1
		if d == len(digits) {
			digs[i] = digits[0]
		} else {
			digs[i] = digits[d]
			carry = false
		}
	}
	if carry {
		if head == 'Z' {
			return "a" + digits[0:1], true, nil
		}
		if head == 'z' {
			return "", false, nil
		}
		h := head + 1
		if h > 'a' {
			digs = append(digs, digits[0])
		} else {
			digs = digs[:len(digs)-1]
		}
		return string(h) + string(digs), true, nil
	}
	return string(head) + string(digs), true, nil
}

func decrementInteger(x string) (string, bool, error) {
	if _, err := integerLength(x[0]); err != nil {
		return "", false, err
	}
	head := x[0]
	digs := []byte(x[1:])
	borrow := true
	for i := len(digs) - 1; borrow && i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) - 1
		if d == -1 {
			digs[i] = digits[len(digits)-1]
		} else {
			digs[i] = digits[d]
			borrow = false
		}
	}
	if borrow {
		if head == 'a' {
			return "Z" + digits[len(digits)-1:], true, nil
		}
		if head == 'A' {
			return "", false, nil
		}
		h := head - 1
		if h < 'Z' {
			digs = append(digs, digits[len(digits)-1])
		} else {
			digs = digs[:len(digs)-1]
		}
		return string(h) + string(digs), true, nil
	}
	return string(head) + string(digs), true, nil
}

// Between 返回严格位于 a 与 b 之间的排序键。a 为空表示最前，b 为空表示最后。
func Between(a, b string) (string, error) {
	if a != "" {
		if err := Validate(a); err != nil {
			return "", err
		}
	}
	if b != "" {
		if err := Validate(b); err != nil {
			return "", err
		}
	}
	if a != "" && b != "" && a >= b {
		return "", errors.New("fracidx: a >= b")
	}
	if a == "" {
		if b == "" {
			return "a" + digits[0:1], nil
		}
		ib, err := integerPart(b)
		if err != nil {
			return "", err
		}
		fb := b[len(ib):]
		if ib == smallestInteger {
			m, err := midpoint("", fb, true)
			if err != nil {
				return "", err
			}
			return ib + m, nil
		}
		if ib < b {
			return ib, nil
		}
		res, ok, err := decrementInteger(ib)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("fracidx: cannot decrement any more")
		}
		return res, nil
	}
	ia, err := integerPart(a)
	if err != nil {
		return "", err
	}
	fa := a[len(ia):]
	if b == "" {
		i, ok, err := incrementInteger(ia)
		if err != nil {
			return "", err
		}
		if !ok {
			m, err := midpoint(fa, "", false)
			if err != nil {
				return "", err
			}
			return ia + m, nil
		}
		return i, nil
	}
	ib, err := integerPart(b)
	if err != nil {
		return "", err
	}
	fb := b[len(ib):]
	if ia == ib {
		m, err := midpoint(fa, fb, true)
		if err != nil {
			return "", err
		}
		return ia + m, nil
	}
	i, ok, err := incrementInteger(ia)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("fracidx: cannot increment any more")
	}
	if i < b {
		return i, nil
	}
	m, err := midpoint(fa, "", false)
	if err != nil {
		return "", err
	}
	return ia + m, nil
}

// NBetween 返回 n 个依次递增、都位于 a 与 b 之间的排序键。
func NBetween(a, b string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	if n == 1 {
		k, err := Between(a, b)
		if err != nil {
			return nil, err
		}
		return []string{k}, nil
	}
	if b == "" {
		out := make([]string, 0, n)
		c := a
		for i := 0; i < n; i++ {
			k, err := Between(c, b)
			if err != nil {
				return nil, err
			}
			out = append(out, k)
			c = k
		}
		return out, nil
	}
	if a == "" {
		out := make([]string, n)
		c := b
		for i := n - 1; i >= 0; i-- {
			k, err := Between(a, c)
			if err != nil {
				return nil, err
			}
			out[i] = k
			c = k
		}
		return out, nil
	}
	mid := n / 2
	c, err := Between(a, b)
	if err != nil {
		return nil, err
	}
	left, err := NBetween(a, c, mid)
	if err != nil {
		return nil, err
	}
	right, err := NBetween(c, b, n-mid-1)
	if err != nil {
		return nil, err
	}
	out := append(left, c)
	return append(out, right...), nil
}
