package target

// 百度网盘开放平台目标。接口依据 2026-11 查证的官方文档（pan.baidu.com/union/doc/ 下的
// 「设备码模式授权」「预上传 / 分片上传 / 获取上传域名 / 创建文件」「查询文件信息」「下载」
// 「递归获取文件列表 / 获取文件列表」「管理文件」「错误码」各页，以及 openauth.baidu.com 的 OAuth 错误码）：
//   - 授权：设备码模式；access_token 30 天，refresh_token 10 年且只能用一次，刷新成功或失败旧值都作废，
//     所以新令牌先通过 Deps.SaveConfig 写盘；同一目标的刷新串行。
//   - 上传：precreate（rtype=3 覆盖）→ locateupload 取上传域名 → superfile2 逐片上传（4 MB 一片，
//     返回的 md5 必须与本地一致）→ create。文件在 create 之前不可见，不会留下半个文件。
//     block_list 需要预先给出每片 md5，所以先把数据落到本机临时文件里。
//   - 下载：filemetas 取 dlink，dlink 后拼 access_token，必须带 User-Agent: pan.baidu.com。
//   - 列目录：listall 递归；单个文件查询用 list 列父目录后按文件名匹配（官方文档化、稳定的方式）。
//   - 删除：filemanager opera=delete（进网盘回收站）。
// 已知限制：开放平台不支持上传空文件；普通用户单片固定 4 MB、最多 1024 片，即单文件最大 4 GB；
// 未审核的应用每小时只能调用 10 次接口、最多 10 个用户，正式使用需要在开放平台提交审核。

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 百度接口的基础地址，测试时可以替换成 httptest 服务器。
var (
	BaiduOAuthBase = "https://openapi.baidu.com"
	BaiduPanBase   = "https://pan.baidu.com"
	BaiduPCSBase   = "https://d.pcs.baidu.com"
)

// 限流（errno 31034）时的重试间隔，测试时可以缩短。
var baiduRetryDelays = []time.Duration{3 * time.Second, 10 * time.Second, 30 * time.Second}

const (
	baiduBlockSize   = 4 << 20
	baiduMaxBlocks   = 1024
	baiduMaxFileSize = int64(baiduBlockSize) * baiduMaxBlocks
	baiduUserAgent   = "pan.baidu.com"
	baiduUploadAppID = "250528" // locateupload 文档规定的固定值
	baiduListLimit   = 1000
	// 令牌剩余有效期少于这个时长时主动刷新。
	baiduRefreshBefore = 24 * time.Hour
)

// BaiduConfig 是百度网盘的配置。
type BaiduConfig struct {
	AppKey       string `json:"app_key"`
	SecretKey    string `json:"secret_key"`
	AppName      string `json:"app_name"` // 开放平台应用的产品名称，文件放在 /apps/<app_name>/ 下
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // access_token 过期时间，Unix 秒
	Prefix       string `json:"prefix"`
}

// BaiduDevice 是设备码授权第一步的结果：界面展示 QRCodeURL 二维码或让用户打开 VerificationURL 输入 UserCode。
type BaiduDevice struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	QRCodeURL       string `json:"qrcode_url"`
	ExpiresIn       int    `json:"expires_in"` // 秒
	Interval        int    `json:"interval"`   // 轮询间隔，秒（至少 5）
}

// BaiduToken 是换到的令牌。ExpiresAt 是按本机时间算出的过期时间（Unix 秒）。
type BaiduToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	ExpiresAt    int64
	Scope        string
}

var (
	// ErrBaiduAuthPending 表示用户还没完成授权，按 Interval 继续轮询即可。
	ErrBaiduAuthPending = errors.New("等待用户在百度网盘完成授权")
	// ErrBaiduSlowDown 表示轮询太快，应把间隔加 5 秒后继续；它也满足 errors.Is(err, ErrBaiduAuthPending)。
	ErrBaiduSlowDown = fmt.Errorf("轮询过于频繁，请放慢：%w", ErrBaiduAuthPending)
)

// bdInt 兼容百度接口里有时是数字、有时是字符串的整数字段。
type bdInt int64

func (b *bdInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		*b = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return err
		}
		n = int64(f)
	}
	*b = bdInt(n)
	return nil
}

// ---------------- 设备码授权 ----------------

type baiduOAuthResp struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        bdInt  `json:"expires_in"`
	Scope            string `json:"scope"`
	DeviceCode       string `json:"device_code"`
	UserCode         string `json:"user_code"`
	VerificationURL  string `json:"verification_url"`
	QRCodeURL        string `json:"qrcode_url"`
	Interval         bdInt  `json:"interval"`
}

