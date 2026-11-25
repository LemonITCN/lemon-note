package web

import (
	"encoding/json"
	"net/http"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesInbox(r chi.Router) {
	r.Get("/api/notifications", s.h(s.handleNotifications))
	r.Post("/api/notifications/read", s.h(s.handleNotificationsRead))
}

// handleNotifications 收件箱：分享给我、评论回复、@ 提及。看不到的页面的通知不返回。
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) error {
	uid := UserOf(r).ID
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}
	var list []model.Notification
	if err := s.Store.Select(r.Context(), &list, "SELECT * FROM notifications WHERE user_id = ? ORDER BY created_at DESC LIMIT ?", uid, limit); err != nil {
		return err
	}
	type item struct {
		model.Notification
		PageTitle string          `json:"page_title"`
		PageIcon  string          `json:"page_icon,omitempty"`
		PageKind  string          `json:"page_kind,omitempty"`
		ActorName string          `json:"actor_name"`
		Detail    json.RawMessage `json:"detail"`
	}
	out := []item{}
	for _, n := range list {
		it := item{Notification: n, Detail: json.RawMessage(n.Data)}
		if !json.Valid(it.Detail) {
			it.Detail = json.RawMessage("{}")
		}
		if n.PageID != nil {
			p, ok := s.Cat.Page(*n.PageID)
			if !ok || p.TrashedAt != 0 || s.PageRole(r, p.ID) == model.RoleNone {
				continue
			}
			it.PageTitle, it.PageIcon, it.PageKind = p.Title, p.Icon, p.Kind
		}
		if n.ActorID != nil {
			if u, ok := s.Cat.User(*n.ActorID); ok {
				it.ActorName = u.DisplayName
			}
		}
		out = append(out, it)
	}
	var unread int
	_ = s.Store.QueryRow(r.Context(), "SELECT count(*) FROM notifications WHERE user_id = ? AND read_at IS NULL", uid).Scan(&unread)
	return ok(w, map[string]any{"items": out, "unread": unread})
}

// handleNotificationsRead 标为已读：ids 为空且 all=true 时全部标为已读。
func (s *Server) handleNotificationsRead(w http.ResponseWriter, r *http.Request) error {
	uid := UserOf(r).ID
	var in struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if err := readJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	var list []model.Notification
	if err := s.Store.Select(r.Context(), &list, "SELECT * FROM notifications WHERE user_id = ? AND read_at IS NULL LIMIT 1000", uid); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, id := range in.IDs {
		want[id] = true
	}
	now := model.Now()
	var marks []model.Notification
	for _, n := range list {
		if in.All || want[n.ID] {
			n.ReadAt = &now
			marks = append(marks, n)
		}
	}
	if len(marks) > 0 {
		if _, err := s.Store.Write(r.Context(), uid, func(tx *store.Tx) error {
			for _, n := range marks {
				if err := tx.Put("notifications", n); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return ok(w, nil)
}
