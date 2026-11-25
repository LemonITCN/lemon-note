package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 0, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 20, IdleConnTimeout: 90 * time.Second,
	TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 120 * time.Second}}

// postStream 发起流式请求；非 2xx 时读出错误信息。
func postStream(ctx context.Context, url string, headers map[string]string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &APIError{Status: resp.StatusCode, Message: errorMessage(data), Retryable: retryableStatus(resp.StatusCode)}
	}
	return resp, nil
}

func errorMessage(data []byte) string {
	var v struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
	}
	if json.Unmarshal(data, &v) == nil {
		switch e := v.Error.(type) {
		case string:
			return e
		case map[string]any:
			if m, ok := e["message"].(string); ok {
				return m
			}
		}
		if v.Msg != "" {
			return v.Msg
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
