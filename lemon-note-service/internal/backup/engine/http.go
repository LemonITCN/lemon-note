package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Mount 挂载备份接口（只给系统管理员，网页会话）。
//
//	GET    /api/admin/backup/targets                  目标列表（凭据打码）
//	POST   /api/admin/backup/targets                  新建目标
//	POST   /api/admin/backup/test                     测试未保存的配置 {id?, kind, config}
//	GET    /api/admin/backup/targets/{id}             目标详情
//	PUT    /api/admin/backup/targets/{id}             修改目标（凭据传打码值表示不改）
//	DELETE /api/admin/backup/targets/{id}             删除目标（不删目标上的备份文件）
//	POST   /api/admin/backup/targets/{id}/test        测试连接并核对加密设置
//	POST   /api/admin/backup/targets/{id}/run         立即执行 → {run_id}
//	POST   /api/admin/backup/targets/{id}/cancel      取消目标上进行中的操作
//	GET    /api/admin/backup/targets/{id}/backups     列出目标上的备份
//	POST   /api/admin/backup/targets/{id}/backups     同上，可带 {password}
//	POST   /api/admin/backup/targets/{id}/verify      校验 {backup, full_hash?, sample?, password?} → {run_id}
//	POST   /api/admin/backup/targets/{id}/restore     恢复到新目录 {backup, to, password?} → {run_id}
//	POST   /api/admin/backup/targets/{id}/baidu/device  百度网盘设备码授权：发起
//	POST   /api/admin/backup/targets/{id}/baidu/poll    百度网盘设备码授权：轮询
//	GET    /api/admin/backup/runs?target_id=&limit=   执行记录（含校验、恢复）
//	GET    /api/admin/backup/runs/{id}                一条执行记录：日志、进行中的进度、校验 / 恢复结果
//	POST   /api/admin/backup/runs/{id}/cancel         取消
func (e *Engine) Mount(ws *web.Server, r chi.Router) {
	h := ws.H
	r.Group(func(r chi.Router) {
		r.Use(ws.RequireAdmin)
		r.Get("/api/admin/backup/targets", h(e.hListTargets))
		r.Post("/api/admin/backup/targets", h(e.hCreateTarget))
		r.Post("/api/admin/backup/test", h(e.hTestConfig))
		r.Get("/api/admin/backup/targets/{id}", h(e.hGetTarget))
		r.Put("/api/admin/backup/targets/{id}", h(e.hUpdateTarget))
		r.Delete("/api/admin/backup/targets/{id}", h(e.hDeleteTarget))
		r.Post("/api/admin/backup/targets/{id}/test", h(e.hTestTarget))
		r.Post("/api/admin/backup/targets/{id}/run", h(e.hRun))
		r.Post("/api/admin/backup/targets/{id}/cancel", h(e.hCancelTarget))
		r.Get("/api/admin/backup/targets/{id}/backups", h(e.hBackups))
		r.Post("/api/admin/backup/targets/{id}/backups", h(e.hBackups))
		r.Post("/api/admin/backup/targets/{id}/verify", h(e.hVerify))
		r.Post("/api/admin/backup/targets/{id}/restore", h(e.hRestore))
		r.Post("/api/admin/backup/targets/{id}/baidu/device", h(e.hBaiduDevice))
		r.Post("/api/admin/backup/targets/{id}/baidu/poll", h(e.hBaiduPoll))
		r.Get("/api/admin/backup/runs", h(e.hRuns))
		r.Get("/api/admin/backup/runs/{id}", h(e.hRunGet))
		r.Post("/api/admin/backup/runs/{id}/cancel", h(e.hRunCancel))
	})
}

