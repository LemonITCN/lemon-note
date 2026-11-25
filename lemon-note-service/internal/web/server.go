// Package web 是 HTTP 层：中间件、接口、静态前端。
package web

import (
	"context"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/realtime"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/search"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/go-chi/chi/v5"
)

// Deps 是 HTTP 层依赖的服务。
type Deps struct {
	Log      *slog.Logger
	Boot     *config.Bootstrap
	Inst     *persist.Instance
	Store    *store.Store
	Cat      *catalog.Catalog
	Auth     *auth.Service
	Settings *settings.Service
	Pages    *pages.Service
	Accounts *accounts.Service
	Hub      *realtime.Hub
	Files    *files.Service
	Search   *search.Index
	Box      *crypt.Box
	Started  time.Time
}

// Mount 让其他模块（智能体、数据库、备份、MCP 等）挂载自己的路由。
type Mount func(s *Server, r chi.Router)

// Server 是 HTTP 服务。
type Server struct {
	Deps
	log     *slog.Logger
	trusted []netip.Prefix
	mounts  []Mount
	limiter *ipLimiter
	// MCP 是 /mcp 端点（只接受 Bearer 令牌，不走会话与 CSRF）
	MCP http.Handler
	// OAuthLookup 校验 OAuth 访问令牌（OAuth 模块注入）
	OAuthLookup func(ctx context.Context, token string) (*AuthInfo, bool)
	// Extra 是额外的顶层路由（OAuth 元数据、公开分享页等）
	Extra []func(r chi.Router)
	// WSHandlers 处理浏览器经 WebSocket 发来的自定义消息
	WSHandlers map[string]func(conn *realtime.Conn, data []byte)
}

func NewServer(d Deps) *Server {
	s := &Server{Deps: d, log: d.Log, limiter: newIPLimiter(), WSHandlers: map[string]func(*realtime.Conn, []byte){}}
	for _, p := range d.Boot.TrustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		if pr, err := netip.ParsePrefix(p); err == nil {
			s.trusted = append(s.trusted, pr)
		}
	}
	return s
}

// AddMount 注册额外路由。
func (s *Server) AddMount(m Mount) { s.mounts = append(s.mounts, m) }

type ctxKey int

const (
	keyAuth ctxKey = iota
	keyIP
)

// AuthInfo 是当前请求的身份。
type AuthInfo struct {
	User     *model.User
	Session  *model.Session
	Token    *model.APIToken
	CanWrite bool
	Spaces   []string // 令牌限定的空间，空表示不限
}

// AuthOf 返回请求身份（未登录为 nil）。
func AuthOf(r *http.Request) *AuthInfo {
	a, _ := r.Context().Value(keyAuth).(*AuthInfo)
	return a
}

// UserOf 返回当前用户（未登录为 nil）。
func UserOf(r *http.Request) *model.User {
	if a := AuthOf(r); a != nil {
		return a.User
	}
	return nil
}

// ClientIP 返回客户端真实 IP。
func ClientIP(r *http.Request) string {
	if v, _ := r.Context().Value(keyIP).(string); v != "" {
		return v
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// ConnID 返回前端带来的 WebSocket 连接 id（实时广播去回声用）。
func ConnID(r *http.Request) string {
	v := r.Header.Get("X-Lemon-Conn")
	if len(v) > 40 {
		return ""
	}
	return v
}

func (s *Server) realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := host
		if addr, err := netip.ParseAddr(host); err == nil && s.isTrusted(addr) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				for i := len(parts) - 1; i >= 0; i-- {
					cand := strings.TrimSpace(parts[i])
					a, err := netip.ParseAddr(cand)
					if err != nil {
						break
					}
					ip = cand
					if !s.isTrusted(a) {
						break
					}
				}
			} else if xr := r.Header.Get("X-Real-IP"); xr != "" {
				ip = strings.TrimSpace(xr)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyIP, ip)))
	})
}

func (s *Server) isTrusted(a netip.Addr) bool {
	for _, p := range s.trusted {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// IsHTTPS 判断请求是否经 HTTPS 到达（直连 TLS 或受信代理声明）。
func (s *Server) IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if a, err := netip.ParseAddr(host); err == nil && s.isTrusted(a) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("请求处理崩溃", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeJSON(w, 500, map[string]any{"error": &APIError{Code: "internal", Message: "服务器内部错误"}})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const contentSecurityPolicy = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https: http:; media-src 'self' blob: https:; connect-src 'self' ws: wss:; " +
	"font-src 'self' data:; frame-src https:; worker-src 'self' blob:; object-src 'none'; base-uri 'self'; " +
	"form-action 'self'; frame-ancestors 'none'"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), geolocation=(), payment=()")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		if s.IsHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") {
			lvl := slog.LevelDebug
			if sw.status >= 500 {
				lvl = slog.LevelError
			}
			s.log.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds(), "ip", ClientIP(r))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

const sessionCookie = "ln_session"

// authenticate 解析会话 Cookie 或 Bearer 令牌；不拒绝请求，只填充身份。
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var info *AuthInfo
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			raw := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			if strings.HasPrefix(raw, auth.TokenPrefix) {
				if t, u, err := s.Auth.LookupToken(r.Context(), raw); err == nil {
					info = &AuthInfo{User: u, Token: t, CanWrite: tokenCanWrite(t), Spaces: tokenSpaces(t)}
				}
			}
		} else if c, err := r.Cookie(sessionCookie); err == nil {
			if sess, u, err := s.Auth.LookupSession(r.Context(), c.Value); err == nil {
				info = &AuthInfo{User: u, Session: sess, CanWrite: true}
			}
		}
		if info != nil {
			r = r.WithContext(context.WithValue(r.Context(), keyAuth, info))
		}
		next.ServeHTTP(w, r)
	})
}

