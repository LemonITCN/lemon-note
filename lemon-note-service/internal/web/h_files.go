package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/go-chi/chi/v5"
)

func (s *Server) routesFiles(r chi.Router) {
	r.Post("/api/files", s.h(s.handleUpload))
	r.Get("/api/files/{id}", s.h(s.handleDownload))
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) error {
	max := int64(s.Settings.System(r.Context()).MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, max+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return errBad("需要 multipart 上传")
	}
	var spaceID, pageID string
	for {
		part, err := mr.NextPart()
		if err != nil {
			return errBad("没有收到文件")
		}
		switch part.FormName() {
		case "space_id":
			b := make([]byte, 64)
			n, _ := part.Read(b)
			spaceID = strings.TrimSpace(string(b[:n]))
		case "page_id":
			b := make([]byte, 64)
			n, _ := part.Read(b)
			pageID = strings.TrimSpace(string(b[:n]))
		case "file":
			if pageID != "" {
				meta, err := s.NeedPage(r, pageID, model.RoleEditor)
				if err != nil {
					return err
				}
				spaceID = meta.SpaceID
			} else {
				if spaceID == "" {
					if sp := s.Cat.VisibleSpaces(UserOf(r).ID); len(sp) > 0 {
						spaceID = sp[0].ID
					}
				}
				if !model.RoleAtLeast(s.SpaceRole(r, spaceID), model.RoleEditor) {
					return errForbidden()
				}
			}
			f, err := s.Files.Save(r.Context(), UserOf(r).ID, spaceID, pageID, part.FileName(), part, max)
			if err != nil {
				if strings.Contains(err.Error(), "大小上限") || strings.Contains(err.Error(), "too large") {
					return &APIError{Status: 413, Code: "too_large", Message: fmt.Sprintf("文件超过上限 %d MB", max>>20)}
				}
				return err
			}
			return ok(w, map[string]any{"file": f})
		}
	}
}

// canReadFile 判断当前登录用户能否读取附件（公开分享页走 /api/public 下的接口）。
func (s *Server) canReadFile(r *http.Request, f *model.File) bool {
	if f.DeletedAt != nil {
		return false
	}
	if UserOf(r) != nil {
		if f.PageID != nil {
			if s.PageRole(r, *f.PageID) != model.RoleNone {
				return true
			}
		}
		if s.SpaceRole(r, f.SpaceID) != model.RoleNone {
			return true
		}
	}
	return false
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) error {
	f, err := s.Files.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !s.canReadFile(r, f) {
		return errNotFound("文件")
	}
	return s.ServeFile(w, r, f)
}

// ServeFile 输出附件内容（调用方负责权限判断）。不能内联显示的类型一律下载，并加沙箱 CSP。
func (s *Server) ServeFile(w http.ResponseWriter, r *http.Request, f *model.File) error {
	fh, err := s.Files.Open(f)
	if err != nil {
		return errNotFound("文件")
	}
	defer fh.Close()
	h := w.Header()
	disp := "inline"
	ct := f.Mime
	if !files.InlineSafe(ct) || r.URL.Query().Get("download") == "1" {
		disp = "attachment"
		if !files.InlineSafe(ct) {
			ct = "application/octet-stream"
		}
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disp, url.PathEscape(f.Name)))
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; media-src 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.UnixMilli(f.CreatedAt), fh)
	return nil
}
