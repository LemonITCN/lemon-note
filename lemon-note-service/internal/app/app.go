// Package app 组装全部服务并管理生命周期，命令行版与带界面版共用。
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"encoding/json"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/agent"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/engine"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/comments"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/exporter"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/importer"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/mcpserver"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/oauth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/realtime"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/search"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/share"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	_ "github.com/LemonITCN/lemon-note/lemon-note-service/internal/whiteboard" // 注册白板工具
)

// State 运行状态。
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateSetup    State = "setup"
	StateRunning  State = "running"
	StateError    State = "error"
)

// Status 给界面（托盘窗口、命令行）看的状态。
type Status struct {
	State      State  `json:"state"`
	Error      string `json:"error,omitempty"`
	URL        string `json:"url"`
	SetupURL   string `json:"setup_url,omitempty"`
	DataDir    string `json:"data_dir"`
	CacheDir   string `json:"cache_dir"`
	ConfigPath string `json:"config_path"`
	Version    string `json:"version"`
	StartedAt  int64  `json:"started_at,omitempty"`
}

// App 是一个服务实例。
type App struct {
	Boot     *config.Bootstrap
	Mode     config.Mode
	Log      *slog.Logger
	TakeOver bool

	mu         sync.Mutex
	state      State
	err        error
	startedAt  time.Time
	setupToken string
	srv        *http.Server
	ln         net.Listener
	handler    atomic.Pointer[handlerBox]
	svc        *Services
	stopJobs   context.CancelFunc
	jobsWG     sync.WaitGroup
	// OnStateChange 状态变化回调（带界面版用）
	OnStateChange func(Status)
}

// Services 是运行中的全部服务。
type Services struct {
	Inst     *persist.Instance
	Cat      *catalog.Catalog
	Settings *settings.Service
	Auth     *auth.Service
	Pages    *pages.Service
	Accounts *accounts.Service
	Hub      *realtime.Hub
	Box      *crypt.Box
	Files    *files.Service
	Search   *search.Index
	Tools    *tools.Services
	DBs      *dbs.Service
	Share    *share.Service
	Comments *comments.Service
	OAuth    *oauth.Service
	Agent    *agent.Service
	Backup   *engine.Engine
	Importer *importer.Service
	Web      *web.Server
}

// Extension 让后续模块（智能体、数据库、备份……）在服务启动时挂进来。
type Extension func(a *App, s *Services) error

var extensions []Extension

// RegisterExtension 注册扩展（在 init 里调用）。
func RegisterExtension(e Extension) { extensions = append(extensions, e) }

type handlerBox struct{ h http.Handler }

func (a *App) setHandler(h http.Handler) { a.handler.Store(&handlerBox{h: h}) }

func New(boot *config.Bootstrap, mode config.Mode, log *slog.Logger) *App {
	a := &App{Boot: boot, Mode: mode, Log: log, state: StateStopped}
	a.setHandler(http.NotFoundHandler())
	return a
}

func (a *App) setState(st State, err error) {
	a.mu.Lock()
	a.state, a.err = st, err
	a.mu.Unlock()
	if a.OnStateChange != nil {
		a.OnStateChange(a.Status())
	}
}

// Status 返回当前状态。
func (a *App) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Status{State: a.state, URL: a.baseURL(), DataDir: a.Boot.DataDir, CacheDir: a.Boot.CacheDir, ConfigPath: a.Boot.Path(), Version: version.Version}
	if a.err != nil {
		s.Error = a.err.Error()
	}
	if a.state == StateSetup {
		s.SetupURL = s.URL + "/setup?token=" + a.setupToken
	}
	if !a.startedAt.IsZero() {
		s.StartedAt = a.startedAt.UnixMilli()
	}
	return s
}

