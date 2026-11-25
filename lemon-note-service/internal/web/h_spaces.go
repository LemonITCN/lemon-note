package web

import (
	"encoding/json"
	"net/http"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesSpaces(r chi.Router) {
	r.Get("/api/spaces", s.h(s.handleListSpaces))
	r.Post("/api/spaces", s.h(s.handleCreateSpace))
	r.Patch("/api/spaces/{id}", s.h(s.handleUpdateSpace))
	r.Delete("/api/spaces/{id}", s.h(s.handleDeleteSpace))
	r.Get("/api/spaces/{id}/tree", s.h(s.handleSpaceTree))
	r.Get("/api/spaces/{id}/trash", s.h(s.handleSpaceTrash))
	r.Get("/api/spaces/{id}/members", s.h(s.handleListMembers))
	r.Put("/api/spaces/{id}/members/{uid}", s.h(s.handleSetMember))
	r.Delete("/api/spaces/{id}/members/{uid}", s.h(s.handleRemoveMember))
	r.Post("/api/spaces/{id}/transfer", s.h(s.handleTransferSpace))
}

func (s *Server) handleListSpaces(w http.ResponseWriter, r *http.Request) error {
	a := AuthOf(r)
	out := []map[string]any{}
	for _, sp := range s.Cat.VisibleSpaces(a.User.ID) {
		role := s.SpaceRole(r, sp.ID)
		if role == model.RoleNone {
			continue
		}
		out = append(out, map[string]any{"space": sp, "role": role, "settings": json.RawMessage(orJSON(sp.Settings))})
	}
	return ok(w, map[string]any{"spaces": out})
}

func orJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

func (s *Server) handleCreateSpace(w http.ResponseWriter, r *http.Request) error {
	a, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
		Icon string `json:"icon"`
	}
	if err := readJSONLimit(r, &in, 16<<10); err != nil {
		return err
	}
	sp, err := s.Accounts.CreateSharedSpace(r.Context(), a.User.ID, in.Name, in.Icon)
	if err != nil {
		return err
	}
	s.audit(r, a.User.ID, "space_created", sp.ID, map[string]any{"name": sp.Name})
	return ok(w, map[string]any{"space": sp, "role": model.RoleOwner})
}

func (s *Server) needSpace(r *http.Request, id, need string) error {
	role := s.SpaceRole(r, id)
	if role == model.RoleNone {
		return errNotFound("空间")
	}
	if !model.RoleAtLeast(role, need) {
		return errForbidden()
	}
	return nil
}

func (s *Server) handleUpdateSpace(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := s.needSpace(r, id, model.RoleManager); err != nil {
		return err
	}
	var in struct {
		Name     *string         `json:"name"`
		Icon     *string         `json:"icon"`
		Settings json.RawMessage `json:"settings"`
	}
	if err := readJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	sp, err := s.Accounts.UpdateSpace(r.Context(), UserOf(r).ID, id, in.Name, in.Icon, in.Settings)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"space": sp})
}

func (s *Server) handleDeleteSpace(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := s.needSpace(r, id, model.RoleOwner); err != nil {
		return err
	}
	if err := s.Accounts.DeleteSpace(r.Context(), UserOf(r).ID, id); err != nil {
		return err
	}
	s.audit(r, UserOf(r).ID, "space_deleted", id, nil)
	return ok(w, nil)
}

// handleSpaceTree 返回空间页面树（不含回收站、数据库行及其子页面）。
func (s *Server) handleSpaceTree(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := s.needSpace(r, id, model.RoleViewer); err != nil {
		return err
	}
	all := s.Cat.SpacePages(id)
	byID := map[string]catalog.PageMeta{}
	for _, p := range all {
		byID[p.ID] = p
	}
	underRow := func(p catalog.PageMeta) bool {
		seen := 0
		for cur := p; seen < 1000; seen++ {
			if cur.DatabaseID != "" {
				return true
			}
			if cur.ParentID == "" {
				return false
			}
			parent, ok := byID[cur.ParentID]
			if !ok {
				return false
			}
			cur = parent
		}
		return false
	}
	out := make([]catalog.PageMeta, 0, len(all))
	for _, p := range all {
		if p.TrashedAt != 0 || underRow(p) {
			continue
		}
		out = append(out, p)
	}
	return ok(w, map[string]any{"pages": out})
}

func (s *Server) handleSpaceTrash(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := s.needSpace(r, id, model.RoleEditor); err != nil {
		return err
	}
	out := []catalog.PageMeta{}
	for _, p := range s.Cat.SpacePages(id) {
		if p.TrashedAt != 0 && p.TrashRoot == p.ID {
			out = append(out, p)
		}
	}
	return ok(w, map[string]any{"pages": out})
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := s.needSpace(r, id, model.RoleViewer); err != nil {
		return err
	}
	sp, _ := s.Cat.Space(id)
	out := []map[string]any{}
	if u, ok := s.Cat.User(sp.OwnerID); ok {
		out = append(out, map[string]any{"user": u, "role": model.RoleOwner})
	}
	for uid, role := range s.Cat.Members(id) {
		if u, ok := s.Cat.User(uid); ok {
			out = append(out, map[string]any{"user": u, "role": role})
		}
	}
	return ok(w, map[string]any{"members": out})
}

func (s *Server) handleSetMember(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := s.needSpace(r, id, model.RoleManager); err != nil {
		return err
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := readJSONLimit(r, &in, 1024); err != nil {
		return err
	}
	uid := chi.URLParam(r, "uid")
	// 管理员不能把别人提升到高于自己的角色
	if in.Role == model.RoleManager && s.SpaceRole(r, id) != model.RoleOwner {
		return errForbidden()
	}
	if err := s.Accounts.SetMember(r.Context(), UserOf(r).ID, id, uid, in.Role); err != nil {
		return err
	}
	s.audit(r, UserOf(r).ID, "space_member_set", id, map[string]any{"user": uid, "role": in.Role})
	return ok(w, nil)
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	uid := chi.URLParam(r, "uid")
	if uid != UserOf(r).ID { // 自己可以退出
		if err := s.needSpace(r, id, model.RoleManager); err != nil {
			return err
		}
	}
	if err := s.Accounts.RemoveMember(r.Context(), UserOf(r).ID, id, uid); err != nil {
		return err
	}
	s.audit(r, UserOf(r).ID, "space_member_removed", id, map[string]any{"user": uid})
	return ok(w, nil)
}

func (s *Server) handleTransferSpace(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := s.needSpace(r, id, model.RoleOwner); err != nil {
		return err
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := readJSONLimit(r, &in, 1024); err != nil {
		return err
	}
	if _, ok := s.Cat.User(in.UserID); !ok {
		return errNotFound("用户")
	}
	if err := s.Accounts.TransferOwnership(r.Context(), UserOf(r).ID, id, in.UserID); err != nil {
		return err
	}
	s.audit(r, UserOf(r).ID, "space_transferred", id, map[string]any{"to": in.UserID})
	return ok(w, nil)
}
