package target

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeObjStore 是测试用的最小对象存储：PUT/GET/HEAD/DELETE、ListObjects V1/V2、分片上传。
// pathStyle 时路径是 /bucket/key（S3 路径风格），否则路径就是 /key（自定义域名 / 虚拟主机）。
// ossSecret 非空时按 OSS V4 规则独立地从收到的请求重算签名并校验。
type fakeObjStore struct {
	t         *testing.T
	bucket    string
	pathStyle bool
	ossAK     string
	ossSecret string
	ossRegion string

	mu       sync.Mutex
	objects  map[string][]byte
	mtimes   map[string]time.Time
	uploads  map[string]map[int][]byte
	nextID   int
	requests []string // 方法 + 查询，便于断言
	badSigs  int
	md5Seen  int
	srv      *httptest.Server
}

func newFakeObjStore(t *testing.T, bucket string, pathStyle bool) *fakeObjStore {
	f := &fakeObjStore{t: t, bucket: bucket, pathStyle: pathStyle,
		objects: map[string][]byte{}, mtimes: map[string]time.Time{}, uploads: map[string]map[int][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeObjStore) xmlErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message><RequestId>1</RequestId></Error>`, code, code)
}

// testURIEncode 是测试侧独立写的 RFC 3986 编码。
func testURIEncode(s string, slash bool) string {
	const hexd = "0123456789ABCDEF"
	var b []byte
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.~", c) >= 0 || (slash && c == '/') {
			b = append(b, c)
		} else {
			b = append(b, '%', hexd[c>>4], hexd[c&15])
		}
	}
	return string(b)
}

func (f *fakeObjStore) checkOSSSig(r *http.Request, key string) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "OSS4-HMAC-SHA256 ") {
		return false
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, "OSS4-HMAC-SHA256 "), ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			fields[kv[0]] = kv[1]
		}
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[0] != f.ossAK || cred[2] != f.ossRegion || cred[3] != "oss" || cred[4] != "aliyun_v4_request" {
		return false
	}
	ts := r.Header.Get("x-oss-date")
	if !strings.HasPrefix(ts, cred[1]) || r.Header.Get("x-oss-content-sha256") != "UNSIGNED-PAYLOAD" {
		return false
	}
	// 规范请求：从实际收到的请求重建。
	var hnames []string
	for k := range r.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-oss-") || lk == "content-type" || lk == "content-md5" {
			hnames = append(hnames, lk)
		}
	}
	sort.Strings(hnames)
	var hb strings.Builder
	for _, k := range hnames {
		hb.WriteString(k + ":" + strings.TrimSpace(r.Header.Get(k)) + "\n")
	}
	var qs []string
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			if v == "" {
				qs = append(qs, testURIEncode(k, false))
			} else {
				qs = append(qs, testURIEncode(k, false)+"="+testURIEncode(v, false))
			}
		}
	}
	sort.Strings(qs)
	cr := r.Method + "\n" + "/" + f.bucket + "/" + testURIEncode(key, true) + "\n" + strings.Join(qs, "&") + "\n" + hb.String() + "\n" + "\n" + "UNSIGNED-PAYLOAD"
	sum := sha256.Sum256([]byte(cr))
	sts := "OSS4-HMAC-SHA256\n" + ts + "\n" + strings.Join(cred[1:], "/") + "\n" + hex.EncodeToString(sum[:])
	want := hex.EncodeToString(hmacSHA256(ossSigningKey(f.ossSecret, cred[1], cred[2]), sts))
	return fields["Signature"] == want
}

func (f *fakeObjStore) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if f.pathStyle {
		if !strings.HasPrefix(p, "/"+f.bucket) {
			f.xmlErr(w, 404, "NoSuchBucket")
			return
		}
		p = strings.TrimPrefix(p, "/"+f.bucket)
	}
	key := strings.TrimPrefix(p, "/")
	q := r.URL.Query()
	f.requests = append(f.requests, r.Method+" "+r.URL.RawQuery)
	if f.ossSecret != "" && !f.checkOSSSig(r, key) {
		f.badSigs++
		f.xmlErr(w, 403, "SignatureDoesNotMatch")
		return
	}
	body, _ := io.ReadAll(r.Body)
	if cm := r.Header.Get("Content-Md5"); cm != "" {
		f.md5Seen++
		s := md5.Sum(body)
		if base64.StdEncoding.EncodeToString(s[:]) != cm {
			f.xmlErr(w, 400, "BadDigest")
			return
		}
	}
	if r.Method == http.MethodPut && r.ContentLength != int64(len(body)) {
		f.xmlErr(w, 400, "IncompleteBody")
		return
	}
	switch {
	case key == "" && r.Method == http.MethodGet:
		f.list(w, q)
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.nextID++
		id := "up" + strconv.Itoa(f.nextID)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, f.bucket, key, id)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		parts, ok := f.uploads[q.Get("uploadId")]
		if !ok {
			f.xmlErr(w, 404, "NoSuchUpload")
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		parts[n] = body
		s := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(s[:])+`"`)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		parts, ok := f.uploads[q.Get("uploadId")]
		if !ok {
			f.xmlErr(w, 404, "NoSuchUpload")
			return
		}
		var c struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		if err := xml.Unmarshal(body, &c); err != nil || len(c.Parts) == 0 {
			f.xmlErr(w, 400, "MalformedXML")
			return
		}
		var all []byte
		for i, cp := range c.Parts {
			d, ok := parts[cp.PartNumber]
			s := md5.Sum(d)
			if !ok || cp.PartNumber != i+1 || strings.Trim(cp.ETag, `"`) != hex.EncodeToString(s[:]) {
				f.xmlErr(w, 400, "InvalidPart")
				return
			}
			all = append(all, d...)
		}
		delete(f.uploads, q.Get("uploadId"))
		f.objects[key] = all
		f.mtimes[key] = time.Now()
		fmt.Fprintf(w, `<CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><ETag>"x-%d"</ETag></CompleteMultipartUploadResult>`, f.bucket, key, len(c.Parts))
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(204)
	case r.Method == http.MethodPut:
		f.objects[key] = body
		f.mtimes[key] = time.Now()
		s := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(s[:])+`"`)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		d, ok := f.objects[key]
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(404)
				return
			}
			f.xmlErr(w, 404, "NoSuchKey")
			return
		}
		s := md5.Sum(d)
		w.Header().Set("ETag", `"`+hex.EncodeToString(s[:])+`"`)
		w.Header().Set("Last-Modified", f.mtimes[key].UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(d)))
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.Method == http.MethodGet {
			w.Write(d)
		}
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(204)
	default:
		f.xmlErr(w, 405, "MethodNotAllowed")
	}
}

