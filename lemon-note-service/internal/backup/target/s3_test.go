package target

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestS3Options(t *testing.T) {
	f := false
	cases := []struct {
		in       S3Config
		endpoint string
		secure   bool
		lookup   minio.BucketLookupType
	}{
		{S3Config{Endpoint: "s3.amazonaws.com", Region: "us-west-2", Bucket: "bkt", AccessKey: "a", SecretKey: "s"}, "s3.amazonaws.com", true, minio.BucketLookupDNS},
		{S3Config{Endpoint: "minio.lan:9000", UseSSL: &f, PathStyle: true, Bucket: "bkt", AccessKey: "a", SecretKey: "s"}, "minio.lan:9000", false, minio.BucketLookupPath},
		{S3Config{Endpoint: "http://10.0.0.2:9000/", Bucket: "bkt", AccessKey: "a", SecretKey: "s"}, "10.0.0.2:9000", false, minio.BucketLookupDNS},
		{S3Config{Endpoint: "https://r2.example.com", UseSSL: &f, Bucket: "bkt", AccessKey: "a", SecretKey: "s"}, "r2.example.com", true, minio.BucketLookupDNS},
	}
	for _, c := range cases {
		o, err := s3OptionsFrom(c.in)
		if err != nil {
			t.Fatalf("%+v: %v", c.in, err)
		}
		if o.endpoint != c.endpoint || o.secure != c.secure || o.lookup != c.lookup {
			t.Errorf("%+v => endpoint=%s secure=%v lookup=%v", c.in, o.endpoint, o.secure, o.lookup)
		}
	}
	// use_ssl 缺省为 true
	var c S3Config
	json.Unmarshal([]byte(`{"endpoint":"x.com","bucket":"bkt","access_key":"a","secret_key":"s"}`), &c)
	if o, _ := s3OptionsFrom(c); !o.secure {
		t.Error("use_ssl 缺省应为 true")
	}
	bad := []S3Config{
		{Bucket: "b", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "x.com", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "x.com", Bucket: "b"},
		{Endpoint: "https://x.com/sub", Bucket: "b", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "x.com", Bucket: "bkt", AccessKey: "a", SecretKey: "s", Prefix: "a/../b"},
		{Endpoint: "x.com", Bucket: "b", AccessKey: "a", SecretKey: "s"},
	}
	for _, c := range bad {
		if _, err := s3OptionsFrom(c); err == nil {
			t.Errorf("%+v 应报错", c)
		}
	}
}

func TestCOSOptions(t *testing.T) {
	o, err := cosOptionsFrom(COSConfig{Region: "ap-guangzhou", Bucket: "backup-1250000000", Prefix: "/notes/", SecretID: "id", SecretKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if o.endpoint != "cos.ap-guangzhou.myqcloud.com" || !o.secure || o.lookup != minio.BucketLookupDNS || o.region != "ap-guangzhou" || o.prefix != "notes" || !o.listV1 {
		t.Fatalf("COS 参数不对：%+v", o)
	}
	for _, c := range []COSConfig{
		{Region: "广州", Bucket: "backup-1250000000", SecretID: "i", SecretKey: "k"},
		{Region: "ap-guangzhou", Bucket: "backup", SecretID: "i", SecretKey: "k"},
		{Region: "ap-guangzhou", Bucket: "backup-1250000000"},
	} {
		if _, err := cosOptionsFrom(c); err == nil {
			t.Errorf("%+v 应报错", c)
		}
	}
}

// recordRT 记录请求、返回固定响应，不连网。
type recordRT struct {
	mu   sync.Mutex
	reqs []*http.Request
	resp func(*http.Request) *http.Response
}

func (r *recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	if req.Body != nil {
		io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	return r.resp(req), nil
}

func status(code int, body string) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		h := http.Header{}
		if body != "" {
			h.Set("Content-Type", "application/xml")
		}
		return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req, ContentLength: int64(len(body))}
	}
}

func TestCOSRequestShape(t *testing.T) {
	rt := &recordRT{resp: status(404, "")}
	cfg, _ := json.Marshal(COSConfig{Region: "ap-guangzhou", Bucket: "backup-1250000000", Prefix: "notes", SecretID: "AKIDtest", SecretKey: "secret"})
	tg, err := Open(KindCOS, cfg, Deps{HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tg.Stat(context.Background(), "a/b.bin"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("404 应映射为 ErrNotExist，实际 %v", err)
	}
	if len(rt.reqs) != 1 {
		t.Fatalf("应只发 1 个请求（不探测地域），实际 %d", len(rt.reqs))
	}
	req := rt.reqs[0]
	if req.URL.Scheme != "https" || req.URL.Host != "backup-1250000000.cos.ap-guangzhou.myqcloud.com" || req.URL.Path != "/notes/a/b.bin" {
		t.Fatalf("请求地址不对：%s", req.URL)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDtest/") || !strings.Contains(auth, "/ap-guangzhou/s3/aws4_request") {
		t.Fatalf("签名头不对：%s", auth)
	}
	if strings.Contains(auth, "secret") {
		t.Fatal("签名头里出现了密钥")
	}
}

func TestS3ErrorMessages(t *testing.T) {
	body := `<?xml version="1.0"?><Error><Code>SignatureDoesNotMatch</Code><Message>sig</Message></Error>`
	rt := &recordRT{resp: status(403, body)}
	cfg, _ := json.Marshal(S3Config{Endpoint: "s3.example.com", Region: "us-east-1", Bucket: "bucket", AccessKey: "a", SecretKey: "topsecret"})
	tg, _ := Open(KindS3, cfg, Deps{HTTPClient: &http.Client{Transport: rt}})
	_, err := tg.List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "Secret Key 不正确") || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("错误提示不对：%v", err)
	}
}

func TestS3Suite(t *testing.T) {
	fs := newFakeObjStore(t, "notes-bucket", true)
	u, _ := url.Parse(fs.srv.URL)
	f := false
	cfg, _ := json.Marshal(S3Config{Endpoint: u.Host, Region: "us-east-1", Bucket: "notes-bucket", Prefix: "team/backup",
		AccessKey: "minio", SecretKey: "minio123", UseSSL: &f, PathStyle: true})
	tg, err := Open(KindS3, cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	runSuite(t, tg, suiteOpts{bigSize: 17<<20 + 9}) // 超过 16 MiB，走分片上传
	for _, k := range fs.keys() {
		if !strings.HasPrefix(k, "team/backup/") {
			t.Errorf("对象 %q 不在 prefix 下", k)
		}
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
}

func TestCOSSuiteViaFake(t *testing.T) {
	// COS 用虚拟主机风格 + ListObjects V1；这里用路径风格的假服务器验证 V1 列表与读写逻辑。
	fs := newFakeObjStore(t, "backup-1250000000", true)
	u, _ := url.Parse(fs.srv.URL)
	o, err := cosOptionsFrom(COSConfig{Region: "ap-guangzhou", Bucket: "backup-1250000000", SecretID: "i", SecretKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	o.endpoint, o.secure, o.lookup = u.Host, false, minio.BucketLookupPath
	tg, err := newS3Target(o, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	runSuite(t, tg, suiteOpts{})
	for _, r := range fs.requests {
		if strings.Contains(r, "list-type=2") {
			t.Fatal("COS 应使用 ListObjects V1")
		}
	}
}
