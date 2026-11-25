// Package netsafe 提供防 SSRF 的 HTTP 客户端：服务端代用户访问外部网址（抓网页、书签预览、URL 导入、搜索）时一律使用。
// 解析出的地址若是回环、私有、链路本地、组播等内网地址则拒绝；重定向逐跳检查；限制响应大小与时长。
package netsafe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var ErrBlocked = errors.New("目标地址是内网或保留地址，已拦截")

// AllowPrivate 为真时不拦截内网地址（管理员可在设置里打开，用于访问家里 NAS 上的服务）。
var AllowPrivate = false

func blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if AllowPrivate {
		return ip.IsUnspecified() || ip.IsMulticast()
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var reserved = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32", "64:ff9b::/96", "100::/64", "2001:db8::/32", "fc00::/7", "fec0::/10"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// dialControl 在真正建立连接时检查目标 IP（防 DNS 重绑定）。
func dialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if blocked(ip) {
		return ErrBlocked
	}
	return nil
}

// Client 返回安全的 HTTP 客户端。
func Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: dialControl}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		Proxy:                 nil, // 不走环境代理，避免绕过检查
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("重定向次数过多")
		}
		return CheckURL(req.URL)
	}}
}

// CheckURL 检查 URL 协议与主机。
func CheckURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("只支持 http 与 https 地址")
	}
	if u.Hostname() == "" {
		return errors.New("地址缺少主机名")
	}
	if u.User != nil {
		return errors.New("地址里不能带用户名口令")
	}
	h := strings.ToLower(u.Hostname())
	if !AllowPrivate && (h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal")) {
		return ErrBlocked
	}
	if ip, err := netip.ParseAddr(h); err == nil && blocked(ip) {
		return ErrBlocked
	}
	return nil
}

// Get 安全地 GET 一个地址，最多读 maxBytes 字节。
func Get(ctx context.Context, raw string, maxBytes int64, accept string) (*http.Response, []byte, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, nil, errors.New("地址格式不正确")
	}
	if err := CheckURL(u); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; LemonNote/1.0; +https://github.com/LemonITCN/lemon-note)")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := Client(30 * time.Second).Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return nil, nil, ErrBlocked
		}
		return nil, nil, fmt.Errorf("访问失败：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return resp, nil, fmt.Errorf("读取失败：%v", err)
	}
	return resp, body, nil
}
