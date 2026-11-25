package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesMe(r chi.Router) {
	r.Patch("/api/me", s.h(s.handleUpdateMe))
	r.Put("/api/me/prefs", s.h(s.handleSavePrefs))
	r.Post("/api/me/password", s.h(s.handleChangePassword))
	r.Post("/api/me/totp/setup", s.h(s.handleTOTPSetup))
	r.Post("/api/me/totp/enable", s.h(s.handleTOTPEnable))
	r.Post("/api/me/totp/disable", s.h(s.handleTOTPDisable))
	r.Post("/api/me/recovery-codes", s.h(s.handleRegenRecovery))
	r.Get("/api/me/sessions", s.h(s.handleListSessions))
	r.Delete("/api/me/sessions/{pid}", s.h(s.handleRevokeSession))
	r.Post("/api/me/sessions/revoke-others", s.h(s.handleRevokeOthers))
	r.Get("/api/me/tokens", s.h(s.handleListTokens))
	r.Post("/api/me/tokens", s.h(s.handleCreateToken))
	r.Delete("/api/me/tokens/{id}", s.h(s.handleRevokeToken))
	r.Get("/api/users", s.h(s.handleUserDirectory))
}

// sessionOnly 敏感操作只允许网页会话，不允许令牌。
func sessionOnly(r *http.Request) (*AuthInfo, error) {
	a := AuthOf(r)
	if a == nil || a.Session == nil {
		return nil, &APIError{Status: 403, Code: "session_required", Message: "该操作只能在网页里登录后进行"}
	}
	return a, nil
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Avatar      *string `json:"avatar"`
		Locale      *string `json:"locale"`
	}
	if err := readJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	u, err := s.Accounts.UpdateUser(r.Context(), a.User.ID, a.User.ID, func(u *model.User) error {
		if in.DisplayName != nil {
			d := strings.TrimSpace(*in.DisplayName)
			if d == "" || utf8.RuneCountInString(d) > 64 {
				return errors.New("显示名称不能为空且不超过 64 字")
			}
			u.DisplayName = d
		}
		if in.Email != nil {
			e := strings.TrimSpace(*in.Email)
			if len(e) > 191 || (e != "" && !strings.Contains(e, "@")) {
				return errors.New("邮箱格式不正确")
			}
			u.Email = model.StrPtr(e)
		}
		if in.Avatar != nil {
			if len(*in.Avatar) > 512 {
				return errors.New("头像地址太长")
			}
			u.Avatar = *in.Avatar
		}
		if in.Locale != nil {
			if *in.Locale != "en" && *in.Locale != "zh-CN" {
				return errors.New("不支持的语言")
			}
			u.Locale = *in.Locale
		}
		return nil
	})
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"user": viewUser(u)})
}

func (s *Server) handleSavePrefs(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	var prefs json.RawMessage
	if err := readJSONLimit(r, &prefs, 32<<10); err != nil {
		return err
	}
	var probe map[string]any
	if json.Unmarshal(prefs, &probe) != nil {
		return errBad("偏好设置必须是对象")
	}
	u, err := s.Accounts.UpdateUser(r.Context(), a.User.ID, a.User.ID, func(u *model.User) error {
		u.Prefs = string(prefs)
		return nil
	})
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"user": viewUser(u)})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if _, err := s.Auth.CheckPassword(r.Context(), a.User.Username, in.Old, ClientIP(r)); err != nil {
		return &APIError{Status: 400, Code: "bad_password", Message: "当前口令不正确"}
	}
	if err := s.Accounts.SetPassword(r.Context(), a.User.ID, a.User.ID, in.New); err != nil {
		return err
	}
	// 改口令让旧会话全部失效，给当前浏览器换一个新会话
	tok, sess, err := s.Auth.CreateSession(r.Context(), a.User.ID, ClientIP(r), r.UserAgent(), true)
	if err != nil {
		return err
	}
	_ = s.Auth.RevokeUserSessions(r.Context(), a.User.ID, sess.ID)
	s.setSessionCookie(w, r, tok, sess)
	s.audit(r, a.User.ID, "password_changed", "", nil)
	return ok(w, map[string]any{"csrf": sess.CSRF})
}

