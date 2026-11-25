package web

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/fracidx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesPages(r chi.Router) {
	r.Get("/api/shared-with-me", s.h(s.handleSharedWithMe))
	r.Post("/api/pages", s.h(s.handleCreatePage))
	r.Get("/api/pages/{id}", s.h(s.handleGetPage))
	r.Patch("/api/pages/{id}", s.h(s.handlePatchPage))
	r.Post("/api/pages/{id}/move", s.h(s.handleMovePage))
	r.Post("/api/pages/{id}/duplicate", s.h(s.handleDuplicatePage))
	r.Delete("/api/pages/{id}", s.h(s.handleTrashPage))
	r.Post("/api/pages/{id}/restore", s.h(s.handleRestorePage))
	r.Delete("/api/trash/{id}", s.h(s.handlePurgePage))
	r.Post("/api/pages/{id}/ops", s.h(s.handleBlockOps))
	r.Get("/api/pages/{id}/versions", s.h(s.handleListVersions))
	r.Get("/api/pages/{id}/versions/{vid}", s.h(s.handleGetVersion))
	r.Post("/api/pages/{id}/versions/{vid}/restore", s.h(s.handleRestoreVersion))
	r.Post("/api/pages/{id}/visit", s.h(s.handleVisit))
	r.Get("/api/recent", s.h(s.handleRecent))
	r.Get("/api/favorites", s.h(s.handleFavorites))
	r.Put("/api/favorites/{id}", s.h(s.handleAddFavorite))
	r.Delete("/api/favorites/{id}", s.h(s.handleRemoveFavorite))
}

// PageRole 返回当前请求对页面的角色（含令牌的空间限定）。
func (s *Server) PageRole(r *http.Request, pageID string) string {
	a := AuthOf(r)
	if a == nil {
		return model.RoleNone
	}
	p, ok := s.Cat.Page(pageID)
	if !ok {
		return model.RoleNone
	}
	if len(a.Spaces) > 0 && !slices.Contains(a.Spaces, p.SpaceID) {
		return model.RoleNone
	}
	role := s.Cat.PageRole(a.User.ID, pageID)
	if !a.CanWrite && model.RoleAtLeast(role, model.RoleCommenter) {
		role = model.RoleViewer
	}
	return role
}

// SpaceRole 返回当前请求在空间里的角色（含令牌的空间限定）。
func (s *Server) SpaceRole(r *http.Request, spaceID string) string {
	a := AuthOf(r)
	if a == nil {
		return model.RoleNone
	}
	if len(a.Spaces) > 0 && !slices.Contains(a.Spaces, spaceID) {
		return model.RoleNone
	}
	role := s.Cat.SpaceRole(a.User.ID, spaceID)
	if !a.CanWrite && model.RoleAtLeast(role, model.RoleCommenter) {
		role = model.RoleViewer
	}
	return role
}

// NeedPage 要求对页面至少有 need 角色，返回页面信息。
func (s *Server) NeedPage(r *http.Request, pageID, need string) (catalog.PageMeta, error) {
	p, ok := s.Cat.Page(pageID)
	role := s.PageRole(r, pageID)
	if !ok || role == model.RoleNone {
		return p, errNotFound("页面")
	}
	if !model.RoleAtLeast(role, need) {
		return p, errForbidden()
	}
	return p, nil
}

func (s *Server) writePut(r *http.Request, table string, v any) error {
	_, err := s.Store.Write(r.Context(), UserOf(r).ID, func(tx *store.Tx) error { return tx.Put(table, v) })
	return err
}

func (s *Server) handleSharedWithMe(w http.ResponseWriter, r *http.Request) error {
	return ok(w, map[string]any{"pages": s.Cat.SharedWithUser(UserOf(r).ID)})
}

// breadcrumbs 返回用户可读的祖先链。
func (s *Server) breadcrumbs(r *http.Request, pageID string) []catalog.PageMeta {
	anc := s.Cat.Ancestors(pageID)
	out := []catalog.PageMeta{}
	for _, a := range anc {
		if s.PageRole(r, a.ID) != model.RoleNone {
			out = append(out, a)
		} else {
			out = out[:0]
		}
	}
	return out
}

func (s *Server) handleGetPage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	meta, err := s.NeedPage(r, id, model.RoleViewer)
	if err != nil {
		return err
	}
	doc, err := s.Pages.Doc(r.Context(), id)
	if err != nil {
		return err
	}
	space, _ := s.Cat.Space(meta.SpaceID)
	children := []catalog.PageMeta{}
	for _, c := range s.Cat.Children(meta.SpaceID, id) {
		if c.TrashedAt == 0 && c.DatabaseID == "" {
			children = append(children, c)
		}
	}
	return ok(w, map[string]any{"page": doc.Page, "flags": doc.Flags, "props": doc.Props, "blocks": doc.Blocks,
		"role": s.PageRole(r, id), "breadcrumbs": s.breadcrumbs(r, id), "space": space, "children": children})
}

