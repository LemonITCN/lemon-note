package web

import (
	"net/http"
	"runtime"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesAdmin(r chi.Router) {
	r.Get("/api/admin/users", s.h(s.handleAdminUsers))
	r.Post("/api/admin/users", s.h(s.handleAdminCreateUser))
	r.Patch("/api/admin/users/{id}", s.h(s.handleAdminPatchUser))
	r.Post("/api/admin/users/{id}/password", s.h(s.handleAdminResetPassword))
	r.Post("/api/admin/users/{id}/reset-2fa", s.h(s.handleAdminReset2FA))
	r.Get("/api/admin/settings", s.h(s.handleAdminGetSettings))
	r.Put("/api/admin/settings", s.h(s.handleAdminSaveSettings))
	r.Get("/api/admin/status", s.h(s.handleAdminStatus))
	r.Get("/api/admin/audit", s.h(s.handleAdminAudit))
	r.Post("/api/admin/snapshot", s.h(s.handleAdminSnapshot))
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) error {
	var users []model.User
	if err := s.Store.Select(r.Context(), &users, "SELECT * FROM users ORDER BY created_at"); err != nil {
		return err
	}
	return ok(w, map[string]any{"users": users})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		Locale      string `json:"locale"`
	}
	if err := readJSONLimit(r, &in, 8192); err != nil {
		return err
	}
	u, err := s.Accounts.CreateUser(r.Context(), UserOf(r).ID, accounts.NewUser{Username: in.Username, DisplayName: in.DisplayName,
		Password: in.Password, Role: in.Role, Locale: in.Locale})
	if err != nil {
		return err
	}
	s.audit(r, UserOf(r).ID, "user_created", u.ID, map[string]any{"username": u.Username, "role": u.Role})
	return ok(w, map[string]any{"user": u})
}

func (s *Server) handleAdminPatchUser(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	var in struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Status      *string `json:"status"`
	}
	if err := readJSONLimit(r, &in, 8192); err != nil {
		return err
	}
	me := UserOf(r)
	if id == me.ID && ((in.Role != nil && *in.Role != model.RoleAdmin) || (in.Status != nil && *in.Status != model.UserActive)) {
		return errBad("不能降低自己的权限或停用自己")
	}
	u, err := s.Accounts.UpdateUser(r.Context(), me.ID, id, func(u *model.User) error {
		if in.DisplayName != nil && *in.DisplayName != "" {
			u.DisplayName = *in.DisplayName
		}
		if in.Role != nil && (*in.Role == model.RoleAdmin || *in.Role == model.RoleMember) {
			u.Role = *in.Role
		}
		if in.Status != nil && (*in.Status == model.UserActive || *in.Status == model.UserDisabled) {
			u.Status = *in.Status
		}
		return nil
	})
	if err != nil {
		return err
	}
	if u.Status == model.UserDisabled {
		_ = s.Auth.RevokeUserSessions(r.Context(), u.ID, "")
	}
	s.audit(r, me.ID, "user_updated", id, in)
	return ok(w, map[string]any{"user": u})
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if err := s.Accounts.SetPassword(r.Context(), UserOf(r).ID, id, in.Password); err != nil {
		return err
	}
	_ = s.Auth.RevokeUserSessions(r.Context(), id, "")
	s.audit(r, UserOf(r).ID, "user_password_reset", id, nil)
	return ok(w, nil)
}

func (s *Server) handleAdminReset2FA(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.Accounts.UpdateUser(r.Context(), UserOf(r).ID, id, func(u *model.User) error {
		u.TOTPSecret, u.TOTPEnabled, u.RecoveryCodes = "", false, "[]"
		return nil
	}); err != nil {
		return err
	}
	_ = s.Auth.RevokeUserSessions(r.Context(), id, "")
	s.audit(r, UserOf(r).ID, "user_mfa_reset", id, nil)
	return ok(w, nil)
}

// 设置里的密钥字段：读取时打码，保存时空串表示不改。
const maskedSecret = "********"

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) error {
	sys := s.Settings.System(r.Context())
	if sys.WebSearch.APIKey != "" {
		sys.WebSearch.APIKey = maskedSecret
	}
	return ok(w, map[string]any{"settings": sys})
}

func (s *Server) handleAdminSaveSettings(w http.ResponseWriter, r *http.Request) error {
	cur := s.Settings.System(r.Context())
	var in settings.System
	if err := readJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	if in.WebSearch.APIKey == maskedSecret {
		in.WebSearch.APIKey = cur.WebSearch.APIKey
	} else if in.WebSearch.APIKey != "" {
		in.WebSearch.APIKey = s.Box.Seal(in.WebSearch.APIKey)
	}
	clamp := func(v *int, lo, hi, def int) {
		if *v < lo || *v > hi {
			*v = def
		}
	}
	d := settings.Defaults()
	clamp(&in.SessionIdleDays, 1, 365, d.SessionIdleDays)
	clamp(&in.SessionMaxDays, 1, 3650, d.SessionMaxDays)
	clamp(&in.MaxUploadMB, 1, 10240, d.MaxUploadMB)
	clamp(&in.TrashRetainDays, 1, 3650, d.TrashRetainDays)
	clamp(&in.HistoryRetainDays, 1, 36500, d.HistoryRetainDays)
	clamp(&in.AgentMaxSteps, 5, 1000, d.AgentMaxSteps)
	clamp(&in.AgentBudgetTokens, 20000, 2000000, d.AgentBudgetTokens)
	if in.SiteName == "" {
		in.SiteName = d.SiteName
	}
	if err := s.Settings.SaveSystem(r.Context(), UserOf(r).ID, in); err != nil {
		return err
	}
	s.audit(r, UserOf(r).ID, "settings_changed", "system", nil)
	return s.handleAdminGetSettings(w, r)
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) error {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	st := s.Inst.Status(r.Context())
	return ok(w, map[string]any{
		"version": version.Version, "commit": version.Commit, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"uptime_seconds": int64(time.Since(s.Started).Seconds()), "memory_bytes": mem.Alloc, "connections": s.Hub.Count(),
		"persist": st, "listen": s.Boot.Listen, "config_path": s.Boot.Path(),
	})
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) error {
	limit := atoiDefault(r.URL.Query().Get("limit"), 200)
	if limit > 1000 {
		limit = 1000
	}
	var logs []model.AuditLog
	if err := s.Store.Select(r.Context(), &logs, "SELECT * FROM audit_logs ORDER BY ts DESC LIMIT ?", limit); err != nil {
		return err
	}
	return ok(w, map[string]any{"logs": logs})
}

func (s *Server) handleAdminSnapshot(w http.ResponseWriter, r *http.Request) error {
	if err := s.Inst.Snapshot(r.Context()); err != nil {
		return err
	}
	return ok(w, map[string]any{"persist": s.Inst.Status(r.Context())})
}
