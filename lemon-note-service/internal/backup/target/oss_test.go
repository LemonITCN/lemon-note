package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 阿里云「在 Header 中包含 V4 签名」文档里的规范请求示例，哈希值是文档给出的。
func TestOSSCanonicalRequestDocVector(t *testing.T) {
	headers := map[string]string{
		"content-disposition":  "attachment",
		"content-length":       "3",
		"content-md5":          "ICy5YqxZB1uWSwcVLSNLcA==",
		"content-type":         "text/plain",
		"x-oss-content-sha256": "UNSIGNED-PAYLOAD",
		"x-oss-date":           "20250411T064124Z",
	}
	cr := ossCanonicalRequest("PUT", "/examplebucket/exampleobject", nil, headers, []string{"content-length", "content-disposition"})
	sum := sha256.Sum256([]byte(cr))
	if got := hex.EncodeToString(sum[:]); got != "c46d96390bdbc2d739ac9363293ae9d710b14e48081fcb22cd8ad54b63136eca" {
		t.Fatalf("规范请求哈希与文档不一致：%s\n%s", got, cr)
	}
}

func TestOSSCanonicalQuery(t *testing.T) {
	q := url.Values{"uploads": {""}, "prefix": {"a b/中"}, "list-type": {"2"}, "continuation-token": {"x+y=z"}}
	got := ossCanonicalQuery(q)
	want := "continuation-token=x%2By%3Dz&list-type=2&prefix=a%20b%2F%E4%B8%AD&uploads"
	if got != want {
		t.Fatalf("规范查询串 %q，期望 %q", got, want)
	}
	if ossURIEncode("a b/c~d!", true) != "a%20b/c~d%21" {
		t.Fatalf("URI 编码不对：%s", ossURIEncode("a b/c~d!", true))
	}
}

func TestOSSEndpoint(t *testing.T) {
	cases := []struct {
		in     OSSConfig
		scheme string
		host   string
	}{
		{OSSConfig{Region: "cn-hangzhou", Bucket: "notes"}, "https", "notes.oss-cn-hangzhou.aliyuncs.com"},
		{OSSConfig{Region: "oss-cn-shanghai", Bucket: "notes", Internal: true}, "https", "notes.oss-cn-shanghai-internal.aliyuncs.com"},
		{OSSConfig{Region: "cn-hangzhou", Bucket: "notes", Endpoint: "oss-accelerate.aliyuncs.com"}, "https", "notes.oss-accelerate.aliyuncs.com"},
		{OSSConfig{Region: "cn-hangzhou", Bucket: "notes", Endpoint: "https://notes.oss-cn-hangzhou.aliyuncs.com/"}, "https", "notes.oss-cn-hangzhou.aliyuncs.com"},
		{OSSConfig{Region: "cn-hangzhou", Bucket: "notes", Endpoint: "backup.example.com"}, "https", "backup.example.com"},
		{OSSConfig{Region: "cn-hangzhou", Bucket: "notes", Endpoint: "http://backup.example.com:8080"}, "http", "backup.example.com:8080"},
	}
	for _, c := range cases {
		s, h, err := ossEndpointFrom(c.in)
		if err != nil || s != c.scheme || h != c.host {
			t.Errorf("%+v => %s://%s %v，期望 %s://%s", c.in, s, h, err, c.scheme, c.host)
		}
	}
	for _, c := range []OSSConfig{
		{Region: "杭州", Bucket: "notes", AccessKeyID: "a", AccessKeySecret: "s"},
		{Region: "cn-hangzhou", Bucket: "No_Upper", AccessKeyID: "a", AccessKeySecret: "s"},
		{Region: "cn-hangzhou", Bucket: "notes"},
		{Region: "cn-hangzhou", Bucket: "notes", AccessKeyID: "a", AccessKeySecret: "s", Endpoint: "https://x.com/path"},
	} {
		b, _ := json.Marshal(c)
		if _, err := Open(KindOSS, b, Deps{}); err == nil {
			t.Errorf("%+v 应报错", c)
		}
	}
}

