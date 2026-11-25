package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/engine"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/logx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
)

func backupUsage() {
	fmt.Fprint(os.Stderr, `用法（服务停止时直接操作数据目录）：
  lemon-note backup list [<目标>]                       列出备份目标；给出目标时列出目标上的备份
  lemon-note backup run <目标>                          立即执行一次备份
  lemon-note backup restore <目标> <备份名> --to <新目录>  恢复到一个新的空目录
  lemon-note backup restore <目标> <备份名> --overwrite    用备份替换当前数据目录（原目录改名保留）
  lemon-note backup verify <目标> <备份名> [--full]      校验备份完整性

<目标> 可以是：
  已保存的备份目标名称或 ID（从当前数据目录读取，需要先停止服务）；
  一个本机目录的绝对路径（例如挂载的 NAS 目录，不需要数据目录，换机器恢复时用）；
  一个 .lnbak 文件的路径（此时省略 <备份名>）；
  或者用 --target-file <文件> 给出 {"kind":"s3","config":{...}} 形式的目标配置，此时省略 <目标>。
<备份名> 是全量包文件名（lemon-note-xxxxxxxx-YYYYMMDD-HHMMSS.lnbak）或镜像目录名（mirror-xxxxxxxx），可用 list 查看。

选项：
  --to <目录>             恢复到的新目录（必须不存在或为空）
  --overwrite             覆盖当前数据目录：先恢复到同级的临时目录并校验，再把原目录改名为 *.before-restore-时间 保留
  --yes                   覆盖时不再询问确认
  --full                  校验镜像时下载全部附件核对哈希（默认抽查）
  --password-file <文件>  备份口令文件（也可以用环境变量 LEMON_NOTE_BACKUP_PASSWORD；加密目标未提供时会提示输入）
  --target-file <文件>    目标配置文件
  以及 --config、--data-dir、--cache-dir、--take-over 等全局选项
`)
}

// parseInterspersed 允许选项出现在位置参数之后（例如 restore a b --to c）。
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// cliTarget 是命令行操作的目标：要么是数据目录里保存的目标（经引擎操作），要么是临时指定的目标。
type cliTarget struct {
	label    string
	backup   string // 由 .lnbak 文件路径推出的备份名
	adhoc    target.Target
	inst     *persist.Instance
	eng      *engine.Engine
	targetID string
}

func (c *cliTarget) close() {
	if c.adhoc != nil {
		c.adhoc.Close()
	}
	if c.eng != nil {
		c.eng.Close()
	}
	if c.inst != nil {
		c.inst.Close(context.Background())
	}
}

type backupFlags struct {
	to, passwordFile, targetFile string
	overwrite, yes, full         bool
}

