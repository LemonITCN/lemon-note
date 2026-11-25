package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
)

// audit 写安全审计日志（失败不影响主流程）。
// Audit 写一条安全审计日志（给其他模块用）。
func (s *Server) Audit(r *http.Request, userID, action, target string, detail any) {
	s.audit(r, userID, action, target, detail)
}

// Allow 按 key 限流：每秒 rate 个、最多 burst 个。
func (s *Server) Allow(key string, rate, burst float64) bool {
	return s.limiter.Allow(key, rate, burst)
}

func (s *Server) audit(r *http.Request, userID, action, target string, detail any) {
	d, _ := json.Marshal(detail)
	if detail == nil {
		d = []byte("{}")
	}
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	row := model.AuditLog{ID: ids.New(), TS: model.Now(), UserID: model.StrPtr(userID), Action: action, IP: ClientIP(r), UA: ua, Target: target, Detail: string(d)}
	if _, err := s.Store.Write(context.WithoutCancel(r.Context()), "system", func(tx *store.Tx) error { return tx.Put("audit_logs", row) }); err != nil {
		s.log.Warn("写审计日志失败", "err", err)
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, tok string, sess *model.Session) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: s.IsHTTPS(r),
		SameSite: http.SameSiteLaxMode, Expires: time.UnixMilli(sess.ExpiresAt)})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.IsHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// userView 是返回给本人的用户信息。
type userView struct {
	*model.User
	Prefs json.RawMessage `json:"prefs"`
}

func viewUser(u *model.User) userView {
	p := json.RawMessage(u.Prefs)
	if len(p) == 0 {
		p = json.RawMessage("{}")
	}
	return userView{User: u, Prefs: p}
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) error {
	sys := s.Settings.System(r.Context())
	out := map[string]any{
		"mode": "normal", "version": version.Version, "site_name": sys.SiteName, "default_locale": sys.DefaultLocale,
		"allow_registration": sys.AllowRegistration, "public_sharing": sys.PublicSharing,
	}
	if a := AuthOf(r); a != nil && a.Session != nil {
		out["csrf"] = a.Session.CSRF
		if a.Session.MFAOK {
			out["user"] = viewUser(a.User)
			out["mfa_setup_required"] = s.mfaSetupRequired(r.Context(), a.User)
		} else {
			out["mfa_pending"] = true
		}
	}
	return ok(w, out)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	if !s.limiter.Allow("login:"+ClientIP(r), 1, 20) {
		return &APIError{Status: 429, Code: "too_many_attempts", Message: "请求过于频繁，请稍后再试"}
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	u, err := s.Auth.CheckPassword(r.Context(), in.Username, in.Password, ClientIP(r))
	if err != nil {
		s.audit(r, "", "login_failed", auth.NormalizeUsername(in.Username), nil)
		return err
	}
	mfaOK := !u.TOTPEnabled
	tok, sess, err := s.Auth.CreateSession(r.Context(), u.ID, ClientIP(r), r.UserAgent(), mfaOK)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, r, tok, sess)
	if !mfaOK {
		return ok(w, map[string]any{"mfa_required": true, "csrf": sess.CSRF})
	}
	s.afterLogin(r, u)
	return ok(w, map[string]any{"user": viewUser(u), "csrf": sess.CSRF, "mfa_setup_required": s.mfaSetupRequired(r.Context(), u)})
}

func (s *Server) afterLogin(r *http.Request, u *model.User) {
	now := model.Now()
	_, _ = s.Accounts.UpdateUser(context.WithoutCancel(r.Context()), u.ID, u.ID, func(x *model.User) error {
		x.LastLoginAt = &now
		return nil
	})
	s.audit(r, u.ID, "login", u.Username, nil)
}

func (s *Server) handleMFA(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	if a == nil || a.Session == nil {
		return errUnauth()
	}
	if a.Session.MFAOK {
		return ok(w, map[string]any{"user": viewUser(a.User), "csrf": a.Session.CSRF})
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := readJSONLimit(r, &in, 1024); err != nil {
		return err
	}
	secret, err := s.Box.Open(a.User.TOTPSecret)
	if err != nil {
		return err
	}
	if err := s.Auth.CheckMFA(r.Context(), a.User, secret, in.Code, ClientIP(r)); err != nil {
		s.audit(r, a.User.ID, "mfa_failed", "", nil)
		return err
	}
	tok, sess, err := s.Auth.MarkMFA(r.Context(), a.Session)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, r, tok, sess)
	s.afterLogin(r, a.User)
	return ok(w, map[string]any{"user": viewUser(a.User), "csrf": sess.CSRF})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if a := AuthOf(r); a != nil && a.Session != nil {
		_ = s.Auth.RevokeSession(r.Context(), a.Session.ID)
	}
	s.clearSessionCookie(w, r)
	return ok(w, nil)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	if a == nil || (a.Session != nil && !a.Session.MFAOK) {
		return errUnauth()
	}
	out := map[string]any{"user": viewUser(a.User), "mfa_setup_required": s.mfaSetupRequired(r.Context(), a.User)}
	if a.Session != nil {
		out["csrf"] = a.Session.CSRF
	}
	return ok(w, out)
}

// handleRegister 开放注册（管理员在设置里打开后才可用）。
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) error {
	if !s.Settings.System(r.Context()).AllowRegistration {
		return errForbidden()
	}
	if !s.limiter.Allow("register:"+ClientIP(r), 0.02, 3) {
		return &APIError{Status: 429, Code: "too_many_attempts", Message: "请求过于频繁，请稍后再试"}
	}
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Locale      string `json:"locale"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	u, err := s.Accounts.CreateUser(r.Context(), "", accounts.NewUser{Username: in.Username, DisplayName: in.DisplayName, Password: in.Password, Role: model.RoleMember, Locale: in.Locale})
	if err != nil {
		return err
	}
	s.audit(r, u.ID, "register", u.Username, nil)
	tok, sess, err := s.Auth.CreateSession(r.Context(), u.ID, ClientIP(r), r.UserAgent(), true)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, r, tok, sess)
	return ok(w, map[string]any{"user": viewUser(u), "csrf": sess.CSRF})
}
