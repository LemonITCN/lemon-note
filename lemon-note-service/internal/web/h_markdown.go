package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesMarkdown(r chi.Router) {
	r.Post("/api/markdown/parse", s.h(s.handleParseMarkdown))
	r.Get("/api/pages/{id}/export", s.h(s.handleExportPage))
}

// RenderOpts 按当前请求的权限解析页面标题与用户名。
func (s *Server) RenderOpts(r *http.Request) lnmd.RenderOptions {
	return lnmd.RenderOptions{
		PageTitle: func(id string) (string, bool) {
			p, ok := s.Cat.Page(id)
			if !ok || s.PageRole(r, id) == model.RoleNone {
				return "", false
			}
			return p.Title, true
		},
		UserName: func(id string) (string, bool) {
			u, ok := s.Cat.User(id)
			return u.DisplayName, ok
		},
	}
}

// handleParseMarkdown 把粘贴进来的 Markdown 转成块（前端粘贴时调用）。
func (s *Server) handleParseMarkdown(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Markdown string `json:"markdown"`
	}
	if err := readJSONLimit(r, &in, 8<<20); err != nil {
		return err
	}
	nodes, err := lnmd.Parse(in.Markdown)
	if err != nil {
		return errBad("Markdown 无法解析：" + err.Error())
	}
	if nodes == nil {
		nodes = []lnmd.Node{}
	}
	return ok(w, map[string]any{"blocks": nodes})
}

func (s *Server) handleExportPage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	meta, err := s.NeedPage(r, id, model.RoleViewer)
	if err != nil {
		return err
	}
	bs, err := s.Pages.Blocks(r.Context(), id)
	if err != nil {
		return err
	}
	nodes := make([]lnmd.Node, 0, len(bs))
	for _, b := range bs {
		var n lnmd.Node
		if json.Unmarshal([]byte(b.Data), &n) == nil {
			nodes = append(nodes, n)
		}
	}
	md := lnmd.Render(nodes, s.RenderOpts(r))
	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = "untitled"
	}
	body := "# " + title + "\n\n" + md + "\n"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s.md", url.PathEscape(title)))
	_, err = w.Write([]byte(body))
	return err
}