func runBackupCmd(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		backupUsage()
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet("backup "+action, flag.ExitOnError)
	fs.Usage = backupUsage
	var bf backupFlags
	fs.StringVar(&bf.to, "to", "", "恢复到的新目录")
	fs.BoolVar(&bf.overwrite, "overwrite", false, "覆盖当前数据目录")
	fs.BoolVar(&bf.yes, "yes", false, "不再询问确认")
	fs.BoolVar(&bf.full, "full", false, "校验镜像时核对全部附件哈希")
	fs.StringVar(&bf.passwordFile, "password-file", "", "备份口令文件")
	fs.StringVar(&bf.targetFile, "target-file", "", "目标配置文件")
	g, err := parseGlobal(fs, nil)
	if err != nil {
		return err
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch action {
	case "list":
		return backupList(ctx, g, &bf, pos)
	case "run":
		return backupRun(ctx, g, &bf, pos)
	case "restore":
		return backupRestore(ctx, g, &bf, pos)
	case "verify":
		return backupVerify(ctx, g, &bf, pos)
	default:
		backupUsage()
		return fmt.Errorf("不认识的备份命令「%s」", action)
	}
}

// openEngineOffline 打开数据目录（服务必须已停止）并创建备份引擎。
func openEngineOffline(g *globalFlags) (*persist.Instance, *engine.Engine, error) {
	inst, err := openOffline(g)
	if err != nil {
		return nil, nil, err
	}
	key, err := inst.SecretKey()
	if err != nil {
		inst.Close(context.Background())
		return nil, nil, fmt.Errorf("读取 config/secret.key 失败：%w", err)
	}
	box, err := crypt.New(key)
	if err != nil {
		inst.Close(context.Background())
		return nil, nil, err
	}
	b, _, _ := loadBoot(g)
	log, _ := logx.Setup("warn", "", true)
	opt := engine.Options{Inst: inst, Box: box, Log: log}
	if b != nil {
		opt.ConfigPath, opt.CacheRoot = b.Path(), b.CacheDir
	}
	e, err := engine.New(opt)
	if err != nil {
		inst.Close(context.Background())
		return nil, nil, err
	}
	return inst, e, nil
}

// resolveTarget 解析 <目标>，返回剩余的位置参数。
func resolveTarget(g *globalFlags, bf *backupFlags, pos []string) (*cliTarget, []string, error) {
	if bf.targetFile != "" {
		raw, err := os.ReadFile(bf.targetFile)
		if err != nil {
			return nil, nil, fmt.Errorf("读取目标配置文件失败：%w", err)
		}
		var spec struct {
			Kind   string          `json:"kind"`
			Config json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal(raw, &spec); err != nil || spec.Kind == "" || len(spec.Config) == 0 {
			return nil, nil, errors.New(`目标配置文件格式应为 {"kind":"类型","config":{...}}`)
		}
		t, err := target.Open(spec.Kind, spec.Config, target.Deps{})
		if err != nil {
			return nil, nil, err
		}
		return &cliTarget{label: bf.targetFile, adhoc: t}, pos, nil
	}
	if len(pos) == 0 {
		return nil, nil, errors.New("请指定备份目标")
	}
	ref := pos[0]
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, ".") {
		abs, err := filepath.Abs(ref)
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, nil, fmt.Errorf("找不到「%s」：%w", ref, err)
		}
		dir, backup := abs, ""
		if !info.IsDir() {
			dir, backup = filepath.Dir(abs), filepath.Base(abs)
		}
		cfg, _ := json.Marshal(target.LocalConfig{Path: dir})
		t, err := target.Open(target.KindLocal, cfg, target.Deps{})
		if err != nil {
			return nil, nil, err
		}
		return &cliTarget{label: dir, backup: backup, adhoc: t}, pos[1:], nil
	}
	inst, e, err := openEngineOffline(g)
	if err != nil {
		return nil, nil, err
	}
	id, err := e.FindTarget(context.Background(), ref)
	if err != nil {
		e.Close()
		inst.Close(context.Background())
		return nil, nil, err
	}
	return &cliTarget{label: ref, inst: inst, eng: e, targetID: id}, pos[1:], nil
}