func TestOSSRequestShape(t *testing.T) {
	rt := &recordRT{resp: status(404, "")}
	cfg, _ := json.Marshal(OSSConfig{Region: "cn-hangzhou", Bucket: "notes", Prefix: "lemon", AccessKeyID: "LTAItest", AccessKeySecret: "topsecret"})
	tg, err := Open(KindOSS, cfg, Deps{HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tg.Stat(context.Background(), "a b/c.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("404 应映射为 ErrNotExist，实际 %v", err)
	}
	req := rt.reqs[0]
	if req.Method != http.MethodHead || req.URL.Scheme != "https" || req.URL.Host != "notes.oss-cn-hangzhou.aliyuncs.com" || req.URL.EscapedPath() != "/lemon/a%20b/c.bin" {
		t.Fatalf("请求不对：%s %s", req.Method, req.URL)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "OSS4-HMAC-SHA256 Credential=LTAItest/") || !strings.Contains(auth, "/cn-hangzhou/oss/aliyun_v4_request, Signature=") {
		t.Fatalf("签名头不对：%s", auth)
	}
	if req.Header.Get("x-oss-content-sha256") != "UNSIGNED-PAYLOAD" || req.Header.Get("x-oss-date") == "" {
		t.Fatal("缺少 x-oss-content-sha256 / x-oss-date")
	}
	if strings.Contains(auth, "topsecret") {
		t.Fatal("签名头里出现了密钥")
	}
}

func TestOSSPublicEndpointForbidden(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>PublicEndpointForbidden: ...</Message><EC>0048-00000401</EC></Error>`
	rt := &recordRT{resp: status(403, body)}
	cfg, _ := json.Marshal(OSSConfig{Region: "cn-hangzhou", Bucket: "notes", AccessKeyID: "a", AccessKeySecret: "s"})
	tg, _ := Open(KindOSS, cfg, Deps{HTTPClient: &http.Client{Transport: rt}})
	_, err := tg.List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "自定义域名") {
		t.Fatalf("默认域名被禁时应提示绑定自定义域名，实际 %v", err)
	}
}

func TestOSSSuite(t *testing.T) {
	fs := newFakeObjStore(t, "notes-backup", false)
	fs.ossAK, fs.ossSecret, fs.ossRegion = "LTAI5tTest", "test-secret", "cn-hangzhou"
	cfg, _ := json.Marshal(OSSConfig{Region: "oss-cn-hangzhou", Bucket: "notes-backup", Prefix: "lemon/备份",
		AccessKeyID: "LTAI5tTest", AccessKeySecret: "test-secret", Endpoint: fs.srv.URL})
	tg, err := Open(KindOSS, cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	runSuite(t, tg, suiteOpts{bigSize: 2*ossPartSize + 12345})
	if fs.badSigs != 0 {
		t.Fatalf("有 %d 个请求签名校验失败", fs.badSigs)
	}
	multipart := false
	for _, r := range fs.requests {
		if strings.Contains(r, "uploads") {
			multipart = true
		}
	}
	if !multipart {
		t.Error("大文件没有走分片上传")
	}
	if fs.md5Seen == 0 {
		t.Error("上传没有带 Content-MD5")
	}
	for _, k := range fs.keys() {
		if !strings.HasPrefix(k, "lemon/备份/") {
			t.Errorf("对象 %q 不在 prefix 下", k)
		}
	}
	if len(fs.uploads) != 0 {
		t.Errorf("有 %d 个未完成的分片上传没有清理", len(fs.uploads))
	}
}

func TestOSSWrongSecret(t *testing.T) {
	fs := newFakeObjStore(t, "notes-backup", false)
	fs.ossAK, fs.ossSecret, fs.ossRegion = "LTAI5tTest", "test-secret", "cn-hangzhou"
	cfg, _ := json.Marshal(OSSConfig{Region: "cn-hangzhou", Bucket: "notes-backup", AccessKeyID: "LTAI5tTest", AccessKeySecret: "wrong", Endpoint: fs.srv.URL})
	tg, _ := Open(KindOSS, cfg, Deps{})
	err := Probe(context.Background(), tg)
	if err == nil || !strings.Contains(err.Error(), "AccessKey Secret 不正确") {
		t.Fatalf("密钥错误提示不对：%v", err)
	}
}

func TestOSSSigningClock(t *testing.T) {
	// 固定时间下签名稳定，且日期进 scope。
	o := &ossTarget{region: "cn-hangzhou", bucket: "b", ak: "ak", sk: "sk",
		now: func() time.Time { return time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC) }}
	req, _ := http.NewRequest(http.MethodGet, "https://b.oss-cn-hangzhou.aliyuncs.com/k", nil)
	o.sign(req, "k", nil)
	if req.Header.Get("x-oss-date") != "20261125T010203Z" || !strings.Contains(req.Header.Get("Authorization"), "ak/20261125/cn-hangzhou/oss/aliyun_v4_request") {
		t.Fatalf("签名时间不对：%v", req.Header)
	}
}
