package comments

import (
	"errors"
	"net/http"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

func apiErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return web.NotFound("评论")
	case errors.Is(err, ErrEmpty), errors.Is(err, ErrTooLong):
		return web.Bad(err.Error())
	case errors.Is(err, ErrNotOwner):
		return &web.APIError{Status: 403, Code: "forbidden", Message: err.Error()}
	}
	return err
}

// Mount 挂载评论接口。
func (s *Service) Mount(ws *web.Server, r chi.Router) {
	h := ws.H
	r.Get("/api/pages/{id}/comments", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if _, err := ws.NeedPage(r, id, model.RoleViewer); err != nil {
			return err
		}
		ths, err := s.List(r.Context(), id, r.URL.Query().Get("resolved") == "1")
		if err != nil {
			return err
		}
		role := ws.PageRole(r, id)
		return web.OK(w, map[string]any{"threads": ths, "can_comment": model.RoleAtLeast(role, model.RoleCommenter),
			"can_moderate": model.RoleAtLeast(role, model.RoleManager)})
	}))
	r.Post("/api/pages/{id}/comments", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if _, err := ws.NeedPage(r, id, model.RoleCommenter); err != nil {
			return err
		}
		var in AddInput
		if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
			return err
		}
		th, err := s.Add(r.Context(), web.UserOf(r).ID, id, in)
		if err != nil {
			return apiErr(err)
		}
		return web.OK(w, map[string]any{"thread": th})
	}))
	r.Patch("/api/comments/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		c, err := s.Comment(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			return apiErr(err)
		}
		if _, err := ws.NeedPage(r, c.PageID, model.RoleCommenter); err != nil {
			return err
		}
		var in struct {
			Body string `json:"body"`
		}
		if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
			return err
		}
		out, err := s.Edit(r.Context(), web.UserOf(r).ID, c.ID, in.Body)
		if err != nil {
			return apiErr(err)
		}
		return web.OK(w, map[string]any{"comment": out})
	}))
	r.Delete("/api/comments/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		c, err := s.Comment(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			return apiErr(err)
		}
		if _, err := ws.NeedPage(r, c.PageID, model.RoleCommenter); err != nil {
			return err
		}
		gone, err := s.Delete(r.Context(), web.UserOf(r).ID, c.ID, model.RoleAtLeast(ws.PageRole(r, c.PageID), model.RoleManager))
		if err != nil {
			return apiErr(err)
		}
		return web.OK(w, map[string]any{"thread_id": c.ThreadID, "thread_deleted": gone})
	}))
	r.Post("/api/comment-threads/{id}/resolve", h(func(w http.ResponseWriter, r *http.Request) error {
		tid := chi.URLParam(r, "id")
		pid, err := s.PageOfThread(r.Context(), tid)
		if err != nil {
			return apiErr(err)
		}
		if _, err := ws.NeedPage(r, pid, model.RoleCommenter); err != nil {
			return err
		}
		var in struct {
			Resolved bool `json:"resolved"`
		}
		if err := web.ReadJSONLimit(r, &in, 1024); err != nil {
			return err
		}
		th, err := s.Resolve(r.Context(), web.UserOf(r).ID, tid, in.Resolved)
		if err != nil {
			return apiErr(err)
		}
		return web.OK(w, map[string]any{"thread": th})
	}))
}
