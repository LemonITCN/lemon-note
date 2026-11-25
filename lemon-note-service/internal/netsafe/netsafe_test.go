package netsafe

import (
	"context"
	"net/url"
	"testing"
)

func TestBlocksInternal(t *testing.T) {
	for _, s := range []string{"http://127.0.0.1/", "http://localhost:8080", "http://10.0.0.1", "http://192.168.1.1", "http://[::1]/", "http://169.254.169.254/latest/meta-data", "file:///etc/passwd", "http://user:pw@example.com"} {
		u, _ := url.Parse(s)
		if CheckURL(u) == nil {
			t.Errorf("%s 应被拦截", s)
		}
	}
	if _, _, err := Get(context.Background(), "http://127.0.0.1:1/", 100, ""); err == nil {
		t.Error("回环地址应被拦截")
	}
}
