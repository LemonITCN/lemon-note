package target

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/s3utils"
)

// S3Config 是 S3 兼容存储（AWS S3、MinIO、Cloudflare R2、群晖 C2 等）的配置。
type S3Config struct {
	// Endpoint 是服务地址，例如 s3.amazonaws.com、minio.example.com:9000；
	// 也可以带 http:// 或 https://，此时以地址里的协议为准。
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"` // 留空时自动探测
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	UseSSL    *bool  `json:"use_ssl"`    // 缺省为 true
	PathStyle bool   `json:"path_style"` // true 用路径风格（MinIO 常用），false 用虚拟主机风格
}

// COSConfig 是腾讯云 COS 的配置，走 COS 的 S3 兼容接口。
type COSConfig struct {
	Region    string `json:"region"` // 例如 ap-guangzhou
	Bucket    string `json:"bucket"` // 名称-APPID，例如 backup-1250000000
	Prefix    string `json:"prefix"`
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

// s3Options 是由配置推出的 minio 客户端参数，单独拿出来便于测试端点构造。
type s3Options struct {
	label    string // 给用户看的名字：S3 / 腾讯云 COS
	endpoint string // host[:port]
	secure   bool
	region   string
	bucket   string
	prefix   string
	lookup   minio.BucketLookupType
	listV1   bool
	access   string
	secret   string
}

var (
	regionRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	cosBucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-[0-9]+$`)
)

func s3OptionsFrom(c S3Config) (s3Options, error) {
	o := s3Options{label: "S3", region: strings.TrimSpace(c.Region), bucket: strings.TrimSpace(c.Bucket),
		access: strings.TrimSpace(c.AccessKey), secret: c.SecretKey}
	ep := strings.TrimSpace(c.Endpoint)
	if ep == "" {
		return o, errors.New("请填写 S3 服务地址（endpoint）")
	}
	o.secure = boolDefault(c.UseSSL, true)
	if strings.Contains(ep, "://") {
		u, err := url.Parse(ep)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return o, fmt.Errorf("S3 服务地址「%s」不正确", ep)
		}
		if u.Path != "" && u.Path != "/" {
			return o, fmt.Errorf("S3 服务地址「%s」不能带路径，子目录请填在 prefix 里", ep)
		}
		o.secure = u.Scheme == "https"
		ep = u.Host
	}
	if strings.ContainsAny(ep, "/ ") {
		return o, fmt.Errorf("S3 服务地址「%s」不正确", ep)
	}
	o.endpoint = ep
	if o.bucket == "" {
		return o, errors.New("请填写存储桶名称")
	}
	if err := s3utils.CheckValidBucketName(o.bucket); err != nil {
		return o, fmt.Errorf("存储桶名称「%s」不正确", o.bucket)
	}
	if o.access == "" || o.secret == "" {
		return o, errors.New("请填写 Access Key 和 Secret Key")
	}
	p, err := cleanRootPrefix(c.Prefix)
	if err != nil {
		return o, err
	}
	o.prefix = p
	if c.PathStyle {
		o.lookup = minio.BucketLookupPath
	} else {
		o.lookup = minio.BucketLookupDNS
	}
	return o, nil
}

func cosOptionsFrom(c COSConfig) (s3Options, error) {
	o := s3Options{label: "腾讯云 COS", secure: true, lookup: minio.BucketLookupDNS, listV1: true,
		region: strings.TrimSpace(c.Region), bucket: strings.TrimSpace(c.Bucket),
		access: strings.TrimSpace(c.SecretID), secret: c.SecretKey}
	if !regionRe.MatchString(o.region) {
		return o, fmt.Errorf("腾讯云 COS 地域「%s」不正确，应类似 ap-guangzhou", o.region)
	}
	if !cosBucketRe.MatchString(o.bucket) {
		return o, fmt.Errorf("腾讯云 COS 存储桶「%s」不正确，应为「名称-APPID」形式，例如 backup-1250000000", o.bucket)
	}
	if o.access == "" || o.secret == "" {
		return o, errors.New("请填写 SecretId 和 SecretKey")
	}
	p, err := cleanRootPrefix(c.Prefix)
	if err != nil {
		return o, err
	}
	o.prefix = p
	o.endpoint = "cos." + o.region + ".myqcloud.com"
	return o, nil
}

type s3Target struct {
	o   s3Options
	cli *minio.Client
}

func openS3(c S3Config, d Deps) (Target, error) {
	o, err := s3OptionsFrom(c)
	if err != nil {
		return nil, err
	}
	return newS3Target(o, d)
}

func openCOS(c COSConfig, d Deps) (Target, error) {
	o, err := cosOptionsFrom(c)
	if err != nil {
		return nil, err
	}
	return newS3Target(o, d)
}

func newS3Target(o s3Options, d Deps) (*s3Target, error) {
	cli, err := minio.New(o.endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(o.access, o.secret, ""),
		Secure:       o.secure,
		Region:       o.region,
		BucketLookup: o.lookup,
		Transport:    httpTransport(d),
		// 不开尾随校验头：COS、OSS 等兼容实现不一定支持 aws-chunked 尾随校验。
		TrailingHeaders: false,
	})
	if err != nil {
		return nil, fmt.Errorf("%s 配置不正确：%v", o.label, err)
	}
	return &s3Target{o: o, cli: cli}, nil
}

func (t *s3Target) key(p string) string { return joinKey(t.o.prefix, p) }

func (t *s3Target) wrap(ctx context.Context, op, p string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		switch er.Code {
		case "NoSuchKey", "NotFound":
			return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
		case "NoSuchBucket":
			return fmt.Errorf("%s：存储桶「%s」不存在，请检查存储桶名称和地域", t.o.label, t.o.bucket)
		case "InvalidAccessKeyId":
			return fmt.Errorf("%s：Access Key 不存在或已停用", t.o.label)
		case "SignatureDoesNotMatch":
			return fmt.Errorf("%s：Secret Key 不正确", t.o.label)
		case "AccessDenied":
			return fmt.Errorf("%s「%s」失败：没有权限，请检查密钥对该存储桶的读写授权", op, p)
		case "RequestTimeTooSkewed":
			return fmt.Errorf("%s：本机时间与服务器相差太大，请校准系统时间", t.o.label)
		case "AuthorizationHeaderMalformed":
			return fmt.Errorf("%s：地域（region）填写不正确：%s", t.o.label, er.Message)
		}
		if er.StatusCode == http.StatusNotFound && er.Code == "" {
			return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
		}
		msg := er.Message
		if msg == "" {
			msg = er.Code
		}
		if msg == "" {
			msg = http.StatusText(er.StatusCode)
		}
		return fmt.Errorf("%s「%s」失败：%s", op, p, msg)
	}
	if errors.Is(err, ErrSizeMismatch) {
		return fmt.Errorf("%s「%s」失败：%w", op, p, err)
	}
	return fmt.Errorf("连接 %s 失败：%s", t.o.label, netErrText(err))
}

func (t *s3Target) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	if size < 0 {
		return errors.New("写入文件时必须给出文件大小")
	}
	if size == 0 {
		if err := emptyCheck(r); err != nil {
			return fmt.Errorf("上传「%s」失败：%w", p, err)
		}
	}
	// 对象存储的 PUT 与分片合并都是原子的：上传中断不会出现半个对象。
	// 用 Content-MD5 让服务端校验每个请求体；签名用 UNSIGNED-PAYLOAD，不用 aws-chunked，兼容性最好。
	info, err := t.cli.PutObject(ctx, t.o.bucket, t.key(p), newSizedReader(r, size), size, minio.PutObjectOptions{
		ContentType:          "application/octet-stream",
		SendContentMd5:       true,
		DisableContentSha256: true,
	})
	if err != nil {
		return t.wrap(ctx, "上传", p, err)
	}
	if info.Size != size {
		return fmt.Errorf("上传「%s」失败：服务端记录的大小 %d 与实际 %d 不一致", p, info.Size, size)
	}
	return nil
}

func (t *s3Target) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	obj, err := t.cli.GetObject(ctx, t.o.bucket, t.key(p), minio.GetObjectOptions{})
	if err != nil {
		return nil, t.wrap(ctx, "读取", p, err)
	}
	st, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, t.wrap(ctx, "读取", p, err)
	}
	return newCheckedReadCloser(obj, st.Size), nil
}

func (t *s3Target) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	st, err := t.cli.StatObject(ctx, t.o.bucket, t.key(p), minio.StatObjectOptions{})
	if err != nil {
		return Entry{}, t.wrap(ctx, "查询", p, err)
	}
	return Entry{Path: p, Size: st.Size, ModTime: st.LastModified}, nil
}

func (t *s3Target) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	err = t.cli.RemoveObject(ctx, t.o.bucket, t.key(p), minio.RemoveObjectOptions{})
	if err != nil {
		e := t.wrap(ctx, "删除", p, err)
		if errors.Is(e, ErrNotExist) {
			return nil
		}
		return e
	}
	return nil
}

func (t *s3Target) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	full := joinKey(t.o.prefix, prefix)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fetchOwner := false
	var out []Entry
	for obj := range t.cli.ListObjects(ctx, t.o.bucket, minio.ListObjectsOptions{
		Prefix:     full,
		Recursive:  true,
		UseV1:      t.o.listV1,
		FetchOwner: &fetchOwner,
	}) {
		if obj.Err != nil {
			return nil, t.wrap(ctx, "列出", prefix, obj.Err)
		}
		rel, ok := relKey(t.o.prefix, obj.Key)
		if !ok || strings.HasSuffix(rel, "/") || isTempName(baseName(rel)) {
			continue // 目录占位对象、前缀之外的对象
		}
		if _, err := cleanPath(rel); err != nil {
			continue // 不是本程序写的、路径不合法的对象
		}
		out = append(out, Entry{Path: rel, Size: obj.Size, ModTime: obj.LastModified})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// relKey 把完整对象键转成相对配置 prefix 的路径。
func relKey(prefix, key string) (string, bool) {
	if prefix == "" {
		return key, key != ""
	}
	if !strings.HasPrefix(key, prefix+"/") {
		return "", false
	}
	rel := key[len(prefix)+1:]
	return rel, rel != ""
}

func (t *s3Target) Close() error { return nil }