// apiErr 把引擎错误转成合适的 HTTP 状态。
func apiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBusy):
		return &web.APIError{Status: http.StatusConflict, Code: "backup_busy", Message: err.Error()}
	case errors.Is(err, ErrTargetNotFound):
		return web.NotFound("备份目标")
	case errors.Is(err, ErrWrongPassword):
		return &web.APIError{Status: http.StatusBadRequest, Code: "wrong_password", Message: err.Error()}
	case errors.Is(err, ErrNeedPassword):
		return &web.APIError{Status: http.StatusBadRequest, Code: "password_required", Message: err.Error()}
	case errors.Is(err, ErrClosed):
		return &web.APIError{Status: http.StatusServiceUnavailable, Code: "stopping", Message: err.Error()}
	}
	return err
}

func uidOf(r *http.Request) string {
	if u := web.UserOf(r); u != nil {
		return u.ID
	}
	return ""
}

// audit 写安全审计日志（失败不影响主流程）。
func (e *Engine) audit(r *http.Request, action, targetID string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	d, _ := json.Marshal(detail)
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	row := model.AuditLog{ID: ids.New(), TS: model.Now(), UserID: model.StrPtr(uidOf(r)), Action: action, IP: web.ClientIP(r), UA: ua,
		Target: targetID, Detail: string(d)}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	if _, err := e.st.Write(ctx, "system", func(tx *store.Tx) error { return tx.Put("audit_logs", row) }); err != nil {
		e.log.Warn("写审计日志失败", "err", err)
	}
}

func (e *Engine) hListTargets(w http.ResponseWriter, r *http.Request) error {
	list, err := e.ListTargets(r.Context())
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"targets": list, "masked": Masked})
}

func (e *Engine) hGetTarget(w http.ResponseWriter, r *http.Request) error {
	v, err := e.GetTarget(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apiErr(err)
	}
	return web.OK(w, v)
}

func (e *Engine) hCreateTarget(w http.ResponseWriter, r *http.Request) error {
	var in TargetInput
	if err := web.ReadJSONLimit(r, &in, 1<<20); err != nil {
		return err
	}
	v, err := e.CreateTarget(r.Context(), uidOf(r), in)
	if err != nil {
		return apiErr(err)
	}
	e.audit(r, "backup.target_create", v.ID, map[string]any{"name": v.Name, "kind": v.Kind, "mode": v.Mode, "encrypted": v.Encryption.Enabled})
	return web.OK(w, v)
}

func (e *Engine) hUpdateTarget(w http.ResponseWriter, r *http.Request) error {
	var in TargetInput
	if err := web.ReadJSONLimit(r, &in, 1<<20); err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	v, err := e.UpdateTarget(r.Context(), uidOf(r), id, in)
	if err != nil {
		return apiErr(err)
	}
	detail := map[string]any{"name": v.Name, "kind": v.Kind, "mode": v.Mode, "encrypted": v.Encryption.Enabled, "enabled": v.Enabled}
	if in.Encryption != nil && in.Encryption.Password != "" && in.Encryption.Password != Masked {
		detail["password_changed"] = true
	}
	e.audit(r, "backup.target_update", id, detail)
	return web.OK(w, v)
}

func (e *Engine) hDeleteTarget(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	v, _ := e.GetTarget(r.Context(), id)
	if err := e.DeleteTarget(r.Context(), uidOf(r), id); err != nil {
		return apiErr(err)
	}
	detail := map[string]any{}
	if v != nil {
		detail["name"] = v.Name
	}
	e.audit(r, "backup.target_delete", id, detail)
	return web.OK(w, nil)
}

func (e *Engine) hTestTarget(w http.ResponseWriter, r *http.Request) error {
	res, err := e.TestTarget(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apiErr(err)
	}
	return web.OK(w, res)
}

func (e *Engine) hTestConfig(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ID     string          `json:"id"`
		Kind   string          `json:"kind"`
		Config json.RawMessage `json:"config"`
	}
	if err := web.ReadJSONLimit(r, &in, 1<<20); err != nil {
		return err
	}
	if len(in.Config) == 0 {
		return web.Bad("请填写目标配置")
	}
	res, err := e.TestConfig(r.Context(), in.ID, in.Kind, in.Config)
	if err != nil {
		return apiErr(err)
	}
	return web.OK(w, res)
}

