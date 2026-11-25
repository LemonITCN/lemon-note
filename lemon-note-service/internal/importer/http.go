package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Mount 挂载导入接口（都在需要登录的路由下）。
//
//	POST /api/import              multipart：kind、space_id、parent_id、file（文件放最后）
//	POST /api/import/notion-api   JSON：token、root_page_id、space_id、parent_id
//	GET  /api/import/{id}         查进度与报告（只能看自己的任务）
func (s *Service) Mount(ws *web.Server, r chi.Router) {
	r.Post("/api/import", ws.H(func(w http.ResponseWriter, r *http.Request) error { return s.hUpload(ws, w, r) }))
	r.Post("/api/import/notion-api", ws.H(func(w http.ResponseWriter, r *http.Request) error { return s.hNotionAPI(ws, w, r) }))
	r.Get("/api/import/{id}", ws.H(func(w http.ResponseWriter, r *http.Request) error {
		v, err := s.Get(r.Context(), web.UserOf(r).ID, chi.URLParam(r, "id"))
		if err != nil {
			return web.NotFound("导入任务")
		}
		return web.OK(w, map[string]any{"job": v})
	}))
}

// target 校验目标位置并要求编辑权限。
func (s *Service) target(ws *web.Server, r *http.Request, spaceID, parentID string) (Target, error) {
	if parentID != "" {
		m, err := ws.NeedPage(r, parentID, model.RoleEditor)
		if err != nil {
			return Target{}, err
		}
		if m.Kind == model.KindDatabase {
			return Target{}, web.Bad("不能导入到数据库里，请选择普通页面")
		}
		if m.TrashedAt != 0 {
			return Target{}, web.Bad("目标页面在回收站里")
		}
		return Target{SpaceID: m.SpaceID, ParentID: parentID}, nil
	}
	if spaceID == "" {
		return Target{}, web.Bad("请选择导入到哪个空间")
	}
	if !model.RoleAtLeast(ws.SpaceRole(r, spaceID), model.RoleEditor) {
		return Target{}, web.Forbidden()
	}
	return Target{SpaceID: spaceID}, nil
}

func readField(p io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(p, 256))
	return strings.TrimSpace(string(b))
}

func (s *Service) hUpload(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	max := s.Limits.MaxPackageBytes
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return web.Bad("需要 multipart 上传")
	}
	var kind, spaceID, parentID string
	for {
		part, err := mr.NextPart()
		if err != nil {
			return web.Bad("没有收到文件")
		}
		switch part.FormName() {
		case "kind":
			kind = readField(part)
		case "space_id":
			spaceID = readField(part)
		case "parent_id":
			parentID = readField(part)
		case "file":
			if !ValidFileKind(kind) {
				return web.Bad("不支持的导入来源")
			}
			tgt, err := s.target(ws, r, spaceID, parentID)
			if err != nil {
				return err
			}
			name := part.FileName()
			tmp, err := s.TempFile()
			if err != nil {
				return err
			}
			n, err := io.Copy(tmp, io.LimitReader(part, max+1))
			cerr := tmp.Close()
			if err == nil {
				err = cerr
			}
			if err != nil || n > max {
				os.Remove(tmp.Name())
				var mbe *http.MaxBytesError
				if n > max || errors.As(err, &mbe) {
					return &web.APIError{Status: 413, Code: "too_large", Message: fmt.Sprintf("导入包超过上限 %d MB", max>>20)}
				}
				return web.Bad("上传中断，请重试")
			}
			if n == 0 {
				os.Remove(tmp.Name())
				return web.Bad("上传的文件是空的")
			}
			v, err := s.StartFile(r.Context(), web.UserOf(r).ID, kind, tgt, tmp.Name(), name)
			if err != nil {
				os.Remove(tmp.Name())
				if errors.Is(err, ErrBusy) || errors.Is(err, ErrServerBusy) {
					return &web.APIError{Status: 409, Code: "import_busy", Message: err.Error()}
				}
				return err
			}
			return web.OK(w, map[string]any{"job": v})
		}
	}
}

func (s *Service) hNotionAPI(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token      string `json:"token"`
		RootPageID string `json:"root_page_id"`
		SpaceID    string `json:"space_id"`
		ParentID   string `json:"parent_id"`
	}
	if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	in.Token = strings.TrimSpace(in.Token)
	if in.Token == "" || len(in.Token) > 300 || strings.ContainsAny(in.Token, " \t\r\n") {
		return web.Bad("请填写有效的 Notion 集成密钥")
	}
	root := strings.TrimSpace(in.RootPageID)
	if root != "" {
		if root = hashIn(root); root == "" {
			return web.Bad("根页面 id 格式不正确（可以直接粘贴页面链接）")
		}
	}
	tgt, err := s.target(ws, r, in.SpaceID, in.ParentID)
	if err != nil {
		return err
	}
	v, err := s.StartNotionAPI(r.Context(), web.UserOf(r).ID, tgt, in.Token, root)
	if err != nil {
		if errors.Is(err, ErrBusy) || errors.Is(err, ErrServerBusy) {
			return &web.APIError{Status: 409, Code: "import_busy", Message: err.Error()}
		}
		return err
	}
	return web.OK(w, map[string]any{"job": v})
}

// StartFile 用上传的导入包启动任务。tmpPath 交给任务，任务结束后删除。
func (s *Service) StartFile(ctx context.Context, userID, kind string, tgt Target, tmpPath, name string) (*JobView, error) {
	if !ValidFileKind(kind) {
		return nil, fail("不支持的导入来源")
	}
	j, err := s.newJob(ctx, userID, kind)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "import"
	}
	// 单个文件按来源补上扩展名
	if !isZip(tmpPath) {
		ext := strings.ToLower(path.Ext(name))
		switch kind {
		case KindCSV:
			if ext != ".csv" {
				name += ".csv"
			}
		case KindMarkdown, KindObsidian, KindNotionZip:
			if ext != ".md" && ext != ".markdown" {
				name += ".md"
			}
		}
	}
	s.start(j, userID, tgt, func() { os.Remove(tmpPath) }, func(ctx context.Context, b *builder) error {
		v, err := openPackage(tmpPath, name, s.Limits, s.tmpDir, j.warn, kind == KindNotionZip)
		if err != nil {
			return err
		}
		defer v.Close()
		if kind == KindNotionZip {
			return s.notionZipWork(v)(ctx, b)
		}
		return s.vaultWork(v, kind, name)(ctx, b)
	})
	v := j.view()
	return &v, nil
}

// StartNotionAPI 用 Notion 集成密钥启动任务。密钥只在任务内存里用，不落盘、不进日志与报告。
func (s *Service) StartNotionAPI(ctx context.Context, userID string, tgt Target, token, rootID string) (*JobView, error) {
	j, err := s.newJob(ctx, userID, KindNotionAPI)
	if err != nil {
		return nil, err
	}
	s.start(j, userID, tgt, nil, s.notionAPIWork(token, rootID))
	v := j.view()
	return &v, nil
}