// csrf 用 Cookie 登录的改动类请求必须带 X-CSRF-Token，且来源一致。
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		a := AuthOf(r)
		if a != nil && a.Session != nil {
			if r.Header.Get("X-CSRF-Token") != a.Session.CSRF {
				s.writeError(w, r, &APIError{Status: 403, Code: "csrf", Message: "请求校验失败，请刷新页面后重试"})
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" && a != nil && a.Session != nil {
			if !sameHost(origin, r.Host) {
				s.writeError(w, r, &APIError{Status: 403, Code: "csrf", Message: "不允许跨站请求"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameHost(origin, host string) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return strings.EqualFold(o, host)
}

// requireUser 要求已登录且已通过两步验证。
func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := AuthOf(r)
		if a == nil {
			s.writeError(w, r, errUnauth())
			return
		}
		if a.Session != nil && !a.Session.MFAOK {
			s.writeError(w, r, &APIError{Status: 401, Code: "mfa_required", Message: "需要完成两步验证"})
			return
		}
		if a.Session != nil && s.mfaSetupRequired(r.Context(), a.User) && !strings.HasPrefix(r.URL.Path, "/api/me") && r.URL.Path != "/api/auth/me" {
			s.writeError(w, r, &APIError{Status: 403, Code: "mfa_setup_required", Message: "管理员要求所有账号启用两步验证"})
			return
		}
		if !a.CanWrite && r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.writeError(w, r, &APIError{Status: 403, Code: "read_only_token", Message: "该令牌只有读取权限"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) mfaSetupRequired(ctx context.Context, u *model.User) bool {
	return s.Settings.System(ctx).RequireMFA && !u.TOTPEnabled
}

// RequireAdmin 要求系统管理员（网页会话）。
func (s *Server) RequireAdmin(next http.Handler) http.Handler { return s.requireAdmin(next) }

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := AuthOf(r)
		if a == nil || a.User.Role != model.RoleAdmin || a.Token != nil {
			s.writeError(w, r, errForbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler 组装全部路由。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.realIP, s.securityHeaders, s.accessLog)
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate, s.csrf)
		r.Get("/api/bootstrap", s.h(s.handleBootstrap))
		r.Post("/api/auth/login", s.h(s.handleLogin))
		r.Post("/api/auth/mfa", s.h(s.handleMFA))
		r.Post("/api/auth/register", s.h(s.handleRegister))
		r.Post("/api/auth/logout", s.h(s.handleLogout))
		r.Get("/api/auth/me", s.h(s.handleMe))
		r.Get("/api/files/{id}", s.h(s.handleDownload)) // 未登录时一律当不存在；公开分享页走 /api/public
		r.Group(func(r chi.Router) {
			r.Use(s.requireUser)
			s.routesMe(r)
			s.routesSpaces(r)
			s.routesPages(r)
			s.routesSearch(r)
			s.routesMarkdown(r)
			r.Get("/api/ws", s.handleWS)
			r.Post("/api/files", s.h(s.handleUpload))
			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)
				s.routesAdmin(r)
			})
			for _, m := range s.mounts {
				m(s, r)
			}
		})
	})
	if s.MCP != nil {
		r.Handle("/mcp", s.MCP)
		r.Handle("/mcp/*", s.MCP)
	}
	for _, fn := range s.Extra {
		fn(r)
	}
	r.NotFound(s.serveStatic)
	return r
}

// BearerAuth 用个人访问令牌认证（给 MCP 用）。
func (s *Server) BearerAuth(r *http.Request) (*AuthInfo, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	if strings.HasPrefix(raw, auth.TokenPrefix) {
		if t, u, err := s.Auth.LookupToken(r.Context(), raw); err == nil {
			return &AuthInfo{User: u, Token: t, CanWrite: tokenCanWrite(t), Spaces: tokenSpaces(t)}, true
		}
	}
	if s.OAuthLookup != nil {
		if a, ok := s.OAuthLookup(r.Context(), raw); ok {
			return a, true
		}
	}
	return nil, false
}

func tokenCanWrite(t *model.APIToken) bool {
	var sc auth.TokenScopes
	_ = jsonUnmarshal(t.Scopes, &sc)
	return sc.Write
}

func tokenSpaces(t *model.APIToken) []string {
	var sp []string
	_ = jsonUnmarshal(t.SpaceIDs, &sp)
	return sp
}