func baiduOAuthGet(ctx context.Context, client *http.Client, endpoint string, q url.Values) (*baiduOAuthResp, error) {
	if client == nil {
		client = defaultHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("百度网盘授权服务地址不正确") // 不带原始错误：URL 里有密钥
	}
	req.Header.Set("User-Agent", baiduUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接百度网盘授权服务失败：%s", netErrText(ctxErr(ctx, err)))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("连接百度网盘授权服务失败：%s", netErrText(ctxErr(ctx, err)))
	}
	var r baiduOAuthResp
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("百度网盘授权服务返回了无法识别的内容（HTTP %d）", resp.StatusCode)
	}
	return &r, nil
}

func baiduOAuthErr(r *baiduOAuthResp) error {
	switch r.Error {
	case "authorization_pending":
		return ErrBaiduAuthPending
	case "slow_down":
		return ErrBaiduSlowDown
	case "authorization_declined":
		return errors.New("用户拒绝了百度网盘授权")
	case "expired_token", "invalid_grant":
		return errors.New("百度网盘授权码已过期或已使用，请重新发起授权")
	case "invalid_client", "unauthorized_client":
		return errors.New("百度网盘 AppKey 或 SecretKey 不正确")
	}
	if r.ErrorDescription != "" {
		return fmt.Errorf("百度网盘授权失败：%s（%s）", r.ErrorDescription, r.Error)
	}
	return fmt.Errorf("百度网盘授权失败：%s", r.Error)
}

// BaiduDeviceCode 发起设备码授权，返回给用户展示的授权码与二维码地址。client 可为 nil。
func BaiduDeviceCode(ctx context.Context, client *http.Client, appKey string) (*BaiduDevice, error) {
	if strings.TrimSpace(appKey) == "" {
		return nil, errors.New("请填写百度网盘 AppKey")
	}
	q := url.Values{"response_type": {"device_code"}, "client_id": {strings.TrimSpace(appKey)}, "scope": {"basic,netdisk"}}
	r, err := baiduOAuthGet(ctx, client, BaiduOAuthBase+"/oauth/2.0/device/code", q)
	if err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, baiduOAuthErr(r)
	}
	if r.DeviceCode == "" || r.UserCode == "" {
		return nil, errors.New("百度网盘没有返回设备码")
	}
	d := &BaiduDevice{DeviceCode: r.DeviceCode, UserCode: r.UserCode, VerificationURL: r.VerificationURL,
		QRCodeURL: r.QRCodeURL, ExpiresIn: int(r.ExpiresIn), Interval: int(r.Interval)}
	if d.Interval < 5 {
		d.Interval = 5
	}
	return d, nil
}

// BaiduPollToken 用设备码换令牌。用户还没授权时返回 ErrBaiduAuthPending（可重试），
// 轮询过快时返回 ErrBaiduSlowDown（也满足 errors.Is(err, ErrBaiduAuthPending)）。client 可为 nil。
func BaiduPollToken(ctx context.Context, client *http.Client, appKey, secretKey, deviceCode string) (*BaiduToken, error) {
	q := url.Values{"grant_type": {"device_token"}, "code": {deviceCode},
		"client_id": {strings.TrimSpace(appKey)}, "client_secret": {strings.TrimSpace(secretKey)}}
	r, err := baiduOAuthGet(ctx, client, BaiduOAuthBase+"/oauth/2.0/token", q)
	if err != nil {
		return nil, err
	}
	return baiduTokenFrom(r, time.Now())
}

func baiduTokenFrom(r *baiduOAuthResp, now time.Time) (*BaiduToken, error) {
	if r.Error != "" {
		return nil, baiduOAuthErr(r)
	}
	if r.AccessToken == "" || r.RefreshToken == "" {
		return nil, errors.New("百度网盘没有返回完整的令牌")
	}
	if r.Scope != "" && !strings.Contains(r.Scope, "netdisk") {
		return nil, errors.New("百度网盘授权范围里没有网盘权限（netdisk），请确认开放平台应用已开通网盘能力")
	}
	exp := int64(r.ExpiresIn)
	if exp <= 0 {
		exp = 30 * 24 * 3600
	}
	return &BaiduToken{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, ExpiresIn: exp,
		ExpiresAt: now.Unix() + exp, Scope: r.Scope}, nil
}

// BaiduApplyToken 把令牌写进配置 JSON，保留其余字段原样，返回新配置。
func BaiduApplyToken(configJSON []byte, tok *BaiduToken) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(configJSON)) > 0 {
		if err := json.Unmarshal(configJSON, &m); err != nil {
			return nil, fmt.Errorf("百度网盘配置格式不正确：%v", err)
		}
	}
	set := func(k string, v any) {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	set("access_token", tok.AccessToken)
	set("refresh_token", tok.RefreshToken)
	set("expires_at", tok.ExpiresAt)
	return json.Marshal(m)
}

// ---------------- 目标 ----------------

type baiduMeta struct {
	fsID  uint64
	size  int64
	mtime time.Time
}

