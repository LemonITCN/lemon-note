package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"

	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
)

type setupHandler struct {
	a        *App
	mu       sync.Mutex
	failures int
	lockedAt time.Time
}

func newSetupHandler(a *App) http.Handler { return &setupHandler{a: a} }

type setupError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func setupFail(w http.ResponseWriter, status int, code, msg string) {
	web.WriteJSON(w, status, map[string]any{"error": setupError{Code: code, Message: msg}})
}

func (h *setupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	switch {
	case r.URL.Path == "/api/bootstrap" && r.Method == http.MethodGet:
		web.WriteJSON(w, 200, map[string]any{"mode": "setup", "version": version.Version, "data_dir": h.a.Boot.DataDir,
			"cache_dir": h.a.Boot.CacheDir, "config_path": h.a.Boot.Path(), "engines": []string{"sqlite", "mysql", "postgres"}})
	case r.URL.Path == "/api/setup/test-db" && r.Method == http.MethodPost:
		h.testDB(w, r)
	case r.URL.Path == "/api/setup" && r.Method == http.MethodPost:
		h.setup(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		setupFail(w, 503, "setup_required", "系统尚未完成首次设置")
	default:
		web.ServeStatic(w, r)
	}
}

type setupInput struct {
	Token       string `json:"token"`
	Engine      string `json:"engine"`
	DSN         string `json:"dsn"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Locale      string `json:"locale"`
	SiteName    string `json:"site_name"`
}

func (h *setupHandler) read(w http.ResponseWriter, r *http.Request) (*setupInput, bool) {
	h.mu.Lock()
	if h.failures >= 10 && time.Since(h.lockedAt) < 10*time.Minute {
		h.mu.Unlock()
		setupFail(w, 429, "too_many_attempts", "尝试次数过多，请 10 分钟后再试")
		return nil, false
	}
	h.mu.Unlock()
	data, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		setupFail(w, 400, "bad_request", "请求体读取失败")
		return nil, false
	}
	var in setupInput
	if json.Unmarshal(data, &in) != nil {
		setupFail(w, 400, "bad_request", "请求体不是合法的 JSON")
		return nil, false
	}
	h.a.mu.Lock()
	want := h.a.setupToken
	h.a.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(in.Token), []byte(want)) != 1 {
		h.mu.Lock()
		h.failures++
		h.lockedAt = time.Now()
		h.mu.Unlock()
		setupFail(w, 403, "bad_setup_token", "设置令牌不正确。令牌显示在服务器启动日志或托盘窗口里")
		return nil, false
	}
	return &in, true
}

func (h *setupHandler) testDB(w http.ResponseWriter, r *http.Request) {
	in, ok := h.read(w, r)
	if !ok {
		return
	}
	engine := store.Engine(in.Engine)
	if engine == store.SQLite {
		web.WriteJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if !engine.Valid() {
		setupFail(w, 400, "bad_engine", "未知的数据库类型")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tmp := filepath.Join(h.a.Boot.CacheDir, "setup-test-"+ids.RandomHex(4))
	defer os.RemoveAll(tmp)
	st, err := store.Open(ctx, store.Options{Engine: engine, DSN: in.DSN, WorkPath: filepath.Join(tmp, "x.db")})
	if err != nil {
		setupFail(w, 400, "db_failed", "连接失败："+err.Error())
		return
	}
	defer st.Close()
	var n int
	_ = st.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
	if n > 0 {
		setupFail(w, 400, "db_not_empty", "该数据库里已经有数据，请使用一个空数据库")
		return
	}
	web.WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *setupHandler) setup(w http.ResponseWriter, r *http.Request) {
	in, ok := h.read(w, r)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	engine := store.Engine(in.Engine)
	if in.Engine == "" {
		engine = store.SQLite
	}
	if !engine.Valid() {
		setupFail(w, 400, "bad_engine", "未知的数据库类型")
		return
	}
	if engine != store.SQLite && strings.TrimSpace(in.DSN) == "" {
		setupFail(w, 400, "bad_dsn", "请填写数据库连接串")
		return
	}
	// 先校验管理员信息，避免初始化完才发现口令不合规
	if _, err := accounts.BuildUser(accounts.NewUser{Username: in.Username, DisplayName: in.DisplayName, Password: in.Password, Role: model.RoleAdmin, Locale: in.Locale}); err != nil {
		setupFail(w, 400, "bad_admin", err.Error())
		return
	}
	ctx := context.WithoutCancel(r.Context())
	hostID, _ := persistHostID(h.a)
	cfg := persist.Config{DataDir: h.a.Boot.DataDir, CacheDir: h.a.Boot.CacheDir, DSN: in.DSN, HostID: hostID, Version: version.Version, Logger: h.a.Log}
	if err := persist.Initialize(ctx, cfg, engine); err != nil {
		setupFail(w, 400, "init_failed", "初始化数据目录失败："+err.Error())
		return
	}
	if engine != store.SQLite {
		h.a.Boot.DSN = in.DSN
		if err := h.a.Boot.Save(); err != nil {
			setupFail(w, 500, "save_config_failed", "保存引导配置失败："+err.Error())
			return
		}
	}
	if err := h.a.openInstance(ctx); err != nil {
		setupFail(w, 500, "open_failed", "启动失败："+err.Error())
		return
	}
	svc := h.a.Services()
	u, err := svc.Accounts.CreateUser(ctx, "", accounts.NewUser{Username: in.Username, DisplayName: in.DisplayName, Password: in.Password, Role: model.RoleAdmin, Locale: in.Locale})
	if err != nil {
		setupFail(w, 500, "admin_failed", "创建管理员失败："+err.Error())
		return
	}
	sys := svc.Settings.System(ctx)
	if in.SiteName != "" {
		sys.SiteName = in.SiteName
	}
	if in.Locale == "en" {
		sys.DefaultLocale = "en"
	}
	if err := svc.Settings.SaveSystem(ctx, u.ID, sys); err != nil {
		h.a.Log.Warn("保存系统设置失败", "err", err)
	}
	h.a.mu.Lock()
	h.a.setupToken = ""
	h.a.mu.Unlock()
	web.WriteJSON(w, 200, map[string]any{"ok": true})
}

func persistHostID(a *App) (string, error) {
	return configHostID(a.Boot.CacheDir)
}
