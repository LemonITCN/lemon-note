//go:build gui

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/app"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// controller 管理进程内的服务实例：启动、停止、换数据目录、开机自启，并把状态推给托盘与状态窗口。
type controller struct {
	configPath string // 命令行 --config 指定的引导配置文件；空表示默认位置
	log        *slog.Logger
	logDir     string

	wapp     *application.App
	window   *application.WebviewWindow
	onChange func(viewStatus) // 托盘刷新（只在界面主线程之外调用）

	opMu sync.Mutex // 串行化启动 / 停止 / 换目录

	mu             sync.Mutex
	app            *app.App
	bootErr        error  // 引导配置读不出来
	busy           string // starting / stopping / switching
	lastErr        string // 最近一次操作失败的原因（不属于服务自身状态的错误）
	ownedElsewhere bool   // 数据目录被另一台机器占用，可以强制接管
	autostart      bool
	autostartErr   string
	gen            uint64
	changed        chan struct{}

	quitting atomic.Bool
}

// viewStatus 是给状态窗口与托盘看的完整状态。
type viewStatus struct {
	app.Status
	Busy           string `json:"busy,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	OwnedElsewhere bool   `json:"owned_elsewhere,omitempty"`
	LogDir         string `json:"log_dir"`
	Autostart      bool   `json:"autostart"`
	AutostartError string `json:"autostart_error,omitempty"`
	DataDirLocked  bool   `json:"data_dir_locked,omitempty"` // 数据目录由环境变量指定，界面里改不了
	Commit         string `json:"commit,omitempty"`
	BuildDate      string `json:"build_date,omitempty"`
	Lang           string `json:"lang"`
	Gen            uint64 `json:"gen"`
}

func newController(configPath string, log *slog.Logger, cacheDir string) *controller {
	return &controller{configPath: configPath, log: log, logDir: app.LogDir(cacheDir), changed: make(chan struct{})}
}

func (c *controller) effectiveConfigPath() string {
	if c.configPath != "" {
		return c.configPath
	}
	return config.DefaultConfigPath(config.UserMode)
}

// notify 状态有变化：唤醒等待中的状态请求，并刷新托盘。
func (c *controller) notify() {
	c.mu.Lock()
	c.gen++
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
	if c.quitting.Load() || c.onChange == nil {
		return
	}
	c.onChange(c.status())
}

func (c *controller) status() viewStatus {
	c.mu.Lock()
	a := c.app
	v := viewStatus{Busy: c.busy, LastError: c.lastErr, OwnedElsewhere: c.ownedElsewhere, LogDir: c.logDir,
		Autostart: c.autostart, AutostartError: c.autostartErr, Gen: c.gen, Commit: version.Commit, BuildDate: version.Date,
		DataDirLocked: os.Getenv("LEMON_NOTE_DATA_DIR") != ""}
	bootErr := c.bootErr
	c.mu.Unlock()
	if a != nil {
		v.Status = a.Status()
	} else {
		v.Status = app.Status{State: app.StateStopped, ConfigPath: c.effectiveConfigPath(), Version: version.Version,
			DataDir: config.DefaultDataDir(config.UserMode), CacheDir: config.DefaultCacheDir(config.UserMode)}
	}
	if bootErr != nil {
		v.State = app.StateError
		v.Error = bootErr.Error()
	}
	return v
}

// waitStatus 等到状态版本号不等于 since（最多 timeout），用来给状态窗口做长轮询。
func (c *controller) waitStatus(ctx context.Context, since uint64, timeout time.Duration) viewStatus {
	c.mu.Lock()
	gen, ch := c.gen, c.changed
	c.mu.Unlock()
	if gen == since {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
		}
	}
	return c.status()
}

func (c *controller) setBusy(b string) {
	c.mu.Lock()
	c.busy = b
	c.mu.Unlock()
	c.notify()
}

func (c *controller) setLastErr(err error) {
	c.mu.Lock()
	if err != nil {
		c.lastErr = err.Error()
	} else {
		c.lastErr = ""
	}
	c.mu.Unlock()
	c.notify()
}

func (c *controller) current() *app.App {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.app
}

func (c *controller) start(takeOver bool) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.startLocked(takeOver)
}

// startLocked 每次都重新读取引导配置并新建服务实例，这样改了配置文件或数据目录之后点「启动」就能生效。
func (c *controller) startLocked(takeOver bool) {
	if c.quitting.Load() {
		return
	}
	if a := c.current(); a != nil {
		switch a.Status().State {
		case app.StateRunning, app.StateSetup, app.StateStarting:
			return
		}
	}
	c.setBusy("starting")
	defer c.setBusy("")
	boot, err := config.Load(c.configPath, config.UserMode)
	c.mu.Lock()
	c.bootErr = err
	c.lastErr = ""
	c.ownedElsewhere = false
	c.mu.Unlock()
	if err != nil {
		c.log.Error("读取引导配置失败", "err", err)
		return
	}
	if err := app.EnsureDirs(boot); err != nil {
		c.setLastErr(fmt.Errorf("无法创建缓存目录 %s: %w", boot.CacheDir, err))
		return
	}
	a := app.New(boot, config.UserMode, c.log)
	a.TakeOver = takeOver
	a.OnStateChange = func(app.Status) { c.notify() }
	c.mu.Lock()
	c.app = a
	c.mu.Unlock()
	if err := a.Start(context.Background()); err != nil {
		c.log.Error("服务启动失败", "err", err)
		var owned *datadir.ErrOwnedElsewhere
		if errors.As(err, &owned) {
			c.mu.Lock()
			c.ownedElsewhere = true
			c.mu.Unlock()
		}
	}
}

func (c *controller) stop() error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.stopLocked()
}

func (c *controller) stopLocked() error {
	a := c.current()
	if a == nil || a.Status().State == app.StateStopped {
		return nil
	}
	c.setBusy("stopping")
	defer c.setBusy("")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := a.Stop(ctx)
	if err != nil {
		c.log.Error("停止服务出错", "err", err)
	}
	return err
}

// shutdown 在程序退出前调用：停服务（封存日志段、释放数据目录），之后不再刷新界面。
func (c *controller) shutdown() {
	c.quitting.Store(true)
	c.opMu.Lock()
	defer c.opMu.Unlock()
	_ = c.stopLocked()
}

// quit 托盘「退出」：先在后台停服务，再让界面程序退出。
func (c *controller) quit() {
	go func() {
		c.shutdown()
		c.wapp.Quit()
	}()
}

// openInBrowser 用默认浏览器打开服务：首次设置时打开带令牌的设置地址；服务没在运行就先启动。
func (c *controller) openInBrowser() {
	go func() {
		st := c.status()
		if st.State != app.StateRunning && st.State != app.StateSetup {
			c.start(false)
			st = c.status()
		}
		target := st.URL
		if st.State == app.StateSetup && st.SetupURL != "" {
			target = st.SetupURL
		}
		if st.State != app.StateRunning && st.State != app.StateSetup {
			return
		}
		if err := c.wapp.Browser.OpenURL(target); err != nil {
			c.setLastErr(fmt.Errorf("无法打开浏览器: %w", err))
		}
	}()
}

// switchDataDir 停止服务、把新数据目录写进引导配置文件，再重新启动。
func (c *controller) switchDataDir(dir string) error {
	if os.Getenv("LEMON_NOTE_DATA_DIR") != "" {
		return errors.New("数据目录由环境变量 LEMON_NOTE_DATA_DIR 指定，请修改环境变量")
	}
	choice, err := resolveDataDir(dir, c.status())
	if err != nil {
		return err
	}
	go func() {
		c.opMu.Lock()
		defer c.opMu.Unlock()
		c.setBusy("switching")
		defer c.setBusy("")
		if err := c.stopLocked(); err != nil {
			c.log.Warn("切换数据目录前停止服务出错", "err", err)
		}
		if err := writeDataDir(c.effectiveConfigPath(), choice.Path); err != nil {
			c.log.Error("写入引导配置失败", "err", err)
			c.startLocked(false)
			c.setLastErr(fmt.Errorf("写入配置文件失败: %w", err))
			return
		}
		c.log.Info("数据目录已更换", "data_dir", choice.Path)
		c.startLocked(false)
	}()
	return nil
}

func (c *controller) refreshAutostart() {
	if c.wapp == nil {
		return
	}
	on, err := c.wapp.Autostart.IsEnabled()
	c.mu.Lock()
	c.autostart = on
	if err != nil && !errors.Is(err, application.ErrAutostartNotSupported) {
		c.autostartErr = err.Error()
	} else {
		c.autostartErr = ""
	}
	c.mu.Unlock()
	c.notify()
}

func (c *controller) setAutostart(on bool) error {
	var err error
	if on {
		err = c.wapp.Autostart.EnableWithOptions(c.autostartOptions())
	} else {
		err = c.wapp.Autostart.Disable()
	}
	c.refreshAutostart()
	if err != nil {
		c.log.Error("设置开机自启失败", "enable", on, "err", err)
	}
	return err
}

// openPath 用系统文件管理器打开目录（不存在就先建出来）。
func openPath(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", filepath.Clean(dir))
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// dirChoice 是选择新数据目录后实际要用的位置。
type dirChoice struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // existing：已有的 Lemon Note 数据目录；empty：空目录，启动后做首次设置
}

// ignorable 是判断空目录时可以忽略的系统文件。
func ignorable(name string) bool {
	switch strings.ToLower(name) {
	case ".ds_store", "desktop.ini", "thumbs.db", ".localized":
		return true
	}
	return datadir.IsTempName(name)
}

func isDataDir(p string) bool {
	st, err := os.Stat((&datadir.Dir{Root: p}).InstancePath())
	return err == nil && !st.IsDir()
}

// emptyOrMissing 目录不存在或里面只有可忽略的系统文件。
func emptyOrMissing(p string) bool {
	entries, err := os.ReadDir(p)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !ignorable(e.Name()) {
			return false
		}
	}
	return true
}

// resolveDataDir 判断所选文件夹能不能当数据目录：已有的数据目录或空目录直接用；
// 其他有内容的文件夹（例如同步盘根目录）在里面建「Lemon Note」子目录。
func resolveDataDir(selected string, cur viewStatus) (dirChoice, error) {
	abs, err := filepath.Abs(selected)
	if err != nil {
		return dirChoice{}, err
	}
	pick := func(p string) (dirChoice, bool) {
		if isDataDir(p) {
			return dirChoice{Path: p, Kind: "existing"}, true
		}
		if emptyOrMissing(p) {
			return dirChoice{Path: p, Kind: "empty"}, true
		}
		return dirChoice{}, false
	}
	ch, ok := pick(abs)
	if !ok {
		ch, ok = pick(filepath.Join(abs, "Lemon Note"))
	}
	if !ok {
		return dirChoice{}, errors.New("所选文件夹里已有其他内容，请选择空文件夹或已有的 Lemon Note 数据目录")
	}
	same := func(a, b string) bool {
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
		}
		return filepath.Clean(a) == filepath.Clean(b)
	}
	if same(ch.Path, cur.DataDir) {
		return dirChoice{}, errors.New("这就是当前的数据目录")
	}
	if cur.CacheDir != "" {
		if rel, err := filepath.Rel(cur.CacheDir, ch.Path); err == nil && !strings.HasPrefix(rel, "..") {
			return dirChoice{}, errors.New("数据目录不能放在缓存目录里")
		}
	}
	return ch, nil
}