type baiduTarget struct {
	appKey, secretKey string
	root              string // /apps/<app_name>[/<prefix>]
	client            *http.Client
	save              func([]byte) error
	oauthBase         string
	panBase           string
	pcsBase           string
	now               func() time.Time

	mu              sync.Mutex // 保护下面的令牌字段，刷新串行
	raw             []byte     // 当前配置 JSON（刷新后更新）
	access          string
	refresh         string
	expires         int64
	refreshFailedAt time.Time

	cache sync.Map // 完整路径 -> baiduMeta
}

func openBaidu(c BaiduConfig, raw []byte, d Deps) (Target, error) {
	if strings.TrimSpace(c.AppKey) == "" || strings.TrimSpace(c.SecretKey) == "" {
		return nil, errors.New("请填写百度网盘开放平台应用的 AppKey 和 SecretKey")
	}
	app := strings.TrimSpace(c.AppName)
	if app == "" {
		return nil, errors.New("请填写百度网盘开放平台应用的名称（文件会放在「我的应用数据/应用名称」下）")
	}
	if err := checkChars(app); err != nil || strings.ContainsAny(app, "/") || app == "." || app == ".." {
		return nil, fmt.Errorf("百度网盘应用名称「%s」不正确", app)
	}
	if c.AccessToken == "" && c.RefreshToken == "" {
		return nil, errors.New("百度网盘尚未授权，请先完成授权")
	}
	prefix, err := cleanRootPrefix(c.Prefix)
	if err != nil {
		return nil, err
	}
	root := "/apps/" + app
	if prefix != "" {
		root += "/" + prefix
	}
	return &baiduTarget{
		appKey: strings.TrimSpace(c.AppKey), secretKey: strings.TrimSpace(c.SecretKey),
		root: root, client: httpClient(d), save: d.SaveConfig,
		oauthBase: BaiduOAuthBase, panBase: BaiduPanBase, pcsBase: BaiduPCSBase, now: time.Now,
		raw: append([]byte(nil), raw...), access: c.AccessToken, refresh: c.RefreshToken, expires: c.ExpiresAt,
	}, nil
}

func (t *baiduTarget) full(rel string) string {
	if rel == "" {
		return t.root
	}
	return t.root + "/" + rel
}

// token 返回可用的 access_token，快过期时先刷新。
func (t *baiduTarget) token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if t.access != "" && (t.expires == 0 || now.Unix() < t.expires-int64(baiduRefreshBefore/time.Second)) {
		return t.access, nil
	}
	stillValid := t.access != "" && now.Unix() < t.expires
	if t.refresh == "" || t.save == nil {
		if stillValid {
			return t.access, nil
		}
		return "", errors.New("百度网盘授权已过期，请重新授权")
	}
	// 刚刷新失败过、旧令牌还能用时，不要每个请求都去刷新。
	if stillValid && !t.refreshFailedAt.IsZero() && now.Sub(t.refreshFailedAt) < 10*time.Minute {
		return t.access, nil
	}
	if err := t.refreshLocked(ctx); err != nil {
		var se *baiduSaveError
		if stillValid && !errors.As(err, &se) {
			t.refreshFailedAt = now
			return t.access, nil // 旧令牌还能用，先完成这次操作
		}
		return "", err
	}
	return t.access, nil
}

// forceRefresh 在接口报令牌失效时刷新；used 是失败请求用的令牌，别的协程已经刷新过就直接用新的。
func (t *baiduTarget) forceRefresh(ctx context.Context, used string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.access != used && t.access != "" {
		return t.access, nil
	}
	if t.refresh == "" || t.save == nil {
		return "", errors.New("百度网盘授权已失效，请重新授权")
	}
	if err := t.refreshLocked(ctx); err != nil {
		return "", err
	}
	return t.access, nil
}

// 已用掉的 refresh_token → 换来的新令牌。同一进程里可能同时打开同一配置的多个目标实例
// （例如计划备份进行中又点了「测试连接」），refresh_token 只能用一次，后到的实例直接沿用新令牌。
var (
	baiduRotateMu sync.Mutex
	baiduRotated  = map[string]*BaiduToken{}
)

// baiduSaveError 表示令牌已经刷新（旧的已作废），但回写配置失败。
type baiduSaveError struct{ err error }

func (e *baiduSaveError) Error() string {
	return fmt.Sprintf("百度网盘令牌已刷新，但保存新令牌失败（重启后需要重新授权）：%v", e.err)
}

