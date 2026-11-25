package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/logx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"golang.org/x/term"
)

// openOffline 在服务停止时直接打开数据目录做维护。
func openOffline(g *globalFlags) (*persist.Instance, error) {
	b, _, err := loadBoot(g)
	if err != nil {
		return nil, err
	}
	hostID, err := config.HostID(b.CacheDir)
	if err != nil {
		return nil, err
	}
	log, _ := logx.Setup("warn", "", true)
	inst, err := persist.Open(context.Background(), persist.Config{DataDir: b.DataDir, CacheDir: b.CacheDir, DSN: b.DSN, HostID: hostID,
		Version: version.Version, TakeOver: g.takeOver, Logger: log})
	if errors.Is(err, persist.ErrNeedSetup) {
		return nil, fmt.Errorf("数据目录 %s 尚未初始化", b.DataDir)
	}
	if err != nil {
		return nil, fmt.Errorf("%w（如果服务正在运行，请先停止服务）", err)
	}
	return inst, nil
}

func readPassword(prompt string) string {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, _ := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(b)
	}
	return readLine("")
}

func runAdmin(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	g, err := parseGlobal(fs, args[1:])
	if err != nil {
		return err
	}
	ctx := context.Background()
	inst, err := openOffline(g)
	if err != nil {
		return err
	}
	defer inst.Close(ctx)
	cat, err := catalog.Load(ctx, inst.Store)
	if err != nil {
		return err
	}
	acc := accounts.New(inst.Store, cat)
	findUser := func() (*model.User, error) {
		if fs.NArg() < 1 {
			return nil, errors.New("请提供用户名")
		}
		var u model.User
		if err := inst.Store.Get(ctx, &u, "SELECT * FROM users WHERE username = ?", fs.Arg(0)); err != nil {
			return nil, errors.New("用户不存在")
		}
		return &u, nil
	}
	switch action {
	case "list-users":
		var users []model.User
		if err := inst.Store.Select(ctx, &users, "SELECT * FROM users ORDER BY created_at"); err != nil {
			return err
		}
		for _, u := range users {
			fmt.Printf("%-20s %-20s %-8s %-8s 两步验证:%v\n", u.Username, u.DisplayName, u.Role, u.Status, u.TOTPEnabled)
		}
	case "reset-password":
		u, err := findUser()
		if err != nil {
			return err
		}
		pw := readPassword("新口令：")
		if pw2 := readPassword("再输一次："); pw != pw2 {
			return errors.New("两次输入不一致")
		}
		if err := acc.SetPassword(ctx, "cli", u.ID, pw); err != nil {
			return err
		}
		inst.Store.ExecLocal(ctx, "DELETE FROM sessions WHERE user_id = ?", u.ID)
		fmt.Println("口令已重置，该用户的所有登录已失效。")
	case "reset-2fa":
		u, err := findUser()
		if err != nil {
			return err
		}
		if _, err := acc.UpdateUser(ctx, "cli", u.ID, func(x *model.User) error {
			x.TOTPSecret, x.TOTPEnabled, x.RecoveryCodes = "", false, "[]"
			return nil
		}); err != nil {
			return err
		}
		fmt.Println("已关闭该用户的两步验证。")
	default:
		usage()
	}
	return nil
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	g, err := parseGlobal(fs, args)
	if err != nil {
		return err
	}
	ctx := context.Background()
	inst, err := openOffline(g)
	if err != nil {
		return err
	}
	defer inst.Close(ctx)
	st := inst.Status(ctx)
	fmt.Printf("数据目录：%s\n缓存目录：%s\n数据库：%s\n当前序号：%d（已写入数据目录 %d）\n快照序号：%d\n日志段：%d 个，%d 字节\n",
		st.DataDir, st.CacheDir, st.Engine, st.Seq, st.SealedSeq, st.SnapshotSeq, st.JournalFiles, st.JournalBytes)
	if len(st.Warnings) == 0 {
		fmt.Println("检查通过，没有发现问题。")
	}
	for _, w := range st.Warnings {
		fmt.Println("警告：", w)
	}
	return nil
}

func runConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	save := fs.Bool("save", false, "把命令行选项写回配置文件")
	g, err := parseGlobal(fs, args)
	if err != nil {
		return err
	}
	b, _, err := loadBoot(g)
	if err != nil {
		return err
	}
	if *save {
		if err := b.Save(); err != nil {
			return err
		}
		fmt.Println("已保存到", b.Path())
	}
	fmt.Printf("配置文件：%s\n数据目录：%s\n缓存目录：%s\n监听地址：%s\nTLS：%s\n", b.Path(), b.DataDir, b.CacheDir, b.Listen, b.TLS.Mode)
	return nil
}
