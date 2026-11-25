package web

import (
	"net/http"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/go-chi/chi/v5"
)

// routesMarkdown 挂 Markdown 解析接口。页面导出（GET /api/pages/{id}/export）由 exporter 模块挂载。
func (s *Server) routesMarkdown(r chi.Router) {
	r.Post("/api/markdown/parse", s.h(s.handleParseMarkdown))
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