// cliPassword 读取命令行给出的备份口令（没有给出返回空串）。
func cliPassword(bf *backupFlags) (string, error) {
	if bf.passwordFile != "" {
		b, err := os.ReadFile(bf.passwordFile)
		if err != nil {
			return "", fmt.Errorf("读取口令文件失败：%w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return os.Getenv("LEMON_NOTE_BACKUP_PASSWORD"), nil
}

// adhocKey 为临时目标准备密钥：目标加密而没有给出口令时提示输入。
func adhocKey(ctx context.Context, t target.Target, bf *backupFlags, prompt bool) ([]byte, *engine.Header, error) {
	pw, err := cliPassword(bf)
	if err != nil {
		return nil, nil, err
	}
	h, err := engine.ReadHeader(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	if !h.Encrypted() {
		return nil, h, nil
	}
	if pw == "" {
		if !prompt {
			return nil, h, nil
		}
		pw = readPassword("备份口令：")
	}
	key, err := h.Key(pw)
	return key, h, err
}

// progressPrinter 把日志实时打到终端，并每隔一段时间打印进度。
func progressPrinter(ctx context.Context) (*engine.Progress, func()) {
	p := engine.NewProgress()
	p.OnLine = func(line string) { fmt.Println(line) }
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		var last engine.ProgressView
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				v := p.View()
				if v == last || v.BytesTotal == 0 {
					continue
				}
				last = v
				fmt.Printf("  进度：%d / %d 个文件，%.1f%%\n", v.FilesDone, v.FilesTotal, float64(v.BytesDone)*100/float64(v.BytesTotal))
			}
		}
	}()
	var once sync.Once
	return p, func() { once.Do(func() { close(done) }) }
}

func backupList(ctx context.Context, g *globalFlags, bf *backupFlags, pos []string) error {
	if len(pos) == 0 && bf.targetFile == "" {
		inst, e, err := openEngineOffline(g)
		if err != nil {
			return err
		}
		defer inst.Close(context.Background())
		defer e.Close()
		list, err := e.ListTargets(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("还没有备份目标。")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "名称\tID\t类型\t模式\t加密\t计划\t最近执行\t状态")
		for _, t := range list {
			last := "-"
			if t.LastRunAt != nil {
				last = time.UnixMilli(*t.LastRunAt).Format("2006-01-02 15:04")
			}
			enc := "否"
			if t.Encryption.Enabled {
				enc = "是"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.ID, t.Kind, t.Mode, enc, scheduleText(t.Schedule), last, dash(t.LastStatus))
		}
		return w.Flush()
	}
	ct, _, err := resolveTarget(g, bf, pos)
	if err != nil {
		return err
	}
	defer ct.close()
	var list []engine.BackupInfo
	if ct.eng != nil {
		pw, err := cliPassword(bf)
		if err != nil {
			return err
		}
		list, err = ct.eng.ListTargetBackups(ctx, ct.targetID, pw)
		if err != nil {
			return err
		}
	} else {
		key, h, err := adhocKey(ctx, ct.adhoc, bf, false)
		if err != nil && !errors.Is(err, engine.ErrWrongPassword) {
			return err
		}
		list, err = engine.ListBackups(ctx, ct.adhoc, key, h, "")
		if err != nil {
			return err
		}
	}
	if len(list) == 0 {
		fmt.Println("目标上没有 Lemon Note 备份。")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "备份名\t模式\t实例\t时间\t大小\t加密\t备注")
	for _, b := range list {
		size := "-"
		if b.Size > 0 {
			size = fmt.Sprintf("%.1f MB", float64(b.Size)/(1<<20))
			if b.Volumes > 1 {
				size += fmt.Sprintf("（%d 卷）", b.Volumes)
			}
		}
		when := "-"
		if b.CreatedAt > 0 {
			when = time.UnixMilli(b.CreatedAt).Format("2006-01-02 15:04:05")
		}
		enc := "否"
		if b.Encrypted {
			enc = "是"
		}
		inst := b.Instance
		if b.Current {
			inst += "（当前）"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", b.Name, b.Mode, inst, when, size, enc, b.Note)
	}
	return w.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func scheduleText(s engine.Schedule) string {
	week := []string{"日", "一", "二", "三", "四", "五", "六"}
	switch s.Kind {
	case engine.ScheduleHourly:
		return fmt.Sprintf("每 %d 小时", s.EveryHours)
	case engine.ScheduleDaily:
		return "每天 " + s.At
	case engine.ScheduleWeekly:
		var d []string
		for _, w := range s.Weekdays {
			d = append(d, "周"+week[w])
		}
		return strings.Join(d, "、") + " " + s.At
	}
	return "仅手动"
}

func backupRun(ctx context.Context, g *globalFlags, bf *backupFlags, pos []string) error {
	if bf.targetFile != "" || len(pos) == 0 {
		return errors.New("请指定一个已保存的备份目标（名称或 ID）")
	}
	ct, _, err := resolveTarget(g, bf, pos)
	if err != nil {
		return err
	}
	defer ct.close()
	if ct.eng == nil {
		return errors.New("执行备份需要使用数据目录里已保存的备份目标（名称或 ID）")
	}
	p, stopP := progressPrinter(ctx)
	defer stopP()
	run, err := ct.eng.RunBackup(ctx, ct.targetID, engine.RunOptions{Trigger: "cli", Progress: p})
	if err != nil {
		return err
	}
	fmt.Printf("备份完成：%s，%d 个文件，%.1f MB\n", run.Artifact, run.Files, float64(run.Bytes)/(1<<20))
	if run.Status == engine.StatusWarning {
		fmt.Println("注意：", run.Error)
	}
	return nil
}

// backupName 取 <备份名>：.lnbak 路径推出的优先。
func backupName(ct *cliTarget, rest []string) (string, error) {
	if ct.backup != "" {
		if len(rest) > 0 {
			return "", errors.New("已经通过 .lnbak 文件路径指定了备份，不要再给出备份名")
		}
		return ct.backup, nil
	}
	if len(rest) == 0 {
		return "", errors.New("请指定备份名（可用 lemon-note backup list <目标> 查看）")
	}
	if len(rest) > 1 {
		return "", fmt.Errorf("多余的参数：%s", strings.Join(rest[1:], " "))
	}
	return rest[0], nil
}

func backupVerify(ctx context.Context, g *globalFlags, bf *backupFlags, pos []string) error {
	ct, rest, err := resolveTarget(g, bf, pos)
	if err != nil {
		return err
	}
	defer ct.close()
	name, err := backupName(ct, rest)
	if err != nil {
		return err
	}
	p, stopP := progressPrinter(ctx)
	defer stopP()
	opt := engine.VerifyOptions{FullHash: bf.full}
	var res *engine.VerifyResult
	if ct.eng != nil {
		pw, err := cliPassword(bf)
		if err != nil {
			return err
		}
		res, err = ct.eng.VerifyNow(ctx, ct.targetID, name, opt, pw, engine.RunOptions{Trigger: "cli", Progress: p})
		if err != nil {
			return err
		}
	} else {
		key, _, err := adhocKey(ctx, ct.adhoc, bf, true)
		if err != nil {
			return err
		}
		res, err = engine.Verify(ctx, ct.adhoc, name, key, opt, p)
		if err != nil {
			return err
		}
	}
	if !res.OK {
		for _, pr := range res.Problems {
			fmt.Println("问题：", pr)
		}
		return errors.New("校验未通过")
	}
	fmt.Printf("校验通过：%s，%d 个文件，%.1f MB，数据序号 %d，附件 %d 个（核对内容 %d 个）\n",
		res.Backup, res.Files, float64(res.Bytes)/(1<<20), res.Seq, res.Blobs, res.Hashed)
	return nil
}

func backupRestore(ctx context.Context, g *globalFlags, bf *backupFlags, pos []string) error {
	if (bf.to == "") == !bf.overwrite {
		return errors.New("请用 --to <新目录> 指定恢复位置，或用 --overwrite 覆盖当前数据目录（二选一）")
	}
	var dataDir string
	if bf.overwrite {
		b, _, err := loadBoot(g)
		if err != nil {
			return err
		}
		dataDir, err = filepath.Abs(b.DataDir)
		if err != nil {
			return err
		}
	}
	ct, rest, err := resolveTarget(g, bf, pos)
	if err != nil {
		return err
	}
	defer ct.close()
	name, err := backupName(ct, rest)
	if err != nil {
		return err
	}
	dest := bf.to
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	if bf.overwrite {
		if !bf.yes {
			fmt.Printf("将用备份 %s 替换数据目录 %s。\n原目录会改名保留为 %s.before-restore-<时间>，确认无误后可自行删除。\n", name, dataDir, filepath.Base(dataDir))
			if strings.ToLower(readLine("输入 yes 继续：")) != "yes" {
				return errors.New("已取消")
			}
		}
		if ct.inst == nil {
			// 临时目标：自己确认服务没有在用这个数据目录。
			if release, err = lockDataDir(g, dataDir); err != nil {
				return err
			}
		}
		dest = dataDir + ".restore-" + time.Now().Format("20060102-150405")
	} else {
		if dest, err = filepath.Abs(dest); err != nil {
			return err
		}
	}
	p, stopP := progressPrinter(ctx)
	defer stopP()
	var res *engine.RestoreResult
	if ct.eng != nil {
		pw, err := cliPassword(bf)
		if err != nil {
			return err
		}
		res, err = ct.eng.RestoreNow(ctx, ct.targetID, name, dest, pw, engine.RunOptions{Trigger: "cli", Progress: p})
		if err != nil {
			return err
		}
	} else {
		key, _, err := adhocKey(ctx, ct.adhoc, bf, true)
		if err != nil {
			return err
		}
		res, err = engine.Restore(ctx, ct.adhoc, name, dest, key, engine.RestoreOptions{}, p)
		if err != nil {
			return err
		}
		res.Dir = dest
	}
	stopP()
	if len(res.MissingBlobs) > 0 {
		fmt.Printf("注意：有 %d 个附件在备份里找不到，恢复后这些附件无法打开（详见上面的警告）。\n", len(res.MissingBlobs))
	}
	if !bf.overwrite {
		fmt.Printf("已恢复到 %s（数据序号 %d，附件 %d 个）。\n请在引导配置文件里把 data_dir 改为这个目录（或启动时加 --data-dir %s），原来的数据目录不会被改动。\n",
			dest, res.Seq, res.Blobs, dest)
		return nil
	}
	// 覆盖：先放开数据目录上的锁，再把原目录改名保留、新目录换上去。
	if ct.eng != nil {
		ct.eng.Close()
		ct.eng = nil
	}
	if ct.inst != nil {
		if err := ct.inst.Close(context.Background()); err != nil {
			return fmt.Errorf("关闭当前数据目录失败，恢复出的数据在 %s：%w", dest, err)
		}
		ct.inst = nil
	}
	if release != nil {
		release()
		release = nil
	}
	aside := ""
	if _, err := os.Stat(dataDir); err == nil {
		aside = dataDir + ".before-restore-" + time.Now().Format("20060102-150405")
		if err := datadir.RenameRetry(dataDir, aside); err != nil {
			return fmt.Errorf("无法把原数据目录改名（恢复出的数据在 %s，可以手工替换）：%w", dest, err)
		}
	}
	if err := datadir.RenameRetry(dest, dataDir); err != nil {
		if aside != "" {
			_ = datadir.RenameRetry(aside, dataDir)
		}
		return fmt.Errorf("无法把恢复出的目录换到 %s（恢复出的数据在 %s，可以手工替换）：%w", dataDir, dest, err)
	}
	if aside != "" {
		_ = os.Remove(filepath.Join(aside, "locks", "owner.json"))
		fmt.Printf("已用备份替换数据目录 %s（数据序号 %d，附件 %d 个）。原目录已保留为 %s。\n", dataDir, res.Seq, res.Blobs, aside)
	} else {
		fmt.Printf("已恢复到数据目录 %s（数据序号 %d，附件 %d 个）。\n", dataDir, res.Seq, res.Blobs)
	}
	fmt.Println("现在可以启动服务了。")
	return nil
}

// lockDataDir 在覆盖数据目录前确认没有服务在使用它（数据目录尚未初始化或已损坏时直接放行）。
func lockDataDir(g *globalFlags, dataDir string) (func(), error) {
	d := &datadir.Dir{Root: dataDir}
	info, err := d.ReadInstance()
	if err != nil {
		return func() {}, nil
	}
	b, _, err := loadBoot(g)
	if err != nil {
		return nil, err
	}
	hostID, err := config.HostID(b.CacheDir)
	if err != nil {
		return nil, err
	}
	lock, err := d.Acquire(filepath.Join(b.CacheDir, info.InstanceID, "instance.lock"), hostID, version.Version, g.takeOver)
	if err != nil {
		return nil, fmt.Errorf("%w（如果服务正在运行，请先停止服务）", err)
	}
	return lock.Release, nil
}