func (t *baiduTarget) refreshLocked(ctx context.Context) error {
	baiduRotateMu.Lock()
	key := t.appKey + "\x00" + t.refresh
	tok, ok := baiduRotated[key]
	if !ok {
		q := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.refresh},
			"client_id": {t.appKey}, "client_secret": {t.secretKey}}
		r, err := baiduOAuthGet(ctx, t.client, t.oauthBase+"/oauth/2.0/token", q)
		if err != nil {
			baiduRotateMu.Unlock()
			return err
		}
		tok, err = baiduTokenFrom(r, t.now())
		if err != nil {
			baiduRotateMu.Unlock()
			if r.Error != "" {
				return fmt.Errorf("刷新百度网盘令牌失败，请重新授权：%v", err)
			}
			return err
		}
		baiduRotated[key] = tok
	}
	baiduRotateMu.Unlock()
	newRaw, err := BaiduApplyToken(t.raw, tok)
	if err != nil {
		return err
	}
	// 旧 refresh_token 已经作废：无论能否保存，内存里都换成新令牌，本进程后续操作还能继续。
	t.access, t.refresh, t.expires, t.raw = tok.AccessToken, tok.RefreshToken, tok.ExpiresAt, newRaw
	t.refreshFailedAt = time.Time{}
	if err := t.save(newRaw); err != nil {
		return &baiduSaveError{err}
	}
	return nil
}

