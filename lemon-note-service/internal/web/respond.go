package web

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// APIError 是返回给前端的错误。Code 是稳定的英文标识，前端据此翻译；Message 是中文说明。
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Extra   any    `json:"extra,omitempty"`
}

func (e *APIError) Error() string { return e.Message }

func errBad(msg string) *APIError { return &APIError{Status: 400, Code: "bad_request", Message: msg} }
func errUnauth() *APIError {
	return &APIError{Status: 401, Code: "unauthorized", Message: "请先登录"}
}
func errForbidden() *APIError {
	return &APIError{Status: 403, Code: "forbidden", Message: "没有权限"}
}
func errNotFound(what string) *APIError {
	return &APIError{Status: 404, Code: "not_found", Message: what + "不存在"}
}
func errConflict(msg string) *APIError { return &APIError{Status: 409, Code: "conflict", Message: msg} }

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (s *Server) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.writeError(w, r, err)
		}
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *APIError
	var locked *auth.ErrLocked
	switch {
	case errors.As(err, &ae):
	case errors.As(err, &locked):
		ae = &APIError{Status: 429, Code: "too_many_attempts", Message: err.Error(), Extra: map[string]any{"retry_after": int(locked.RetryAfter.Seconds()) + 1}}
	case errors.Is(err, auth.ErrBadCredentials):
		ae = &APIError{Status: 401, Code: "bad_credentials", Message: err.Error()}
	case errors.Is(err, auth.ErrDisabled):
		ae = &APIError{Status: 403, Code: "account_disabled", Message: err.Error()}
	case errors.Is(err, auth.ErrSessionInvalid):
		ae = &APIError{Status: 401, Code: "session_invalid", Message: err.Error()}
	case errors.Is(err, pages.ErrNotFound):
		ae = errNotFound("页面")
	case errors.Is(err, pages.ErrLocked):
		ae = &APIError{Status: 423, Code: "page_locked", Message: err.Error()}
	case errors.Is(err, pages.ErrCycle):
		ae = errBad(err.Error())
	case errors.Is(err, store.ErrReadOnly):
		ae = &APIError{Status: 503, Code: "read_only", Message: err.Error()}
	case errors.Is(err, errBodyTooLarge):
		ae = &APIError{Status: 413, Code: "too_large", Message: "请求内容太大"}
	default:
		if isUserFacing(err) {
			ae = errBad(err.Error())
		} else {
			s.log.Error("请求处理失败", "method", r.Method, "path", r.URL.Path, "err", err)
			ae = &APIError{Status: 500, Code: "internal", Message: "服务器内部错误"}
		}
	}
	writeJSON(w, ae.Status, map[string]any{"error": ae})
}

// userFacingError 标记可以直接把内容展示给用户的业务错误。
type userFacingError interface{ UserFacing() bool }

// isUserFacing 业务层返回的中文错误直接展示；数据库、IO 等底层错误不外泄。
func isUserFacing(err error) bool {
	var uf userFacingError
	if errors.As(err, &uf) {
		return uf.UserFacing()
	}
	msg := err.Error()
	for _, bad := range []string{"sql", "SQL", "database", "driver", "dial", "open ", "no such", "syntax", "constraint", "connection", "/", "\\"} {
		if strings.Contains(msg, bad) {
			return false
		}
	}
	for _, r := range msg {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// WriteJSON 写 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		slog.Debug("写响应失败", "err", err)
	}
}

func ok(w http.ResponseWriter, v any) error {
	if v == nil {
		v = map[string]any{"ok": true}
	}
	writeJSON(w, 200, v)
	return nil
}

var errBodyTooLarge = errors.New("body too large")

// readJSON 读请求体 JSON（默认上限 16 MB）。
func readJSON(r *http.Request, dst any) error {
	return readJSONLimit(r, dst, 16<<20)
}

func readJSONLimit(r *http.Request, dst any, limit int64) error {
	body := io.LimitReader(r.Body, limit+1)
	data, err := io.ReadAll(body)
	if err != nil {
		return errBad("请求体读取失败")
	}
	if int64(len(data)) > limit {
		return errBodyTooLarge
	}
	if len(data) == 0 {
		return errBad("请求体为空")
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return errBad("请求体不是合法的 JSON")
	}
	return nil
}

// ---------- 给其他模块用的导出 ----------

// H 把返回 error 的处理函数转成 http.HandlerFunc。
func (s *Server) H(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return s.h(fn)
}

// OK 写成功响应。
func OK(w http.ResponseWriter, v any) error { return ok(w, v) }

// ReadJSON 读请求体。
func ReadJSON(r *http.Request, dst any) error { return readJSON(r, dst) }

// ReadJSONLimit 读请求体（指定上限）。
func ReadJSONLimit(r *http.Request, dst any, limit int64) error { return readJSONLimit(r, dst, limit) }

// Bad 参数错误。
func Bad(msg string) error { return errBad(msg) }

// NotFound 不存在。
func NotFound(what string) error { return errNotFound(what) }

// Forbidden 没有权限。
func Forbidden() error { return errForbidden() }
