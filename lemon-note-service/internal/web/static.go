package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) { ServeStatic(w, r) }

// ServeStatic 提供前端静态文件；不存在的路径回落到 index.html（前端路由）。
func ServeStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") {
		WriteJSON(w, 404, map[string]any{"error": errNotFound("接口")})
		return
	}
	sub, _ := fs.Sub(distFS, "dist")
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := sub.Open(p); err == nil {
		st, _ := f.Stat()
		f.Close()
		if st != nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, sub, p)
			return
		}
	}
	idx, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(200)
		w.Write([]byte("<!doctype html><meta charset=utf-8><title>Lemon Note</title><p>前端尚未构建。请在 lemon-note-console 目录执行 npm run build。</p>"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(idx)
}