// baiduStatus 是百度接口通用的错误字段。网盘接口用 errno / errmsg，PCS 接口用 error_code / error_msg。
type baiduStatus struct {
	Errno     bdInt  `json:"errno"`
	Errmsg    string `json:"errmsg"`
	ErrorCode bdInt  `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

func (s baiduStatus) code() int64 {
	if s.Errno != 0 {
		return int64(s.Errno)
	}
	return int64(s.ErrorCode)
}

// baiduAPIError 是百度网盘接口返回的业务错误。
type baiduAPIError struct {
	Code int64
	Msg  string
}

func (e *baiduAPIError) Error() string { return baiduErrnoText(e.Code, e.Msg) }

var baiduErrnoTexts = map[int64]string{
	-1:    "百度网盘权益已过期",
	-3:    "文件不存在",
	-6:    "百度网盘身份验证失败，请重新授权",
	-7:    "文件名不合法或没有访问权限",
	-8:    "文件已存在",
	-9:    "文件不存在",
	-10:   "百度网盘空间不足",
	2:     "参数错误",
	6:     "不允许接入用户数据",
	10:    "创建文件失败",
	111:   "有其他异步任务正在执行，请稍后重试",
	9013:  "今日上传次数已达上限",
	20011: "应用审核中，仅限前 10 个用户使用",
	20012: "接口调用次数超限（未审核的应用每小时只能调用 10 次），请稍后重试或在开放平台提交应用审核",
	20013: "应用没有接口权限",
	20016: "百度网盘授权已过期，请重新授权",
	20017: "百度网盘授权已失效，请重新授权",
	20020: "路径不在应用允许访问的范围内",
	31023: "参数错误",
	31024: "没有访问权限",
	31034: "接口调用过于频繁，请稍后重试",
	31045: "百度网盘身份验证失败，请重新授权",
	31061: "文件已存在",
	31062: "文件名不合法",
	31064: "上传路径错误",
	31066: "文件不存在",
	31190: "文件分片校验失败",
	31299: "分片大小不正确",
	31326: "下载被防盗链拦截",
	31355: "上传任务已失效",
	31360: "下载链接已过期",
	31362: "下载链接签名错误",
	31363: "分片缺失",
	31364: "分片超过大小限制",
	31365: "文件超过大小限制（普通用户单文件最大 4 GB）",
}

func baiduErrnoText(code int64, msg string) string {
	if s, ok := baiduErrnoTexts[code]; ok {
		return fmt.Sprintf("%s（错误码 %d）", s, code)
	}
	if msg != "" {
		return fmt.Sprintf("百度网盘返回错误 %d：%s", code, msg)
	}
	return fmt.Sprintf("百度网盘返回错误 %d", code)
}

func baiduNotFound(code int64) bool { return code == -9 || code == -3 || code == 31066 }

func baiduTokenInvalid(code int64) bool { return code == -6 || code == 20016 || code == 31045 }

// trusted 判断地址能不能带 access_token 访问：只允许百度自己的域名（或测试时配置的基础地址）。
func (t *baiduTarget) trusted(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if u.Scheme == "https" && (strings.HasSuffix(h, ".baidu.com") || strings.HasSuffix(h, ".baidupcs.com")) {
		return true
	}
	for _, b := range []string{t.panBase, t.pcsBase, t.oauthBase} {
		if bu, err := url.Parse(b); err == nil && bu.Scheme == u.Scheme && strings.EqualFold(bu.Host, u.Host) {
			return true
		}
	}
	return false
}

// baiduBody 描述请求体；每次重试都重新生成，保证可以重放。
type baiduBody struct {
	form      url.Values
	multipart func() (io.Reader, string, error) // 返回请求体和 Content-Type
}

// call 调用一个网盘接口：自动带令牌，令牌失效时刷新一次，限流时退避重试，最后把 JSON 解到 out。
func (t *baiduTarget) call(ctx context.Context, method, endpoint string, q url.Values, body *baiduBody, out any) error {
	tok, err := t.token(ctx)
	if err != nil {
		return err
	}
	refreshed := false
	retries := 0
	for {
		code, msg, err := t.callOnce(ctx, method, endpoint, q, body, tok, out)
		if err != nil {
			return err
		}
		if code == 0 {
			return nil
		}
		if baiduTokenInvalid(code) && !refreshed {
			refreshed = true
			if tok, err = t.forceRefresh(ctx, tok); err != nil {
				return err
			}
			continue
		}
		if code == 31034 && retries < len(baiduRetryDelays) {
			if err := sleepCtx(ctx, baiduRetryDelays[retries]); err != nil {
				return err
			}
			retries++
			continue
		}
		return &baiduAPIError{Code: code, Msg: msg}
	}
}

func (t *baiduTarget) callOnce(ctx context.Context, method, endpoint string, q url.Values, body *baiduBody, tok string, out any) (int64, string, error) {
	qq := url.Values{}
	for k, v := range q {
		qq[k] = v
	}
	qq.Set("access_token", tok)
	var rd io.Reader
	ctype := ""
	if body != nil {
		switch {
		case body.multipart != nil:
			r, ct, err := body.multipart()
			if err != nil {
				return 0, "", err
			}
			rd, ctype = r, ct
		case body.form != nil:
			rd, ctype = strings.NewReader(body.form.Encode()), "application/x-www-form-urlencoded"
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+"?"+qq.Encode(), rd)
	if err != nil {
		return 0, "", errors.New("百度网盘接口地址不正确") // 不带原始错误：URL 里有令牌
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("User-Agent", baiduUserAgent)
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("连接百度网盘失败：%s", netErrText(ctxErr(ctx, err)))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, "", fmt.Errorf("连接百度网盘失败：%s", netErrText(ctxErr(ctx, err)))
	}
	var st baiduStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return 0, "", fmt.Errorf("百度网盘返回了无法识别的内容（HTTP %d）", resp.StatusCode)
	}
	if c := st.code(); c != 0 {
		msg := st.Errmsg
		if msg == "" {
			msg = st.ErrorMsg
		}
		return c, msg, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("百度网盘返回 HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return 0, "", errors.New("百度网盘返回了无法识别的内容")
		}
	}
	return 0, "", nil
}

func (t *baiduTarget) wrap(op, p string, err error) error {
	var ae *baiduAPIError
	if errors.As(err, &ae) {
		if baiduNotFound(ae.Code) {
			return fmt.Errorf("%s「%s」失败：%w", op, p, ErrNotExist)
		}
		return fmt.Errorf("%s「%s」失败：%s", op, p, ae.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%s「%s」失败：%w", op, p, err)
}

// ---------------- 上传 ----------------

type baiduPrecreate struct {
	ReturnType bdInt   `json:"return_type"`
	UploadID   string  `json:"uploadid"`
	BlockList  []bdInt `json:"block_list"`
}

type baiduLocate struct {
	Servers    []struct{ Server string } `json:"servers"`
	BakServers []struct{ Server string } `json:"bak_servers"`
}

type baiduCreate struct {
	FsID  bdInt  `json:"fs_id"`
	Path  string `json:"path"`
	Size  bdInt  `json:"size"`
	Mtime bdInt  `json:"mtime"`
}

type baiduPart struct {
	MD5 string `json:"md5"`
}

func (t *baiduTarget) Put(ctx context.Context, p string, r io.Reader, size int64) error {
	p, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	if size < 0 {
		return errors.New("写入文件时必须给出文件大小")
	}
	if size == 0 {
		return fmt.Errorf("上传「%s」失败：百度网盘开放平台不支持上传空文件", p)
	}
	if size > baiduMaxFileSize {
		return fmt.Errorf("上传「%s」失败：文件 %d 字节，超过百度网盘普通用户单文件 4 GB 的上限", p, size)
	}
	full := t.full(p)

	// 1. 落到本机临时文件，同时算出每片 md5。
	spool, err := os.CreateTemp("", "lemon-note-baidu-*.tmp")
	if err != nil {
		return fmt.Errorf("上传「%s」失败：无法创建本机临时文件：%v", p, err)
	}
	defer func() {
		spool.Close()
		os.Remove(spool.Name())
	}()
	sr := newSizedReader(ctxReader{ctx, r}, size)
	buf := make([]byte, baiduBlockSize)
	var blocks []string
	for remain := size; remain > 0; {
		n := int64(baiduBlockSize)
		if remain < n {
			n = remain
		}
		if _, err := io.ReadFull(sr, buf[:n]); err != nil {
			return ctxErr(ctx, fmt.Errorf("上传「%s」失败：%w", p, err))
		}
		sum := md5.Sum(buf[:n])
		blocks = append(blocks, hex.EncodeToString(sum[:]))
		if _, err := spool.Write(buf[:n]); err != nil {
			return fmt.Errorf("上传「%s」失败：写本机临时文件出错：%v", p, err)
		}
		remain -= n
	}
	if _, err := sr.Read(nil); err != nil && err != io.EOF {
		return fmt.Errorf("上传「%s」失败：%w", p, err)
	}
	blockJSON, _ := json.Marshal(blocks)

	// 2. 预上传。
	form := url.Values{"path": {full}, "size": {strconv.FormatInt(size, 10)}, "isdir": {"0"},
		"block_list": {string(blockJSON)}, "autoinit": {"1"}, "rtype": {"3"}}
	var pre baiduPrecreate
	if err := t.call(ctx, http.MethodPost, t.panBase+"/rest/2.0/xpan/file", url.Values{"method": {"precreate"}}, &baiduBody{form: form}, &pre); err != nil {
		return t.wrap("上传", p, err)
	}
	t.cache.Delete(full)
	if pre.ReturnType == 2 {
		// 秒传：云端已有相同内容，文件已经建好。核对一下大小。
		e, err := t.statFull(ctx, full)
		if err != nil {
			return t.wrap("上传", p, err)
		}
		if e.size != size {
			return fmt.Errorf("上传「%s」失败：百度网盘秒传后文件大小 %d 与实际 %d 不一致", p, e.size, size)
		}
		return nil
	}
	if pre.UploadID == "" {
		return fmt.Errorf("上传「%s」失败：百度网盘没有返回上传任务编号", p)
	}
	need := make([]int, 0, len(blocks))
	for _, b := range pre.BlockList {
		if int(b) < 0 || int(b) >= len(blocks) {
			return fmt.Errorf("上传「%s」失败：百度网盘要求上传不存在的分片 %d", p, b)
		}
		need = append(need, int(b))
	}
	if len(need) == 0 {
		for i := range blocks { // 没说要哪些片就全部上传，多传不会出错
			need = append(need, i)
		}
	}

	// 3. 取上传域名，逐片上传。
	host := t.uploadHost(ctx, full, pre.UploadID)
	for _, idx := range need {
		off := int64(idx) * baiduBlockSize
		n := int64(baiduBlockSize)
		if size-off < n {
			n = size - off
		}
		if _, err := spool.ReadAt(buf[:n], off); err != nil {
			return fmt.Errorf("上传「%s」失败：读本机临时文件出错：%v", p, err)
		}
		if err := t.uploadPart(ctx, host, full, pre.UploadID, idx, buf[:n], blocks[idx]); err != nil {
			return t.wrap("上传", p, err)
		}
	}

	// 4. 创建文件（覆盖同名文件）。
	form = url.Values{"path": {full}, "size": {strconv.FormatInt(size, 10)}, "isdir": {"0"},
		"block_list": {string(blockJSON)}, "uploadid": {pre.UploadID}, "rtype": {"3"}}
	var cr baiduCreate
	if err := t.call(ctx, http.MethodPost, t.panBase+"/rest/2.0/xpan/file", url.Values{"method": {"create"}}, &baiduBody{form: form}, &cr); err != nil {
		return t.wrap("上传", p, err)
	}
	if cr.Path != "" && cr.Path != full {
		// 理论上 rtype=3 不会改名；万一被改名，删掉改名后的文件，报错。
		_ = t.deleteFull(ctx, cr.Path)
		return fmt.Errorf("上传「%s」失败：百度网盘把文件保存成了「%s」", p, cr.Path)
	}
	if cr.Size != 0 && int64(cr.Size) != size {
		return fmt.Errorf("上传「%s」失败：百度网盘记录的大小 %d 与实际 %d 不一致", p, cr.Size, size)
	}
	if cr.FsID != 0 {
		t.cache.Store(full, baiduMeta{fsID: uint64(cr.FsID), size: size, mtime: time.Unix(int64(cr.Mtime), 0)})
	}
	return nil
}

// uploadHost 用 locateupload 取上传域名，失败时退回默认的 PCS 地址。
func (t *baiduTarget) uploadHost(ctx context.Context, full, uploadID string) string {
	var loc baiduLocate
	q := url.Values{"method": {"locateupload"}, "appid": {baiduUploadAppID}, "path": {full},
		"uploadid": {uploadID}, "upload_version": {"2.0"}}
	if err := t.call(ctx, http.MethodGet, t.pcsBase+"/rest/2.0/pcs/file", q, nil, &loc); err == nil {
		for _, list := range [][]struct{ Server string }{loc.Servers, loc.BakServers} {
			for _, s := range list {
				// 只用百度自己的域名（且必须 https），不把令牌发给别处。
				if t.trusted(s.Server) {
					return strings.TrimSuffix(s.Server, "/")
				}
			}
		}
	}
	return t.pcsBase
}

func (t *baiduTarget) uploadPart(ctx context.Context, host, full, uploadID string, idx int, data []byte, wantMD5 string) error {
	q := url.Values{"method": {"upload"}, "type": {"tmpfile"}, "path": {full},
		"uploadid": {uploadID}, "partseq": {strconv.Itoa(idx)}}
	body := &baiduBody{multipart: func() (io.Reader, string, error) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, err := mw.CreateFormFile("file", path.Base(full))
		if err != nil {
			return nil, "", err
		}
		fw.Write(data)
		if err := mw.Close(); err != nil {
			return nil, "", err
		}
		return &b, mw.FormDataContentType(), nil
	}}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, time.Duration(attempt)*2*time.Second); err != nil {
				return err
			}
		}
		var part baiduPart
		err := t.call(ctx, http.MethodPost, host+"/rest/2.0/pcs/superfile2", q, body, &part)
		if err == nil {
			if !strings.EqualFold(part.MD5, wantMD5) {
				lastErr = fmt.Errorf("第 %d 片上传后校验不一致", idx+1)
				continue
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ae *baiduAPIError
		if errors.As(err, &ae) && ae.Code != 31034 && ae.Code != 31363 {
			return err // 业务错误重试也没用
		}
		lastErr = err
	}
	return lastErr
}

// ---------------- 查询 / 下载 ----------------

type baiduFile struct {
	FsID           bdInt  `json:"fs_id"`
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	Size           bdInt  `json:"size"`
	IsDir          bdInt  `json:"isdir"`
	ServerMtime    bdInt  `json:"server_mtime"`
	Dlink          string `json:"dlink"`
}

type baiduList struct {
	List    []baiduFile `json:"list"`
	HasMore bdInt       `json:"has_more"`
	Cursor  bdInt       `json:"cursor"`
}

func (f baiduFile) meta() baiduMeta {
	return baiduMeta{fsID: uint64(f.FsID), size: int64(f.Size), mtime: time.Unix(int64(f.ServerMtime), 0)}
}

// statFull 列出父目录，按文件名找到文件（目录视为不存在）。顺手缓存父目录里所有文件的信息。
func (t *baiduTarget) statFull(ctx context.Context, full string) (baiduMeta, error) {
	dir := path.Dir(full)
	var found *baiduMeta
	for start := 0; ; start += baiduListLimit {
		var res baiduList
		q := url.Values{"method": {"list"}, "dir": {dir}, "start": {strconv.Itoa(start)}, "limit": {strconv.Itoa(baiduListLimit)}}
		if err := t.call(ctx, http.MethodGet, t.panBase+"/rest/2.0/xpan/file", q, nil, &res); err != nil {
			return baiduMeta{}, err
		}
		for _, f := range res.List {
			if f.IsDir != 0 {
				continue
			}
			fp := f.Path
			if fp == "" {
				fp = dir + "/" + f.ServerFilename
			}
			m := f.meta()
			t.cache.Store(fp, m)
			if fp == full {
				mm := m
				found = &mm
			}
		}
		if found != nil {
			return *found, nil
		}
		if len(res.List) < baiduListLimit {
			return baiduMeta{}, &baiduAPIError{Code: -9}
		}
	}
}

func (t *baiduTarget) lookup(ctx context.Context, full string) (baiduMeta, error) {
	if v, ok := t.cache.Load(full); ok {
		return v.(baiduMeta), nil
	}
	return t.statFull(ctx, full)
}

func (t *baiduTarget) Stat(ctx context.Context, p string) (Entry, error) {
	p, err := cleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	m, err := t.statFull(ctx, t.full(p))
	if err != nil {
		return Entry{}, t.wrap("查询", p, err)
	}
	return Entry{Path: p, Size: m.size, ModTime: m.mtime}, nil
}

func (t *baiduTarget) Get(ctx context.Context, p string) (io.ReadCloser, error) {
	p, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	full := t.full(p)
	var f baiduFile
	for attempt := 0; ; attempt++ {
		m, err := t.lookup(ctx, full)
		if err != nil {
			return nil, t.wrap("读取", p, err)
		}
		var res baiduList
		q := url.Values{"method": {"filemetas"}, "fsids": {"[" + strconv.FormatUint(m.fsID, 10) + "]"}, "dlink": {"1"}}
		err = t.call(ctx, http.MethodGet, t.panBase+"/rest/2.0/xpan/multimedia", q, nil, &res)
		if err == nil && len(res.List) == 1 && res.List[0].Path == full && res.List[0].Dlink != "" {
			f = res.List[0]
			break
		}
		var ae *baiduAPIError
		if err != nil && !errors.As(err, &ae) {
			return nil, t.wrap("读取", p, err)
		}
		// 缓存过期（文件被替换或删除）：清掉缓存重新查一次。
		t.cache.Delete(full)
		if attempt > 0 {
			if err != nil {
				return nil, t.wrap("读取", p, err)
			}
			return nil, fmt.Errorf("读取「%s」失败：%w", p, ErrNotExist)
		}
	}
	if !t.trusted(f.Dlink) {
		return nil, fmt.Errorf("读取「%s」失败：百度网盘返回的下载地址不可信", p)
	}
	tok, err := t.token(ctx)
	if err != nil {
		return nil, err
	}
	sep := "&"
	if !strings.Contains(f.Dlink, "?") {
		sep = "?"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Dlink+sep+"access_token="+url.QueryEscape(tok), nil)
	if err != nil {
		return nil, fmt.Errorf("读取「%s」失败：下载地址不正确", p)
	}
	req.Header.Set("User-Agent", baiduUserAgent)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取「%s」失败：%s", p, netErrText(ctxErr(ctx, err)))
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		var st baiduStatus
		if json.Unmarshal(data, &st) == nil && st.code() != 0 {
			return nil, t.wrap("读取", p, &baiduAPIError{Code: st.code(), Msg: st.Errmsg + st.ErrorMsg})
		}
		return nil, fmt.Errorf("读取「%s」失败：百度网盘下载返回 HTTP %d", p, resp.StatusCode)
	}
	return newCheckedReadCloser(resp.Body, int64(f.Size)), nil
}

// ---------------- 列表 / 删除 ----------------

func (t *baiduTarget) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix, err := cleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	dir := t.full(prefixDir(prefix))
	var out []Entry
	cursor := 0
	for page := 0; ; page++ {
		var res baiduList
		q := url.Values{"method": {"listall"}, "path": {dir}, "recursion": {"1"},
			"start": {strconv.Itoa(cursor)}, "limit": {strconv.Itoa(baiduListLimit)}}
		if err := t.call(ctx, http.MethodGet, t.panBase+"/rest/2.0/xpan/multimedia", q, nil, &res); err != nil {
			var ae *baiduAPIError
			if errors.As(err, &ae) && baiduNotFound(ae.Code) && page == 0 {
				return nil, nil // 目录不存在
			}
			return nil, t.wrap("列出", prefix, err)
		}
		for _, f := range res.List {
			if f.IsDir != 0 || !strings.HasPrefix(f.Path, t.root+"/") {
				continue
			}
			t.cache.Store(f.Path, f.meta())
			rel := f.Path[len(t.root)+1:]
			if isTempName(baseName(rel)) || !strings.HasPrefix(rel, prefix) {
				continue
			}
			if _, err := cleanPath(rel); err != nil {
				continue
			}
			out = append(out, Entry{Path: rel, Size: int64(f.Size), ModTime: time.Unix(int64(f.ServerMtime), 0)})
		}
		if res.HasMore == 0 {
			return out, nil
		}
		next := int(res.Cursor)
		if next <= cursor {
			return nil, fmt.Errorf("列出「%s」失败：百度网盘分页标记异常", prefix)
		}
		cursor = next
	}
}

type baiduManage struct {
	Info []struct {
		Errno bdInt  `json:"errno"`
		Path  string `json:"path"`
	} `json:"info"`
}

func (t *baiduTarget) deleteFull(ctx context.Context, full string) error {
	list, _ := json.Marshal([]string{full})
	form := url.Values{"async": {"0"}, "filelist": {string(list)}}
	q := url.Values{"method": {"filemanager"}, "opera": {"delete"}}
	var res baiduManage
	err := t.call(ctx, http.MethodPost, t.panBase+"/rest/2.0/xpan/file", q, &baiduBody{form: form}, &res)
	t.cache.Delete(full)
	if err != nil {
		var ae *baiduAPIError
		if errors.As(err, &ae) && baiduNotFound(ae.Code) {
			return nil
		}
		// 批量操作的错误（errno 12）细节在 info 里，callOnce 拿不到，这里再核对一次文件还在不在。
		if errors.As(err, &ae) && ae.Code == 12 {
			if _, serr := t.statFull(ctx, full); serr != nil {
				var se *baiduAPIError
				if errors.As(serr, &se) && baiduNotFound(se.Code) {
					return nil
				}
			}
		}
		return err
	}
	for _, it := range res.Info {
		if it.Errno != 0 && !baiduNotFound(int64(it.Errno)) {
			return &baiduAPIError{Code: int64(it.Errno)}
		}
	}
	return nil
}

func (t *baiduTarget) Delete(ctx context.Context, p string) error {
	p, err := cleanPath(p)
	if err != nil {
		return err
	}
	full := t.full(p)
	// filemanager 删除目录是递归的：先确认它是文件（目录在查询里视为不存在）。
	if _, err := t.lookup(ctx, full); err != nil {
		var ae *baiduAPIError
		if errors.As(err, &ae) && baiduNotFound(ae.Code) {
			return nil
		}
		return t.wrap("删除", p, err)
	}
	if err := t.deleteFull(ctx, full); err != nil {
		return t.wrap("删除", p, err)
	}
	return nil
}

func (t *baiduTarget) Close() error { return nil }
