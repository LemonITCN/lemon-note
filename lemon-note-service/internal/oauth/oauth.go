// Package oauth 是给 MCP 客户端（Claude Code、Cursor、Codex 等）用的 OAuth 2.1 授权服务器：
// 动态注册（RFC 7591）、授权码 + PKCE（只支持 S256）、刷新令牌轮换、撤销，
// 以及受保护资源元数据（RFC 9728）与授权服务器元数据（RFC 8414）。
//
// 只支持公开客户端（没有客户端密钥）。授权码 10 分钟、一次性；访问令牌 1 小时；
// 刷新令牌 60 天，每用一次换新，旧的再被拿来用视为泄露，整条链作废。
// 这些都是本机表（不进数据目录日志）：换机器恢复后需要重新授权，这是有意的。
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
)

const (
	codeTTL    = 10 * time.Minute
	accessTTL  = time.Hour
	refreshTTL = 60 * 24 * time.Hour

	AccessPrefix  = "ln_oat_"
	RefreshPrefix = "ln_ort_"

	ScopeRead  = "notes:read"
	ScopeWrite = "notes:write"
)

// Service OAuth 服务。
type Service struct {
	st   *store.Store
	cat  *catalog.Catalog
	ws   *web.Server
	base func(r *http.Request) string
}

// New 创建 OAuth 服务；base 返回对外访问地址（不带结尾斜杠）。
func New(st *store.Store, cat *catalog.Catalog, ws *web.Server, base func(r *http.Request) string) *Service {
	return &Service{st: st, cat: cat, ws: ws, base: base}
}

// ---------- 存储结构 ----------

type client struct {
	ID           string `db:"id"`
	Name         string `db:"name"`
	RedirectURIs string `db:"redirect_uris"`
	Data         string `db:"data"`
	CreatedAt    int64  `db:"created_at"`
}

func (c *client) uris() []string {
	var out []string
	_ = json.Unmarshal([]byte(c.RedirectURIs), &out)
	return out
}

type code struct {
	ID        string `db:"id"` // 授权码的哈希
	ClientID  string `db:"client_id"`
	UserID    string `db:"user_id"`
	Data      string `db:"data"`
	ExpiresAt int64  `db:"expires_at"`
}

type codeData struct {
	RedirectURI string   `json:"redirect_uri"`
	Challenge   string   `json:"code_challenge"`
	Write       bool     `json:"write"`
	Spaces      []string `json:"spaces,omitempty"`
	Resource    string   `json:"resource,omitempty"`
	CodeID      string   `json:"code_id"` // 授权码重放时用它找出发出去的令牌
}

type token struct {
	ID        string `db:"id"` // 令牌的哈希
	Kind      string `db:"kind"`
	ClientID  string `db:"client_id"`
	UserID    string `db:"user_id"`
	Data      string `db:"data"`
	ExpiresAt int64  `db:"expires_at"`
	CreatedAt int64  `db:"created_at"`
	RevokedAt *int64 `db:"revoked_at"`
}

type tokenData struct {
	Write  bool     `json:"write"`
	Spaces []string `json:"spaces,omitempty"`
	Family string   `json:"family"`  // 同一次授权衍生出的全部令牌
	CodeID string   `json:"code_id"` // 来自哪个授权码
}

func (t *token) data() tokenData {
	var d tokenData
	_ = json.Unmarshal([]byte(t.Data), &d)
	return d
}

func randToken(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// ---------- 访问令牌校验（给 MCP 用） ----------

// Lookup 校验访问令牌，返回身份。
func (s *Service) Lookup(ctx context.Context, raw string) (*web.AuthInfo, bool) {
	if !strings.HasPrefix(raw, AccessPrefix) || len(raw) > 100 {
		return nil, false
	}
	var t token
	if err := s.st.Get(ctx, &t, "SELECT * FROM oauth_tokens WHERE id = ? AND kind = 'access'", hash(raw)); err != nil {
		return nil, false
	}
	if t.RevokedAt != nil || t.ExpiresAt < model.Now() {
		return nil, false
	}
	var u model.User
	if err := s.st.Get(ctx, &u, "SELECT * FROM users WHERE id = ?", t.UserID); err != nil || u.Status != model.UserActive {
		return nil, false
	}
	d := t.data()
	return &web.AuthInfo{User: &u, CanWrite: d.Write, Spaces: d.Spaces}, true
}

// ---------- 重定向地址校验 ----------

var errBadRedirect = errors.New("回调地址不合法")

// validRedirect 允许：https；本机回环地址的 http（任意端口）；自定义协议（桌面应用回调，如 cursor://）。
func validRedirect(raw string) error {
	if len(raw) > 2000 {
		return errBadRedirect
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Fragment != "" {
		return errBadRedirect
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Host == "" {
			return errBadRedirect
		}
	case "http":
		if !isLoopback(u.Hostname()) {
			return errors.New("http 回调只允许本机地址（localhost / 127.0.0.1 / [::1]）")
		}
	case "javascript", "data", "file", "vbscript", "about", "blob", "ws", "wss", "ftp":
		return errBadRedirect
	}
	return nil
}

func isLoopback(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// redirectMatches 按 RFC 8252：回环地址忽略端口，其余必须完全一致。
func redirectMatches(registered []string, got string) bool {
	gu, err := url.Parse(got)
	if err != nil {
		return false
	}
	for _, r := range registered {
		if r == got {
			return true
		}
		ru, err := url.Parse(r)
		if err != nil {
			continue
		}
		if strings.EqualFold(ru.Scheme, "http") && strings.EqualFold(gu.Scheme, "http") && isLoopback(ru.Hostname()) &&
			ru.Hostname() == gu.Hostname() && ru.Path == gu.Path && ru.RawQuery == gu.RawQuery {
			return true
		}
	}
	return false
}

func (s *Service) client(ctx context.Context, id string) (*client, error) {
	if id == "" || len(id) > 100 {
		return nil, errors.New("client_id 不存在")
	}
	var c client
	if err := s.st.Get(ctx, &c, "SELECT * FROM oauth_clients WHERE id = ?", id); err != nil {
		return nil, errors.New("client_id 不存在")
	}
	return &c, nil
}
