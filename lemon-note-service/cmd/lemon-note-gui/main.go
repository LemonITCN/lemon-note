//go:build gui

// lemon-note-gui 是带界面版：托盘图标常驻（Windows 托盘、macOS 菜单栏、Linux AppIndicator），
// 进程内运行与命令行版相同的服务（internal/app），另有一个状态窗口。
// 构建：go build -tags gui,production ./cmd/lemon-note-gui（Linux 与 macOS 需要 CGO，Windows 不需要）。
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/app"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/logx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// appID 同时用作 macOS 包标识、Linux 应用 ID 与单实例锁的名字。
const appID = "cn.lemonit.note"

var (
	//go:embed ui/index.html
	indexHTML []byte
	//go:embed build/appicon.png
	appIcon []byte
	//go:embed build/tray/tray.png
	trayIcon []byte
	//go:embed build/tray/tray-off.png
	trayIconOff []byte
	//go:embed build/tray/tray-template.png
	trayTemplateIcon []byte
)

func main() {
	configPath := flag.String("config", "", "引导配置文件路径（默认按系统放在用户目录）")
	hidden := flag.Bool("hidden", false, "启动时不显示状态窗口（开机自启时使用）")
	showVersion := flag.Bool("version", false, "显示版本后退出")
	flag.Parse()
	if *showVersion {
		fmt.Printf("Lemon Note %s %s %s\n", version.Version, version.Commit, version.Date)
		return
	}
	if *configPath != "" {
		if abs, err := filepath.Abs(*configPath); err == nil {
			*configPath = abs
		}
	}

	// 日志写缓存目录下的 logs（与命令行版同一位置）；配置读不出来时退回默认缓存目录
	cacheDir := config.DefaultCacheDir(config.UserMode)
	level := "info"
	if b, err := config.Load(*configPath, config.UserMode); err == nil {
		cacheDir, level = b.CacheDir, b.LogLevel
	}
	// Windows 界面程序没有可用的标准错误，写它会让后面的日志文件也写不进去
	log, closer := logx.Setup(level, app.LogDir(cacheDir), runtime.GOOS != "windows")
	log.Info("Lemon Note 带界面版启动", "version", version.Version, "commit", version.Commit)

	lang := detectLang()
	ctl := newController(*configPath, log, cacheDir)

	var window *application.WebviewWindow
	showWindow := func() {
		if window == nil {
			return
		}
		application.InvokeAsync(func() { window.Show().Focus() })
	}

	wapp := application.New(application.Options{
		Name:        "Lemon Note",
		Description: "Lemon Note 私有化笔记",
		Icon:        appIcon,
		Logger:      log.With("component", "gui"),
		Assets:      application.AssetOptions{Handler: ctl.handler(lang), DisableLogging: true},
		Mac: application.MacOptions{
			// 只在菜单栏显示图标，不占程序坞
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: true, ApplicationID: appID},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appID,
			// 已经在运行时再次打开程序：把状态窗口拿出来
			OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
				if !slices.Contains(d.Args, "--hidden") && !slices.Contains(d.Args, "-hidden") {
					showWindow()
				}
			},
		},
		// 任何途径退出（托盘菜单、系统注销、信号）都先停服务，保证日志段封存、数据目录释放
		OnShutdown:   ctl.shutdown,
		PostShutdown: func() { closer.Close() },
	})
	ctl.wapp = wapp

	window = wapp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "status",
		Title:     "Lemon Note",
		Width:     560,
		Height:    640,
		MinWidth:  480,
		MinHeight: 540,
		URL:       "/",
		Hidden:    true,
	})
	// 关闭窗口只是隐藏，程序继续在托盘里运行
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})
	ctl.window = window

	tray := newTray(wapp, ctl, lang, showWindow)
	ctl.onChange = tray.update

	// macOS 上再次打开已在运行的程序不会起第二个进程，而是收到 reopen 事件
	wapp.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { showWindow() })
	wapp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		ctl.refreshAutostart()
		// Windows / Linux 手动打开时直接显示状态窗口；macOS 按菜单栏程序的习惯安静启动
		if !*hidden && runtime.GOOS != "darwin" {
			showWindow()
		}
		go func() {
			ctl.start(false)
			// 数据目录还没初始化：把窗口拿出来，引导去完成首次设置
			if !*hidden && ctl.status().State == app.StateSetup {
				showWindow()
			}
		}()
	})

	if err := wapp.Run(); err != nil {
		log.Error("界面程序异常退出", "err", err)
		closer.Close()
		os.Exit(1)
	}
}

// autostartOptions 是登记开机自启时用的参数：带 --hidden，开机时只在托盘里启动。
func (c *controller) autostartOptions() application.AutostartOptions {
	args := []string{"--hidden"}
	if c.configPath != "" {
		args = append(args, "--config", c.configPath)
	}
	id := appID
	if runtime.GOOS == "windows" {
		id = "LemonNote"
	}
	return application.AutostartOptions{Identifier: id, Arguments: args}
}
