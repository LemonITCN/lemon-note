package web

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

func jsonUnmarshal(s string, dst any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), dst)
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

// ipLimiter 简单的每 IP 令牌桶，用于未登录可访问的接口。
type ipLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	last    time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newIPLimiter() *ipLimiter { return &ipLimiter{buckets: map[string]*bucket{}} }

// Allow 每个 key 每秒 rate 个、最多 burst 个。
func (l *ipLimiter) Allow(key string, rate, burst float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.last) > 10*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.at) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.last = now
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: burst, at: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.at).Seconds() * rate
	if b.tokens > burst {
		b.tokens = burst
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
