package exporter

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Mount 挂载导出接口：GET /api/pages/{id}/export?format=md|html&children=1
func (s *Service) Mount(ws *web.Server, r chi.Router) {
	r.Get("/api/pages/{id}/export", ws.H(func(w http.ResponseWriter, r *http.Request) error {
		id := chi.URLParam(r, "id")
		if _, err := ws.NeedPage(r, id, model.RoleViewer); err != nil {
			return err
		}
		q := r.URL.Query()
		o := Options{Format: strings.ToLower(q.Get("format")), Children: q.Get("children") == "1" || q.Get("children") == "true"}
		if o.Format == "" || o.Format == "markdown" {
			o.Format = "md"
		}
		if o.Format != "md" && o.Format != "html" {
			return web.Bad("不支持的导出格式")
		}
		acc := Access{
			CanRead:      func(pid string) bool { return ws.PageRole(r, pid) != model.RoleNone },
			CanReadSpace: func(sid string) bool { return ws.SpaceRole(r, sid) != model.RoleNone },
			Render:       ws.RenderOpts(r),
		}
		out, single, err := s.Plan(r.Context(), acc, id, o)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", out.ContentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(out.Filename)))
		w.Header().Set("Cache-Control", "no-store")
		if single {
			return s.WriteSingle(r.Context(), w, acc, id, o)
		}
		if err := s.WriteZip(r.Context(), w, acc, id, o); err != nil {
			// 响应头已经发出，只能记日志并中断连接
			s.log.Warn("导出中断", "page", id, "err", err)
			panic(http.ErrAbortHandler)
		}
		return nil
	}))
}