func (a *App) baseURL() string {
	if a.Boot.BaseURL != "" {
		return strings.TrimRight(a.Boot.BaseURL, "/")
	}
	host, port, err := net.SplitHostPort(a.Boot.Listen)
	if err != nil {
		return "http://127.0.0.1:" + fmt.Sprint(config.DefaultPort)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if a.Boot.TLS.Mode != "off" && a.Boot.TLS.Mode != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// ServeHTTP 把请求交给当前模式的处理器（设置模式 / 正常模式）。
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.Load().h.ServeHTTP(w, r)
}

// Start 监听端口并启动服务；数据目录未初始化时进入首次设置模式。
func (a *App) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.state == StateRunning || a.state == StateSetup || a.state == StateStarting {
		a.mu.Unlock()
		return nil
	}
	a.state = StateStarting
	a.mu.Unlock()
	ln, err := net.Listen("tcp", a.Boot.Listen)
	if err != nil {
		err = fmt.Errorf("无法监听 %s（端口可能被占用）: %w", a.Boot.Listen, err)
		a.setState(StateError, err)
		return err
	}
	tlsCfg, err := a.tlsConfig()
	if err != nil {
		ln.Close()
		a.setState(StateError, err)
		return err
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	a.ln = ln
	a.srv = &http.Server{Handler: a, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
		ErrorLog: slog.NewLogLogger(a.Log.Handler(), slog.LevelDebug)}
	go func() {
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.Log.Error("HTTP 服务异常退出", "err", err)
			a.setState(StateError, err)
		}
	}()
	if err := a.openInstance(ctx); err != nil {
		if errors.Is(err, persist.ErrNeedSetup) {
			a.enterSetup()
			return nil
		}
		a.shutdownHTTP(ctx)
		a.setState(StateError, err)
		return err
	}
	return nil
}

func (a *App) enterSetup() {
	a.mu.Lock()
	a.setupToken = ids.RandomToken(24)
	a.mu.Unlock()
	a.setHandler(newSetupHandler(a))
	a.setState(StateSetup, nil)
	a.Log.Info("数据目录尚未初始化，请在浏览器里完成首次设置", "url", a.Status().SetupURL, "data_dir", a.Boot.DataDir)
}

func (a *App) openInstance(ctx context.Context) error {
	hostID, err := config.HostID(a.Boot.CacheDir)
	if err != nil {
		return fmt.Errorf("缓存目录不可写: %w", err)
	}
	inst, err := persist.Open(ctx, persist.Config{DataDir: a.Boot.DataDir, CacheDir: a.Boot.CacheDir, DSN: a.Boot.DSN,
		HostID: hostID, Version: version.Version, TakeOver: a.TakeOver, Logger: a.Log})
	if err != nil {
		return err
	}
	svc, err := a.buildServices(ctx, inst)
	if err != nil {
		inst.Close(context.Background())
		return err
	}
	a.mu.Lock()
	a.svc = svc
	a.startedAt = time.Now()
	a.mu.Unlock()
	a.setHandler(svc.Web.Handler())
	jctx, cancel := context.WithCancel(context.Background())
	a.stopJobs = cancel
	a.startJobs(jctx, svc)
	a.setState(StateRunning, nil)
	a.Log.Info("Lemon Note 已启动", "url", a.baseURL(), "data_dir", a.Boot.DataDir, "engine", inst.Info.Engine)
	return nil
}

func (a *App) buildServices(ctx context.Context, inst *persist.Instance) (*Services, error) {
	key, err := inst.SecretKey()
	if err != nil {
		return nil, fmt.Errorf("读取 config/secret.key 失败: %w", err)
	}
	box, err := crypt.New(key)
	if err != nil {
		return nil, err
	}
	st := inst.Store
	cat, err := catalog.Load(ctx, st)
	if err != nil {
		return nil, err
	}
	set := settings.New(st)
	svc := &Services{Inst: inst, Cat: cat, Settings: set, Auth: auth.New(st, set), Pages: pages.New(st, cat),
		Accounts: accounts.New(st, cat), Hub: realtime.New(cat, st, a.Log), Box: box}
	svc.Files = files.New(st, inst.Dir, inst.CacheDir)
	ix, err := search.Open(ctx, inst.CacheDir, st, a.Log)
	if err != nil {
		return nil, fmt.Errorf("打开全文索引失败: %w", err)
	}
	svc.Search = ix
	ix.Text = func(data string) string {
		var n lnmd.Node
		if json.Unmarshal([]byte(data), &n) != nil {
			return search.PlainFromJSON(data)
		}
		return lnmd.PlainText(n)
	}
	svc.Web = web.NewServer(web.Deps{Log: a.Log, Boot: a.Boot, Inst: inst, Store: st, Cat: cat, Auth: svc.Auth, Settings: set,
		Pages: svc.Pages, Accounts: svc.Accounts, Hub: svc.Hub, Box: box, Files: svc.Files, Search: ix, Started: time.Now()})
	svc.DBs = dbs.New(st, cat, svc.Pages)
	svc.Tools = &tools.Services{Store: st, Cat: cat, Pages: svc.Pages, Search: ix, Files: svc.Files, Settings: set, Box: box, Log: a.Log, DB: svc.DBs}
	svc.Web.AddMount(svc.DBs.Mount)
	svc.Comments = comments.New(st, cat)
	svc.Tools.Comments = svc.Comments
	svc.Web.AddMount(svc.Comments.Mount)
	svc.Share = share.New(st, cat, svc.Pages, svc.DBs, svc.Files, set, box, svc.Hub)
	svc.Web.AddMount(svc.Share.Mount)
	svc.Web.Extra = append(svc.Web.Extra, svc.Share.Public(svc.Web))
	svc.Agent = agent.New(st, cat, svc.Pages, set, box, svc.Hub, a.Log, svc.Tools)
	svc.Web.AddMount(svc.Agent.Mount)
	svc.Importer = importer.New(st, cat, svc.Pages, svc.DBs, svc.Files, set, inst.CacheDir, a.Log)
	svc.Web.AddMount(svc.Importer.Mount)
	svc.Web.AddMount(exporter.New(cat, svc.Pages, svc.DBs, svc.Files, a.Log).Mount)
	if err := a.buildBackup(svc); err != nil {
		return nil, fmt.Errorf("创建备份引擎失败: %w", err)
	}
	svc.OAuth = oauth.New(st, cat, svc.Web, a.publicBase)
	svc.Web.OAuthLookup = svc.OAuth.Lookup
	svc.Web.AddMount(svc.OAuth.Mount)
	svc.Web.Extra = append(svc.Web.Extra, svc.OAuth.Public)
	svc.Web.MCP = mcpserver.Handler(svc.Tools, svc.Web.BearerAuth, func(r *http.Request) string { return a.publicBase(r) + "/.well-known/oauth-protected-resource" })
	for _, e := range extensions {
		if err := e(a, svc); err != nil {
			return nil, err
		}
	}
	return svc, nil
}

