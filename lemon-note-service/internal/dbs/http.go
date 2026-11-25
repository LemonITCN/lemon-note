package dbs

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Mount 挂载数据库接口。
func (s *Service) Mount(ws *web.Server, r chi.Router) {
	h := ws.H
	need := func(r *http.Request, id, role string) error {
		_, err := ws.NeedPage(r, id, role)
		return err
	}
	r.Post("/api/databases", h(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			SpaceID  string  `json:"space_id"`
			ParentID string  `json:"parent_id"`
			Title    string  `json:"title"`
			Inline   bool    `json:"inline"`
			Schema   *Schema `json:"schema"`
			AfterID  string  `json:"after_id"`
		}
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		if in.ParentID != "" {
			m, err := ws.NeedPage(r, in.ParentID, model.RoleEditor)
			if err != nil {
				return err
			}
			in.SpaceID = m.SpaceID
		} else if !model.RoleAtLeast(ws.SpaceRole(r, in.SpaceID), model.RoleEditor) {
			return web.Forbidden()
		}
		u := web.UserOf(r)
		var p *model.Page
		var v *ViewInfo
		_, err := ws.Store.Write(r.Context(), u.ID, func(tx *store.Tx) error {
			var err error
			p, v, err = s.CreateTx(tx, u.ID, CreateInput{SpaceID: in.SpaceID, ParentID: in.ParentID, Title: in.Title, Inline: in.Inline, Schema: in.Schema, Zh: u.Locale != "en", AfterID: in.AfterID})
			return err
		})
		if err != nil {
			return err
		}
		return web.OK(w, map[string]any{"page": p, "views": []*ViewInfo{v}})
	}))
	r.Get("/api/databases/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if err := need(r, id, model.RoleViewer); err != nil {
			return err
		}
		info, err := s.Info(r.Context(), id)
		if err != nil {
			return err
		}
		return web.OK(w, map[string]any{"page": info.Page, "schema": info.Schema, "views": info.Views, "role": ws.PageRole(r, id)})
	}))
	r.Put("/api/databases/{id}/schema", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if err := need(r, id, model.RoleEditor); err != nil {
			return err
		}
		var sc Schema
		if err := web.ReadJSONLimit(r, &sc, 1<<20); err != nil {
			return err
		}
		for _, p := range sc.Properties {
			if p.Type == TRelation && p.DatabaseID != "" {
				if err := need(r, p.DatabaseID, model.RoleViewer); err != nil {
					return web.Bad("关联的数据库不存在或没有权限")
				}
			}
		}
		out, err := s.UpdateSchema(r.Context(), web.UserOf(r).ID, id, &sc)
		if err != nil {
			return err
		}
		return web.OK(w, map[string]any{"schema": out})
	}))
	r.Post("/api/databases/{id}/query", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if err := need(r, id, model.RoleViewer); err != nil {
			return err
		}
		var in QueryInput
		if err := web.ReadJSONLimit(r, &in, 1<<20); err != nil {
			return err
		}
		_, res, err := s.Query(r.Context(), id, in)
		if err != nil {
			return err
		}
		return web.OK(w, res)
	}))
	r.Post("/api/databases/{id}/rows", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if err := need(r, id, model.RoleEditor); err != nil {
			return err
		}
		var in struct {
			Title      string         `json:"title"`
			Props      map[string]any `json:"props"`
			AfterID    string         `json:"after_id"`
			First      bool           `json:"first"`
			TemplateID string         `json:"template_id"`
			Template   bool           `json:"is_template"`
		}
		if err := decodeUseNumber(r, &in); err != nil {
			return err
		}
		u := web.UserOf(r)
		var p *model.Page
		_, err := ws.Store.Write(r.Context(), u.ID, func(tx *store.Tx) error {
			tx.SetOrigin(web.ConnID(r))
			var blocks []pages.BlockDoc
			if in.TemplateID != "" {
				var tp model.Page
				if err := tx.Get(&tp, "SELECT * FROM pages WHERE id = ? AND database_id = ?", in.TemplateID, id); err != nil {
					return errors.New("模板不存在")
				}
				var bs []model.Block
				if err := tx.Select(&bs, "SELECT * FROM blocks WHERE page_id = ?", tp.ID); err != nil {
					return err
				}
				for _, b := range bs {
					blocks = append(blocks, pages.BlockDoc{Type: b.Type, Data: json.RawMessage(b.Data), Position: b.Position})
				}
				if in.Title == "" {
					in.Title = tp.Title
				}
				tr := rowFromPage(&tp)
				for k, v := range tr.Props {
					if _, set := in.Props[k]; !set {
						if in.Props == nil {
							in.Props = map[string]any{}
						}
						in.Props[k] = v
					}
				}
			}
			var err error
			p, err = s.CreateRowTx(tx, u.ID, id, in.Title, nil, in.AfterID, blocks, in.First)
			if err != nil {
				return err
			}
			if len(in.Props) > 0 {
				if err := s.SetPropsTx(tx, u.ID, p.ID, rawProps(in.Props)); err != nil {
					return err
				}
			}
			if in.Template {
				p.IsTemplate = true
				return tx.Put("pages", p)
			}
			return nil
		})
		if err != nil {
			return err
		}
		t, err := s.Load(r.Context(), id)
		if err != nil {
			return err
		}
		row := t.Row(p.ID)
		if row != nil {
			t.FillComputed([]*Row{row})
		}
		return web.OK(w, map[string]any{"row": row})
	}))
	r.Patch("/api/rows/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if err := need(r, id, model.RoleEditor); err != nil {
			return err
		}
		var in struct {
			Props   map[string]any `json:"props"`
			AfterID *string        `json:"after_id"`
			First   bool           `json:"first"`
		}
		if err := decodeUseNumber(r, &in); err != nil {
			return err
		}
		u := web.UserOf(r)
		_, err := ws.Store.Write(r.Context(), u.ID, func(tx *store.Tx) error {
			tx.SetOrigin(web.ConnID(r))
			if len(in.Props) > 0 {
				if err := s.SetPropsTx(tx, u.ID, id, rawProps(in.Props)); err != nil {
					return err
				}
			}
			if in.AfterID != nil || in.First {
				after := ""
				if in.AfterID != nil {
					after = *in.AfterID
				}
				return s.MoveRowTx(tx, u.ID, id, after, in.First)
			}
			return nil
		})
		if err != nil {
			return err
		}
		m, _ := ws.Cat.Page(id)
		t, err := s.Load(r.Context(), m.DatabaseID)
		if err != nil {
			return err
		}
		row := t.Row(id)
		if row != nil {
			t.FillComputed([]*Row{row})
		}
		return web.OK(w, map[string]any{"row": row})
	}))
	r.Post("/api/databases/{id}/views", h(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if err := need(r, id, model.RoleEditor); err != nil {
			return err
		}
		var in struct {
			Name   string     `json:"name"`
			Type   string     `json:"type"`
			HostID string     `json:"host_id"`
			Config ViewConfig `json:"config"`
		}
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		v, err := s.AddView(r.Context(), web.UserOf(r).ID, id, in.HostID, in.Name, in.Type, in.Config)
		if err != nil {
			return err
		}
		return web.OK(w, map[string]any{"view": v})
	}))
	r.Patch("/api/views/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		vid := chi.URLParam(r, "id")
		_, dbID, err := s.View(r.Context(), vid)
		if err != nil {
			return web.NotFound("视图")
		}
		if err := need(r, dbID, model.RoleEditor); err != nil {
			return err
		}
		var in struct {
			Name     *string     `json:"name"`
			Type     *string     `json:"type"`
			Config   *ViewConfig `json:"config"`
			Position *string     `json:"position"`
		}
		if err := web.ReadJSONLimit(r, &in, 1<<20); err != nil {
			return err
		}
		v, err := s.UpdateView(r.Context(), web.UserOf(r).ID, vid, in.Name, in.Type, in.Config, in.Position)
		if err != nil {
			return err
		}
		return web.OK(w, map[string]any{"view": v})
	}))
	r.Delete("/api/views/{id}", h(func(w http.ResponseWriter, r *http.Request) error {
		vid := chi.URLParam(r, "id")
		_, dbID, err := s.View(r.Context(), vid)
		if err != nil {
			return web.NotFound("视图")
		}
		if err := need(r, dbID, model.RoleEditor); err != nil {
			return err
		}
		if err := s.DeleteView(r.Context(), web.UserOf(r).ID, vid); err != nil {
			return err
		}
		return web.OK(w, nil)
	}))
	r.Post("/api/charts/query", h(func(w http.ResponseWriter, r *http.Request) error {
		var in ChartSpec
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		if in.Source.DatabaseID == "" {
			return web.Bad("图表没有绑定数据库")
		}
		if err := need(r, in.Source.DatabaseID, model.RoleViewer); err != nil {
			return err
		}
		opt, err := s.ChartOption(r.Context(), in)
		if err != nil {
			return err
		}
		return web.OK(w, map[string]any{"option": opt})
	}))
}

func decodeUseNumber(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return web.Bad("请求体不是合法的 JSON")
	}
	return nil
}

// rawProps 前端传来的是存储格式（选项 id、用户 id 等），这里统一转成通用值让 NormalizeValue 处理。
func rawProps(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if n, ok := v.(json.Number); ok {
			f, _ := n.Float64()
			out[k] = f
			continue
		}
		out[k] = v
	}
	return out
}

var _ = strings.TrimSpace