func (e *Engine) hRun(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	runID, err := e.StartBackup(r.Context(), id, RunOptions{Trigger: triggerManual, Actor: uidOf(r)})
	if err != nil {
		return apiErr(err)
	}
	e.audit(r, "backup.run", id, map[string]any{"run_id": runID})
	return web.OK(w, map[string]any{"run_id": runID})
}

func (e *Engine) hCancelTarget(w http.ResponseWriter, r *http.Request) error {
	if !e.Cancel(chi.URLParam(r, "id")) {
		return web.Bad("这个目标上没有进行中的操作")
	}
	return web.OK(w, nil)
}

func (e *Engine) hRunCancel(w http.ResponseWriter, r *http.Request) error {
	if !e.Cancel(chi.URLParam(r, "id")) {
		return web.Bad("这条记录不在进行中")
	}
	return web.OK(w, nil)
}

type passwordInput struct {
	Password string `json:"password"`
}

func (e *Engine) hBackups(w http.ResponseWriter, r *http.Request) error {
	var in passwordInput
	if r.Method == http.MethodPost && r.ContentLength != 0 {
		if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	list, err := e.ListTargetBackups(ctx, chi.URLParam(r, "id"), in.Password)
	if err != nil {
		return apiErr(err)
	}
	return web.OK(w, map[string]any{"backups": list})
}

func (e *Engine) hVerify(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Backup   string `json:"backup"`
		FullHash bool   `json:"full_hash"`
		Sample   int    `json:"sample"`
		Password string `json:"password"`
	}
	if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	if strings.TrimSpace(in.Backup) == "" {
		return web.Bad("请选择要校验的备份")
	}
	id := chi.URLParam(r, "id")
	runID, err := e.StartVerify(r.Context(), id, strings.TrimSpace(in.Backup), VerifyOptions{FullHash: in.FullHash, Sample: in.Sample},
		in.Password, RunOptions{Trigger: triggerManual, Actor: uidOf(r)})
	if err != nil {
		return apiErr(err)
	}
	e.audit(r, "backup.verify", id, map[string]any{"run_id": runID, "backup": in.Backup})
	return web.OK(w, map[string]any{"run_id": runID})
}

func (e *Engine) hRestore(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Backup   string `json:"backup"`
		To       string `json:"to"`
		Password string `json:"password"`
	}
	if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	if strings.TrimSpace(in.Backup) == "" {
		return web.Bad("请选择要恢复的备份")
	}
	id := chi.URLParam(r, "id")
	runID, err := e.StartRestore(r.Context(), id, strings.TrimSpace(in.Backup), in.To, in.Password,
		RunOptions{Trigger: triggerManual, Actor: uidOf(r)})
	if err != nil {
		return apiErr(err)
	}
	e.audit(r, "backup.restore", id, map[string]any{"run_id": runID, "backup": in.Backup, "to": in.To})
	return web.OK(w, map[string]any{"run_id": runID})
}

func (e *Engine) hBaiduDevice(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	v, err := e.BaiduDevice(ctx, chi.URLParam(r, "id"))
	if err != nil {
		return apiErr(err)
	}
	return web.OK(w, v)
}

func (e *Engine) hBaiduPoll(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	id := chi.URLParam(r, "id")
	v, err := e.BaiduPoll(ctx, id)
	if err != nil {
		return apiErr(err)
	}
	if v.Status == "authorized" {
		e.audit(r, "backup.baidu_authorize", id, nil)
	}
	return web.OK(w, v)
}

func (e *Engine) hRuns(w http.ResponseWriter, r *http.Request) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := e.Runs(r.Context(), r.URL.Query().Get("target_id"), limit)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"runs": list})
}

func (e *Engine) hRunGet(w http.ResponseWriter, r *http.Request) error {
	v, err := e.Run(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return apiErr(err)
	}
	return web.OK(w, v)
}