func (s *Server) verifyPassword(r *http.Request, u *model.User, pw string) error {
	if _, err := s.Auth.CheckPassword(r.Context(), u.Username, pw, ClientIP(r)); err != nil {
		var locked *auth.ErrLocked
		if errors.As(err, &locked) {
			return err
		}
		return &APIError{Status: 400, Code: "bad_password", Message: "口令不正确"}
	}
	return nil
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if err := s.verifyPassword(r, a.User, in.Password); err != nil {
		return err
	}
	if a.User.TOTPEnabled {
		return errConflict("两步验证已经启用")
	}
	secret := auth.NewTOTPSecret()
	data, _ := json.Marshal(map[string]string{"pending_totp": s.Box.Seal(secret)})
	if _, err := s.Store.ExecLocal(r.Context(), "UPDATE sessions SET data = ? WHERE id = ?", string(data), a.Session.ID); err != nil {
		return err
	}
	site := s.Settings.System(r.Context()).SiteName
	return ok(w, map[string]any{"secret": secret, "url": auth.TOTPURL(site, a.User.Username, secret)})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := readJSONLimit(r, &in, 1024); err != nil {
		return err
	}
	var data struct {
		Pending string `json:"pending_totp"`
	}
	_ = json.Unmarshal([]byte(a.Session.Data), &data)
	secret, err := s.Box.Open(data.Pending)
	if err != nil || secret == "" {
		return errBad("请先重新开始设置两步验证")
	}
	if !auth.VerifyTOTP(a.User.ID, secret, in.Code) {
		return &APIError{Status: 400, Code: "bad_code", Message: "验证码不正确"}
	}
	plain, hashes := auth.NewRecoveryCodes()
	hb, _ := json.Marshal(hashes)
	if _, err := s.Accounts.UpdateUser(r.Context(), a.User.ID, a.User.ID, func(u *model.User) error {
		u.TOTPSecret = s.Box.Seal(secret)
		u.TOTPEnabled = true
		u.RecoveryCodes = string(hb)
		return nil
	}); err != nil {
		return err
	}
	_, _ = s.Store.ExecLocal(r.Context(), "UPDATE sessions SET data = ? WHERE id = ?", "{}", a.Session.ID)
	s.audit(r, a.User.ID, "mfa_enabled", "", nil)
	return ok(w, map[string]any{"recovery_codes": plain})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if err := s.verifyPassword(r, a.User, in.Password); err != nil {
		return err
	}
	if s.Settings.System(r.Context()).RequireMFA {
		return errBad("管理员要求所有账号启用两步验证，不能关闭")
	}
	secret, _ := s.Box.Open(a.User.TOTPSecret)
	if err := s.Auth.CheckMFA(r.Context(), a.User, secret, in.Code, ClientIP(r)); err != nil {
		return err
	}
	if _, err := s.Accounts.UpdateUser(r.Context(), a.User.ID, a.User.ID, func(u *model.User) error {
		u.TOTPSecret, u.TOTPEnabled, u.RecoveryCodes = "", false, "[]"
		return nil
	}); err != nil {
		return err
	}
	s.audit(r, a.User.ID, "mfa_disabled", "", nil)
	return ok(w, nil)
}