func (s *Server) handleCreatePage(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		SpaceID    string            `json:"space_id"`
		ParentID   string            `json:"parent_id"`
		Kind       string            `json:"kind"`
		Title      string            `json:"title"`
		Icon       string            `json:"icon"`
		AfterID    string            `json:"after_id"`
		First      bool              `json:"first"`
		Flags      model.PageFlags   `json:"flags"`
		Blocks     []pages.BlockDoc  `json:"blocks"`
		TemplateID string            `json:"template_id"`
		Props      json.RawMessage   `json:"props"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.ParentID != "" {
		parent, err := s.NeedPage(r, in.ParentID, model.RoleEditor)
		if err != nil {
			return err
		}
		in.SpaceID = parent.SpaceID
		if parent.Kind == model.KindDatabase {
			return errBad("在数据库里新建行请使用数据库接口")
		}
	} else if !model.RoleAtLeast(s.SpaceRole(r, in.SpaceID), model.RoleEditor) {
		return errForbidden()
	}
	if in.Kind == model.KindDatabase {
		return errBad("新建数据库请使用数据库接口")
	}
	uid := UserOf(r).ID
	var p *model.Page
	if in.TemplateID != "" {
		if _, err := s.NeedPage(r, in.TemplateID, model.RoleViewer); err != nil {
			return err
		}
		nid, err := s.Pages.Duplicate(r.Context(), uid, in.TemplateID, true, "")
		if err != nil {
			return err
		}
		if err := s.Pages.Move(r.Context(), uid, nid, pages.MoveInput{SpaceID: in.SpaceID, ParentID: in.ParentID, AfterID: in.AfterID, First: in.First}); err != nil {
			return err
		}
		f := false
		p, err = s.Pages.Update(r.Context(), uid, nid, pages.Patch{Title: &in.Title, IsTemplate: &f})
		if err != nil {
			return err
		}
	} else {
		var err error
		p, err = s.Pages.Create(r.Context(), uid, pages.CreateInput{SpaceID: in.SpaceID, ParentID: in.ParentID, Kind: model.KindPage,
			Title: in.Title, Icon: in.Icon, AfterID: in.AfterID, First: in.First, Flags: in.Flags, Blocks: in.Blocks})
		if err != nil {
			return err
		}
	}
	meta, _ := s.Cat.Page(p.ID)
	return ok(w, map[string]any{"page": p, "meta": meta})
}

func (s *Server) handlePatchPage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	meta, err := s.NeedPage(r, id, model.RoleEditor)
	if err != nil {
		return err
	}
	var patch pages.Patch
	if err := readJSON(r, &patch); err != nil {
		return err
	}
	if patch.Props != nil && meta.DatabaseID == "" {
		patch.Props = nil
	}
	if patch.Props != nil {
		return errBad("数据库行属性请使用数据库接口修改")
	}
	p, err := s.Pages.Update(r.Context(), UserOf(r).ID, id, patch)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"page": p, "flags": p.GetFlags()})
}

func (s *Server) handleMovePage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	meta, err := s.NeedPage(r, id, model.RoleEditor)
	if err != nil {
		return err
	}
	var in pages.MoveInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.ParentID != "" {
		if _, err := s.NeedPage(r, in.ParentID, model.RoleEditor); err != nil {
			return err
		}
	} else {
		target := in.SpaceID
		if target == "" {
			target = meta.SpaceID
		}
		if !model.RoleAtLeast(s.SpaceRole(r, target), model.RoleEditor) {
			return errForbidden()
		}
	}
	if in.SpaceID != "" && in.SpaceID != meta.SpaceID && !model.RoleAtLeast(s.SpaceRole(r, meta.SpaceID), model.RoleEditor) {
		return &APIError{Status: 403, Code: "forbidden", Message: "只有原空间的编辑者才能把页面移出空间"}
	}
	if err := s.Pages.Move(r.Context(), UserOf(r).ID, id, in); err != nil {
		return err
	}
	m, _ := s.Cat.Page(id)
	return ok(w, map[string]any{"meta": m})
}

func (s *Server) handleDuplicatePage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	meta, err := s.NeedPage(r, id, model.RoleViewer)
	if err != nil {
		return err
	}
	if meta.ParentID != "" {
		if _, err := s.NeedPage(r, meta.ParentID, model.RoleEditor); err != nil {
			return err
		}
	} else if !model.RoleAtLeast(s.SpaceRole(r, meta.SpaceID), model.RoleEditor) {
		return errForbidden()
	}
	var in struct {
		WithChildren bool   `json:"with_children"`
		Suffix       string `json:"suffix"`
	}
	_ = readJSON(r, &in)
	nid, err := s.Pages.Duplicate(r.Context(), UserOf(r).ID, id, in.WithChildren, in.Suffix)
	if err != nil {
		return err
	}
	m, _ := s.Cat.Page(nid)
	return ok(w, map[string]any{"meta": m})
}

func (s *Server) handleTrashPage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleEditor); err != nil {
		return err
	}
	if err := s.Pages.Trash(r.Context(), UserOf(r).ID, id); err != nil {
		return err
	}
	return ok(w, nil)
}

func (s *Server) handleRestorePage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleEditor); err != nil {
		return err
	}
	if err := s.Pages.Restore(r.Context(), UserOf(r).ID, id); err != nil {
		return err
	}
	m, _ := s.Cat.Page(id)
	return ok(w, map[string]any{"meta": m})
}

func (s *Server) handlePurgePage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	meta, err := s.NeedPage(r, id, model.RoleEditor)
	if err != nil {
		return err
	}
	if meta.TrashedAt == 0 {
		return errBad("只能彻底删除回收站里的页面")
	}
	if err := s.Pages.Purge(r.Context(), UserOf(r).ID, id); err != nil {
		return err
	}
	s.audit(r, UserOf(r).ID, "page_purged", id, map[string]any{"title": meta.Title})
	return ok(w, nil)
}

func (s *Server) handleBlockOps(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleEditor); err != nil {
		return err
	}
	var in struct {
		Ops []pages.BlockOp `json:"ops"`
	}
	if err := readJSONLimit(r, &in, 32<<20); err != nil {
		return err
	}
	res, err := s.Pages.ApplyOps(r.Context(), UserOf(r).ID, ConnID(r), id, in.Ops)
	if err != nil {
		return err
	}
	return ok(w, res)
}

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleViewer); err != nil {
		return err
	}
	vs, err := s.Pages.Versions(r.Context(), id, atoiDefault(r.URL.Query().Get("limit"), 200))
	if err != nil {
		return err
	}
	if vs == nil {
		vs = []pages.VersionInfo{}
	}
	return ok(w, map[string]any{"versions": vs})
}

func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleViewer); err != nil {
		return err
	}
	doc, err := s.Pages.VersionDoc(r.Context(), id, chi.URLParam(r, "vid"))
	if err != nil {
		return err
	}
	return ok(w, doc)
}

func (s *Server) handleRestoreVersion(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleEditor); err != nil {
		return err
	}
	if err := s.Pages.RestoreVersion(r.Context(), UserOf(r).ID, id, chi.URLParam(r, "vid")); err != nil {
		return err
	}
	return ok(w, nil)
}

func (s *Server) handleVisit(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleViewer); err != nil {
		return err
	}
	uid := UserOf(r).ID
	_ = s.Store.PutLocal(r.Context(), "recent_pages", model.RecentPage{ID: uid + ":" + id, UserID: uid, PageID: id, VisitedAt: model.Now()})
	return ok(w, nil)
}

func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) error {
	uid := UserOf(r).ID
	var rows []model.RecentPage
	if err := s.Store.Select(r.Context(), &rows, "SELECT * FROM recent_pages WHERE user_id = ? ORDER BY visited_at DESC LIMIT 50", uid); err != nil {
		return err
	}
	out := []map[string]any{}
	for _, rp := range rows {
		if m, ok := s.Cat.Page(rp.PageID); ok && m.TrashedAt == 0 && s.PageRole(r, rp.PageID) != model.RoleNone {
			out = append(out, map[string]any{"meta": m, "visited_at": rp.VisitedAt})
		}
	}
	return ok(w, map[string]any{"pages": out})
}

func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) error {
	uid := UserOf(r).ID
	var favs []model.Favorite
	if err := s.Store.Select(r.Context(), &favs, "SELECT * FROM favorites WHERE user_id = ?", uid); err != nil {
		return err
	}
	slices.SortFunc(favs, func(a, b model.Favorite) int {
		if a.Position < b.Position {
			return -1
		}
		if a.Position > b.Position {
			return 1
		}
		return 0
	})
	out := []catalog.PageMeta{}
	for _, f := range favs {
		if m, ok := s.Cat.Page(f.PageID); ok && m.TrashedAt == 0 && s.PageRole(r, f.PageID) != model.RoleNone {
			out = append(out, m)
		}
	}
	return ok(w, map[string]any{"pages": out})
}

func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.NeedPage(r, id, model.RoleViewer); err != nil {
		return err
	}
	uid := UserOf(r).ID
	_, err := s.Store.Write(r.Context(), uid, func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM favorites WHERE user_id = ? AND page_id = ?", uid, id).Scan(&n); err != nil || n > 0 {
			return err
		}
		var last string
		_ = tx.QueryRow("SELECT COALESCE(max(position), '') FROM favorites WHERE user_id = ?", uid).Scan(&last)
		pos, err := fracidx.Between(last, "")
		if err != nil {
			pos, _ = fracidx.Between("", "")
		}
		return tx.Put("favorites", model.Favorite{ID: ids.New(), UserID: uid, PageID: id, Position: pos, CreatedAt: model.Now()})
	})
	if err != nil {
		return err
	}
	return ok(w, nil)
}

func (s *Server) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	uid := UserOf(r).ID
	_, err := s.Store.Write(r.Context(), uid, func(tx *store.Tx) error {
		var f model.Favorite
		if err := tx.Get(&f, "SELECT * FROM favorites WHERE user_id = ? AND page_id = ?", uid, id); err != nil {
			return nil
		}
		return tx.Delete("favorites", f.ID)
	})
	if err != nil {
		return err
	}
	return ok(w, nil)
}
