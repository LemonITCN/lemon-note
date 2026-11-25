// lemon-note 命令行版：前台运行、装成系统服务、管理员维护命令。
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/app"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/logx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/kardianos/service"
)

type globalFlags struct {
	config   string
	dataDir  string
	cacheDir string
	listen   string
	takeOver bool
	asSvc    bool
}

func parseGlobal(fs *flag.FlagSet, args []string) (*globalFlags, error) {
	g := &globalFlags{}
	fs.StringVar(&g.config, "config", "", "引导配置文件路径")
	fs.StringVar(&g.dataDir, "data-dir", "", "数据目录")
	fs.StringVar(&g.cacheDir, "cache-dir", "", "缓存目录")
	fs.StringVar(&g.listen, "listen", "", "监听地址，例如 :8686 或 127.0.0.1:8686")
	fs.BoolVar(&g.takeOver, "take-over", false, "强制接管被另一台机器占用的数据目录")
	fs.BoolVar(&g.asSvc, "service", false, "以系统服务方式运行（由服务管理器传入）")
	return g, fs.Parse(args)
}

func loadBoot(g *globalFlags) (*config.Bootstrap, config.Mode, error) {
	mode := config.UserMode
	if g.asSvc {
		mode = config.ServiceMode
	}
	b, err := config.Load(g.config, mode)
	if err != nil {
		return nil, mode, err
	}
	if g.dataDir != "" {
		b.DataDir = g.dataDir
	}
	if g.cacheDir != "" {
		b.CacheDir = g.cacheDir
	}
	if g.listen != "" {
		b.Listen = g.listen
	}
	return b, mode, nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `Lemon Note %s

用法：
  lemon-note [serve] [选项]          前台运行服务
  lemon-note service <命令> [选项]   install | uninstall | start | stop | restart | status
  lemon-note admin <命令> [选项]     reset-password <用户名> | reset-2fa <用户名> | list-users
  lemon-note doctor [选项]           检查数据目录完整性
  lemon-note config [选项]           显示当前生效的配置（选项会写回配置文件）
  lemon-note version

选项：
  --config <文件>     引导配置文件
  --data-dir <目录>   数据目录（全部数据都在这里，可放同步盘）
  --cache-dir <目录>  缓存目录（可随时删除，会自动重建）
  --listen <地址>     监听地址，默认 :%d
  --take-over         强制接管被另一台机器占用的数据目录
`, version.Version, config.DefaultPort)
}

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "service":
		err = runService(args)
	case "admin":
		err = runAdmin(args)
	case "doctor":
		err = runDoctor(args)
	case "config":
		err = runConfig(args)
	case "version", "--version", "-v":
		fmt.Printf("Lemon Note %s %s %s\n", version.Version, version.Commit, version.Date)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

// program 适配 kardianos/service。
type program struct {
	a      *app.App
	closer func()
}

func (p *program) Start(s service.Service) error {
	go func() {
		if err := p.a.Start(context.Background()); err != nil {
			p.a.Log.Error("启动失败", "err", err)
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := p.a.Stop(ctx)
	if p.closer != nil {
		p.closer()
	}
	return err
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.Usage = usage
	g, err := parseGlobal(fs, args)
	if err != nil {
		return err
	}
	b, mode, err := loadBoot(g)
	if err != nil {
		return err
	}
	if err := app.EnsureDirs(b); err != nil {
		return fmt.Errorf("无法创建缓存目录 %s: %w", b.CacheDir, err)
	}
	// Windows 服务没有可用的标准错误，其余情况都同时写标准错误（Docker、systemd 日志需要）
	log, closer := logx.Setup(b.LogLevel, app.LogDir(b.CacheDir), !(g.asSvc && runtime.GOOS == "windows"))
	a := app.New(b, mode, log)
	a.TakeOver = g.takeOver
	if g.asSvc {
		svc, err := service.New(&program{a: a, closer: func() { closer.Close() }}, serviceConfig(g, b))
		if err != nil {
			return err
		}
		return svc.Run()
	}
	defer closer.Close()
	if err := a.Start(context.Background()); err != nil {
		return err
	}
	st := a.Status()
	fmt.Printf("\nLemon Note %s 已启动\n  地址：%s\n  数据目录：%s\n", version.Version, st.URL, b.DataDir)
	if st.SetupURL != "" {
		fmt.Printf("\n首次使用，请在浏览器打开下面的地址完成设置：\n  %s\n\n", st.SetupURL)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("正在停止……")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return a.Stop(ctx)
}

func serviceConfig(g *globalFlags, b *config.Bootstrap) *service.Config {
	args := []string{"serve", "--service"}
	if g.config != "" {
		args = append(args, "--config", g.config)
	}
	return &service.Config{
		Name:        "lemon-note",
		DisplayName: "Lemon Note",
		Description: "Lemon Note 私有化笔记服务",
		Arguments:   args,
		Option:      service.KeyValue{"Restart": "on-failure", "SuccessExitStatus": "1 2 8 SIGKILL", "OnFailure": "restart"},
	}
}

func runService(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet("service", flag.ExitOnError)
	g, err := parseGlobal(fs, args[1:])
	if err != nil {
		return err
	}
	g.asSvc = true
	b, _, err := loadBoot(g)
	if err != nil {
		return err
	}
	if action == "install" && (g.dataDir != "" || g.listen != "" || g.cacheDir != "") {
		// 安装服务时把命令行指定的目录写进服务模式的配置文件，服务启动时才能读到
		if err := b.Save(); err != nil {
			return fmt.Errorf("写入配置文件失败（可能需要管理员权限）: %w", err)
		}
		fmt.Println("已写入配置文件：", b.Path())
	}
	svc, err := service.New(&program{}, serviceConfig(g, b))
	if err != nil {
		return err
	}
	if action == "status" {
		st, err := svc.Status()
		if err != nil {
			return err
		}
		names := map[service.Status]string{service.StatusRunning: "运行中", service.StatusStopped: "已停止", service.StatusUnknown: "未安装或未知"}
		fmt.Println(names[st])
		return nil
	}
	if err := service.Control(svc, action); err != nil {
		return fmt.Errorf("%s 失败（可能需要管理员权限）: %w", action, err)
	}
	fmt.Println("完成：", action)
	return nil
}

func readLine(prompt string) string {
	fmt.Print(prompt)
	sc := bufio.NewScanner(os.Stdin)
	sc.Scan()
	return strings.TrimSpace(sc.Text())
}