func (s *Server) handleRegenRecovery(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if err := s.verifyPassword(r, a.User, in.Password); err != nil {
		return err
	}
	if !a.User.TOTPEnabled {
		return errBad("尚未启用两步验证")
	}
	plain, hashes := auth.NewRecoveryCodes()
	hb, _ := json.Marshal(hashes)
	if _, err := s.Accounts.UpdateUser(r.Context(), a.User.ID, a.User.ID, func(u *model.User) error {
		u.RecoveryCodes = string(hb)
		return nil
	}); err != nil {
		return err
	}
	s.audit(r, a.User.ID, "recovery_codes_regenerated", "", nil)
	return ok(w, map[string]any{"recovery_codes": plain})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	list, err := s.Auth.ListSessions(r.Context(), a.User.ID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		out = append(out, map[string]any{"id": auth.SessionPublicID(x.ID), "created_at": x.CreatedAt, "last_seen_at": x.LastSeenAt,
			"ip": x.IP, "ua": x.UA, "current": a.Session != nil && a.Session.ID == x.ID})
	}
	return ok(w, map[string]any{"sessions": out})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	pid := chi.URLParam(r, "pid")
	list, err := s.Auth.ListSessions(r.Context(), a.User.ID)
	if err != nil {
		return err
	}
	for _, x := range list {
		if auth.SessionPublicID(x.ID) == pid {
			_ = s.Auth.RevokeSession(r.Context(), x.ID)
			s.audit(r, a.User.ID, "session_revoked", pid, nil)
			return ok(w, nil)
		}
	}
	return errNotFound("会话")
}

func (s *Server) handleRevokeOthers(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	if err := s.Auth.RevokeUserSessions(r.Context(), a.User.ID, a.Session.ID); err != nil {
		return err
	}
	s.audit(r, a.User.ID, "sessions_revoked_others", "", nil)
	return ok(w, nil)
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	var list []model.APIToken
	if err := s.Store.Select(r.Context(), &list, "SELECT * FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC", a.User.ID); err != nil {
		return err
	}
	type view struct {
		model.APIToken
		Write  bool     `json:"write"`
		Spaces []string `json:"spaces"`
	}
	out := make([]view, 0, len(list))
	for i := range list {
		out = append(out, view{APIToken: list[i], Write: tokenCanWrite(&list[i]), Spaces: tokenSpaces(&list[i])})
	}
	return ok(w, map[string]any{"tokens": out})
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		Name      string   `json:"name"`
		Write     bool     `json:"write"`
		Spaces    []string `json:"spaces"`
		ExpiresAt *int64   `json:"expires_at"`
	}
	if err := readJSONLimit(r, &in, 16<<10); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" {
		return errBad("请给令牌起个名字")
	}
	for _, sp := range in.Spaces {
		if s.Cat.SpaceRole(a.User.ID, sp) == model.RoleNone {
			return errBad("只能限定到你有权限的空间")
		}
	}
	raw, t, err := s.Auth.CreateToken(r.Context(), a.User.ID, in.Name, in.Write, in.Spaces, in.ExpiresAt)
	if err != nil {
		return err
	}
	s.audit(r, a.User.ID, "token_created", t.ID, map[string]any{"name": t.Name, "write": in.Write})
	return ok(w, map[string]any{"token": raw, "info": t})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	id := chi.URLParam(r, "id")
	var t model.APIToken
	if err := s.Store.Get(r.Context(), &t, "SELECT * FROM api_tokens WHERE id = ? AND user_id = ?", id, a.User.ID); err != nil {
		return errNotFound("令牌")
	}
	now := model.Now()
	t.RevokedAt = &now
	if err := s.writePut(r, "api_tokens", &t); err != nil {
		return err
	}
	s.audit(r, a.User.ID, "token_revoked", id, nil)
	return ok(w, nil)
}

// handleUserDirectory 列出全部启用的用户（分享、提及时选人用）。
func (s *Server) handleUserDirectory(w http.ResponseWriter, r *http.Request) error {
	out := []map[string]any{}
	for _, u := range s.Cat.Users() {
		if u.Status == model.UserActive {
			out = append(out, map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "avatar": u.Avatar})
		}
	}
	return ok(w, map[string]any{"users": out})
}