func (f *fakeObjStore) list(w http.ResponseWriter, q url.Values) {
	prefix := q.Get("prefix")
	maxKeys := 3 // 故意很小，覆盖分页
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	v2 := q.Get("list-type") == "2"
	after := q.Get("marker")
	if v2 {
		after = q.Get("continuation-token")
		if after == "" {
			after = q.Get("start-after")
		}
	}
	i := sort.SearchStrings(keys, after)
	if i < len(keys) && keys[i] == after {
		i++
	}
	keys = keys[i:]
	trunc := len(keys) > maxKeys
	if trunc {
		keys = keys[:maxKeys]
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>` + f.bucket + `</Name><Prefix>`)
	xml.EscapeText(&b, []byte(prefix))
	fmt.Fprintf(&b, `</Prefix><MaxKeys>%d</MaxKeys><KeyCount>%d</KeyCount><IsTruncated>%v</IsTruncated>`, maxKeys, len(keys), trunc)
	if trunc && v2 {
		b.WriteString("<NextContinuationToken>")
		xml.EscapeText(&b, []byte(keys[len(keys)-1]))
		b.WriteString("</NextContinuationToken>")
	}
	for _, k := range keys {
		b.WriteString("<Contents><Key>")
		xml.EscapeText(&b, []byte(k))
		fmt.Fprintf(&b, `</Key><LastModified>%s</LastModified><ETag>"e"</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`,
			f.mtimes[k].UTC().Format("2006-01-02T15:04:05.000Z"), len(f.objects[k]))
	}
	b.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	w.Write(b.Bytes())
}

func (f *fakeObjStore) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