// Services 返回运行中的服务（未运行时为 nil）。
func (a *App) Services() *Services {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.svc
}

func (a *App) shutdownHTTP(ctx context.Context) {
	if a.srv != nil {
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = a.srv.Shutdown(sctx)
		cancel()
		a.srv = nil
	}
}

// Stop 停止服务：先停 HTTP，再停后台任务，最后封存日志、释放数据目录。
func (a *App) Stop(ctx context.Context) error {
	a.mu.Lock()
	st := a.state
	a.mu.Unlock()
	if st == StateStopped {
		return nil
	}
	a.shutdownHTTP(ctx)
	if a.stopJobs != nil {
		a.stopJobs()
		a.jobsWG.Wait()
		a.stopJobs = nil
	}
	var err error
	a.mu.Lock()
	svc := a.svc
	a.svc = nil
	a.startedAt = time.Time{}
	a.mu.Unlock()
	if svc != nil {
		if svc.Agent != nil {
			svc.Agent.StopAll()
		}
		if svc.Importer != nil {
			svc.Importer.Stop()
		}
		for _, fn := range stopHooks {
			fn(svc)
		}
		if svc.Search != nil {
			svc.Search.Close()
		}
		err = svc.Inst.Close(context.Background())
	}
	a.setHandler(http.NotFoundHandler())
	a.setState(StateStopped, nil)
	a.Log.Info("Lemon Note 已停止")
	return err
}

var stopHooks []func(*Services)

// RegisterStopHook 注册停止时的清理回调（例如中断运行中的智能体）。
func RegisterStopHook(fn func(*Services)) { stopHooks = append(stopHooks, fn) }

func (a *App) tlsConfig() (*tls.Config, error) {
	switch a.Boot.TLS.Mode {
	case "", "off":
		return nil, nil
	case "file":
		cert, err := tls.LoadX509KeyPair(a.Boot.TLS.CertFile, a.Boot.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("加载 TLS 证书失败: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
	case "acme":
		return acmeTLS(a.Boot, filepath.Join(a.Boot.CacheDir, "acme"))
	}
	return nil, fmt.Errorf("未知的 TLS 模式 %q", a.Boot.TLS.Mode)
}

// LogDir 返回日志目录。
func LogDir(cacheDir string) string { return filepath.Join(cacheDir, "logs") }

// EnsureDirs 确保缓存目录存在。
func EnsureDirs(b *config.Bootstrap) error {
	return os.MkdirAll(b.CacheDir, 0o700)
}

// publicBase 返回对外访问地址（优先引导配置里的 base_url，否则按请求推断）。
func (a *App) publicBase(r *http.Request) string {
	if a.Boot.BaseURL != "" {
		return strings.TrimRight(a.Boot.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
