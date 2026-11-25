package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Options 是创建引擎的参数。
type Options struct {
	Inst       *persist.Instance
	Box        *crypt.Box
	Log        *slog.Logger
	HTTPClient *http.Client // 访问 HTTP 类目标用；nil 用驱动内置的默认客户端
	ConfigPath string       // 引导配置文件路径，恢复完成后的提示里用
	CacheRoot  string       // 缓存根目录；恢复目录不能放在这里

	// 以下主要给测试用。
	Now          func() time.Time
	Location     *time.Location // 计划与分层保留按这个时区算，默认本机时区
	VolumeSize   int64          // 大于 0 时覆盖目标设置的分卷大小（字节）
	OpenTarget   func(kind string, config []byte, deps target.Deps) (target.Target, error)
	MaxScheduled int // 同时执行的计划任务上限，默认 2
}

// Engine 是备份引擎。所有方法都可以并发调用。
type Engine struct {
	o    Options
	inst *persist.Instance
	st   *store.Store
	box  *crypt.Box
	log  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	closed    bool
	busy      map[string]*operation // 目标 ID → 正在进行的操作
	ops       map[string]*operation // 操作 ID → 操作（进行中的与最近结束的）
	finished  []string              // 最近结束的操作 ID，超出上限时丢弃最老的
	restoring map[string]bool       // 正在写入的恢复目录
	scheduled int
	baidu     map[string]*baiduPending
}

// operation 是一次备份、校验或恢复。ID 与 backup_runs 里的记录一致。
type operation struct {
	ID         string
	Kind       string
	TargetID   string
	TargetName string
	Trigger    string
	Backup     string
	StartedAt  int64
	FinishedAt int64
	Status     string
	Error      string
	Result     any
	prog       *Progress
	cancel     context.CancelFunc
	done       chan struct{}
}

// New 创建引擎。Inst 为 nil 时只能用不依赖数据目录的函数（命令行直接对目标恢复时）。
func New(o Options) (*Engine, error) {
	if o.Inst == nil || o.Box == nil {
		return nil, errors.New("备份引擎缺少实例或凭据加密器")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.OpenTarget == nil {
		o.OpenTarget = target.Open
	}
	if o.MaxScheduled <= 0 {
		o.MaxScheduled = 2
	}
	if o.CacheRoot == "" && o.Inst.CacheDir != "" {
		o.CacheRoot = filepath.Dir(o.Inst.CacheDir)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{o: o, inst: o.Inst, st: o.Inst.Store, box: o.Box, log: o.Log, ctx: ctx, cancel: cancel,
		busy: map[string]*operation{}, ops: map[string]*operation{}, restoring: map[string]bool{}, baidu: map[string]*baiduPending{}}
	// 上次进程意外退出时留下的分卷临时文件、校验与恢复用的临时目录：这个目录只有备份引擎用，新进程里还没有任何操作，整个清掉。
	if o.Inst.CacheDir != "" {
		_ = os.RemoveAll(filepath.Join(o.Inst.CacheDir, "tmp", "backup"))
	}
	e.markInterrupted()
	return e, nil
}

func (e *Engine) now() time.Time { return e.o.Now() }

func (e *Engine) loc() *time.Location {
	if e.o.Location != nil {
		return e.o.Location
	}
	return time.Local
}

// Close 取消全部进行中的操作并等待它们结束（服务停止时调用，在关闭实例之前）。
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	e.wg.Wait()
}

// tempDir 返回本次操作用的临时目录根（缓存目录下）。
func (e *Engine) tempDir() (string, error) {
	dir := filepath.Join(e.inst.CacheDir, "tmp", "backup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建缓存临时目录失败：%w", err)
	}
	return dir, nil
}

// markInterrupted 把上次进程退出时还处于「进行中」的记录标成失败。
func (e *Engine) markInterrupted() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var runs []model.BackupRun
	if err := e.st.Select(ctx, &runs, "SELECT * FROM backup_runs WHERE status = ?", StatusRunning); err != nil {
		e.log.Warn("读取备份执行记录失败", "err", err)
		return
	}
	var targets []model.BackupTarget
	_ = e.st.Select(ctx, &targets, "SELECT * FROM backup_targets WHERE last_status = ?", StatusRunning)
	if len(runs) == 0 && len(targets) == 0 {
		return
	}
	now := e.now().UnixMilli()
	_, err := e.st.Write(ctx, "system", func(tx *store.Tx) error {
		for i := range runs {
			r := runs[i]
			r.Status, r.FinishedAt, r.Error = StatusFailed, model.Int64Ptr(now), "服务停止或重启时中断"
			if err := tx.Put("backup_runs", r); err != nil {
				return err
			}
		}
		for i := range targets {
			t := targets[i]
			t.LastStatus, t.UpdatedAt = StatusFailed, now
			if err := tx.Put("backup_targets", t); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		e.log.Warn("标记中断的备份记录失败", "err", err)
	}
}

// ---------------- 操作登记与互斥 ----------------

// begin 登记一个操作：同一目标同一时间只能有一个操作。
func (e *Engine) begin(spec *targetSpec, kind, trigger, backup string, p *Progress) (*operation, context.Context, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, nil, ErrClosed
	}
	if _, ok := e.busy[spec.row.ID]; ok {
		return nil, nil, ErrBusy
	}
	if p == nil {
		p = NewProgress()
	}
	ctx, cancel := context.WithCancel(e.ctx)
	op := &operation{ID: ids.New(), Kind: kind, TargetID: spec.row.ID, TargetName: spec.row.Name, Trigger: trigger, Backup: backup,
		StartedAt: e.now().UnixMilli(), Status: StatusRunning, prog: p, cancel: cancel, done: make(chan struct{})}
	e.busy[spec.row.ID] = op
	e.ops[op.ID] = op
	e.wg.Add(1)
	return op, ctx, nil
}

func (e *Engine) end(op *operation, status string, err error, result any) {
	e.mu.Lock()
	op.FinishedAt = e.now().UnixMilli()
	op.Status = status
	if err != nil {
		op.Error = errText(err)
	}
	op.Result = result
	delete(e.busy, op.TargetID)
	if op.Trigger == triggerSchedule && e.scheduled > 0 {
		e.scheduled--
	}
	e.finished = append(e.finished, op.ID)
	if len(e.finished) > 50 {
		delete(e.ops, e.finished[0])
		e.finished = e.finished[1:]
	}
	e.mu.Unlock()
	op.cancel()
	close(op.done)
	e.wg.Done()
}

// Cancel 取消一个进行中的操作（按操作 ID 或目标 ID）。没有进行中的操作返回 false。
func (e *Engine) Cancel(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if op, ok := e.busy[id]; ok {
		op.cancel()
		return true
	}
	if op, ok := e.ops[id]; ok && op.Status == StatusRunning {
		op.cancel()
		return true
	}
	return false
}

// Busy 返回目标上进行中的操作（没有为 nil）。
func (e *Engine) busyOp(targetID string) *operation {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy[targetID]
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "操作已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "操作超时"
	}
	return err.Error()
}

func statusOf(ctx context.Context, err error, p *Progress) string {
	switch {
	case err == nil && len(p.Warnings()) > 0:
		return StatusWarning
	case err == nil:
		return StatusSuccess
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return StatusCanceled
	default:
		return StatusFailed
	}
}

// 触发方式。
const (
	triggerManual   = "manual"
	triggerSchedule = "schedule"
	triggerCLI      = "cli"
)

func triggerText(trigger, actor string) string {
	switch trigger {
	case triggerSchedule:
		return "计划任务"
	case triggerCLI:
		return "命令行"
	default:
		if actor != "" {
			return "手动（用户 " + actor + "）"
		}
		return "手动"
	}
}

// RunOptions 是开始一次操作时的参数。
type RunOptions struct {
	Trigger  string    // manual | schedule | cli，默认 manual
	Actor    string    // 发起操作的用户 ID
	Progress *Progress // 为 nil 时新建
}

// ---------------- 记录 ----------------

func (e *Engine) putRun(r model.BackupRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.st.Write(ctx, "system", func(tx *store.Tx) error { return tx.Put("backup_runs", r) }); err != nil {
		e.log.Warn("写备份执行记录失败", "err", err)
	}
}

// touchTarget 更新目标的最近执行时间与状态（startedAt 为 nil 时只改状态）。
func (e *Engine) touchTarget(id string, startedAt *int64, status string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := e.st.Write(ctx, "system", func(tx *store.Tx) error {
		var row model.BackupTarget
		if err := tx.Get(&row, "SELECT * FROM backup_targets WHERE id = ?", id); err != nil {
			return nil // 目标已被删除
		}
		if startedAt != nil {
			row.LastRunAt = startedAt
		}
		row.LastStatus = status
		return tx.Put("backup_targets", row)
	})
	if err != nil {
		e.log.Warn("更新备份目标状态失败", "err", err)
	}
}

// maxRunsPerTarget 每个目标最多保留的执行记录条数。
const maxRunsPerTarget = 200

func (e *Engine) pruneRuns(targetID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var old []string
	if err := e.st.Select(ctx, &old, "SELECT id FROM backup_runs WHERE target_id = ? ORDER BY started_at DESC LIMIT 100000 OFFSET ?",
		targetID, maxRunsPerTarget); err != nil || len(old) == 0 {
		return
	}
	if _, err := e.st.Write(ctx, "system", func(tx *store.Tx) error {
		for _, id := range old {
			if err := tx.Delete("backup_runs", id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		e.log.Warn("清理旧的备份执行记录失败", "err", err)
	}
}

// ---------------- 备份 ----------------

// StartBackup 在后台开始一次备份，返回执行记录 ID。同一目标正在执行时返回 ErrBusy。
func (e *Engine) StartBackup(ctx context.Context, targetID string, o RunOptions) (string, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return "", err
	}
	op, octx, err := e.begin(spec, spec.row.Mode, defaultTrigger(o.Trigger), "", o.Progress)
	if err != nil {
		return "", err
	}
	go e.runBackup(octx, op, spec, o.Actor)
	return op.ID, nil
}

// RunBackup 同步执行一次备份（命令行与测试用），返回执行记录。
func (e *Engine) RunBackup(ctx context.Context, targetID string, o RunOptions) (*model.BackupRun, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return nil, err
	}
	op, octx, err := e.begin(spec, spec.row.Mode, defaultTrigger(o.Trigger), "", o.Progress)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, op.cancel)
	defer stop()
	run := e.runBackup(octx, op, spec, o.Actor)
	if run.Status == StatusFailed || run.Status == StatusCanceled {
		return run, uerr(run.Error)
	}
	return run, nil
}

func defaultTrigger(t string) string {
	if t == "" {
		return triggerManual
	}
	return t
}

func (e *Engine) runBackup(ctx context.Context, op *operation, spec *targetSpec, actor string) (run *model.BackupRun) {
	p := op.prog
	row := model.BackupRun{ID: op.ID, TargetID: spec.row.ID, Kind: spec.row.Mode, StartedAt: op.StartedAt, Status: StatusRunning}
	var res *runResult
	var err error
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("备份过程中发生内部错误：%v", v)
			e.log.Error("备份崩溃", "target", spec.row.Name, "panic", v)
		}
		status := statusOf(ctx, err, p)
		if err != nil {
			p.Logf("失败：%s", errText(err))
		}
		row.Status = status
		row.FinishedAt = model.Int64Ptr(e.now().UnixMilli())
		row.Error = errText(err)
		if err == nil && status == StatusWarning {
			row.Error = fmt.Sprintf("完成，但有 %d 条警告，详见日志", len(p.Warnings()))
		}
		if res != nil {
			row.Files, row.Bytes, row.Artifact = res.Files, res.Bytes, res.Artifact
		}
		row.Log = p.Text()
		e.putRun(row)
		e.touchTarget(spec.row.ID, nil, status)
		e.pruneRuns(spec.row.ID)
		if err != nil {
			e.log.Warn("备份失败", "target", spec.row.Name, "mode", spec.row.Mode, "err", errText(err))
		} else {
			e.log.Info("备份完成", "target", spec.row.Name, "mode", spec.row.Mode, "files", row.Files, "bytes", row.Bytes, "artifact", row.Artifact)
		}
		e.end(op, status, err, nil)
		run = &row
	}()
	e.putRun(row)
	e.touchTarget(spec.row.ID, &row.StartedAt, StatusRunning)
	p.Logf("开始%s备份到「%s」，触发方式：%s", modeText(spec.row.Mode), spec.row.Name, triggerText(op.Trigger, actor))
	var t target.Target
	t, err = e.openDriver(spec)
	if err != nil {
		return
	}
	defer t.Close()
	p.SetPhase("检查目标与加密设置")
	var key []byte
	var hdr *Header
	key, hdr, err = prepareBackupKey(ctx, t, spec.enc.Enabled, spec.password, p)
	if err != nil {
		return
	}
	if key != nil {
		p.Logf("备份内容将加密")
	}
	switch spec.row.Mode {
	case ModeMirror:
		res, err = e.backupMirror(ctx, p, t, key, workersFor(spec.row.Kind))
	default:
		res, err = e.backupFull(ctx, p, t, key, spec.ret)
	}
	if err == nil {
		err = checkHeaderUnchanged(ctx, t, hdr)
	}
	if err == nil {
		p.SetPhase("完成")
	}
	return
}

func modeText(mode string) string {
	if mode == ModeMirror {
		return "增量镜像"
	}
	return "全量"
}

func workersFor(kind string) int {
	if kind == target.KindBaidu {
		return 2
	}
	return 4
}

// ---------------- 校验与恢复 ----------------

// StartVerify 在后台校验目标上的一个备份，返回执行记录 ID；结果用 Run 查询。password 为空时用目标保存的口令。
func (e *Engine) StartVerify(ctx context.Context, targetID, name string, vo VerifyOptions, password string, o RunOptions) (string, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return "", err
	}
	op, octx, err := e.begin(spec, RunVerify, defaultTrigger(o.Trigger), name, o.Progress)
	if err != nil {
		return "", err
	}
	go e.runVerify(octx, op, spec, name, vo, password, o.Actor)
	return op.ID, nil
}

// VerifyNow 同步校验（命令行与测试用）。
func (e *Engine) VerifyNow(ctx context.Context, targetID, name string, vo VerifyOptions, password string, o RunOptions) (*VerifyResult, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return nil, err
	}
	op, octx, err := e.begin(spec, RunVerify, defaultTrigger(o.Trigger), name, o.Progress)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, op.cancel)
	defer stop()
	return e.runVerify(octx, op, spec, name, vo, password, o.Actor)
}

func (e *Engine) runVerify(ctx context.Context, op *operation, spec *targetSpec, name string, vo VerifyOptions, password, actor string) (res *VerifyResult, err error) {
	p := op.prog
	row := model.BackupRun{ID: op.ID, TargetID: spec.row.ID, Kind: RunVerify, StartedAt: op.StartedAt, Status: StatusRunning, Artifact: name}
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("校验过程中发生内部错误：%v", v)
		}
		status := statusOf(ctx, err, p)
		if err == nil && res != nil && !res.OK {
			status = StatusFailed
			row.Error = "校验未通过：" + strings.Join(firstN(res.Problems, 5), "；")
		} else {
			row.Error = errText(err)
		}
		if res != nil {
			row.Files, row.Bytes = res.Files, res.Bytes
			for _, pr := range res.Problems {
				p.Logf("问题：%s", pr)
			}
			if res.OK {
				p.Logf("校验通过：%d 个文件，%s，附件 %d 个（核对内容 %d 个）", res.Files, humanBytes(res.Bytes), res.Blobs, res.Hashed)
			}
		} else if err != nil {
			p.Logf("失败：%s", errText(err))
		}
		row.Status = status
		row.FinishedAt = model.Int64Ptr(e.now().UnixMilli())
		row.Log = p.Text()
		e.putRun(row)
		e.pruneRuns(spec.row.ID)
		var result any
		if res != nil {
			result = res
		}
		e.end(op, status, err, result)
	}()
	e.putRun(row)
	p.Logf("开始校验「%s」上的备份 %s，触发方式：%s", spec.row.Name, name, triggerText(op.Trigger, actor))
	t, err := e.openDriver(spec)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	if password == "" {
		password = spec.password
	}
	key, _, err := OpenKey(ctx, t, password)
	if err != nil {
		return nil, err
	}
	if vo.TempDir == "" {
		if vo.TempDir, err = e.tempDir(); err != nil {
			return nil, err
		}
	}
	return Verify(ctx, t, name, key, vo, p)
}

// StartRestore 在后台把目标上的一个备份恢复到新的空目录 dest，返回执行记录 ID；结果用 Run 查询。
func (e *Engine) StartRestore(ctx context.Context, targetID, name, dest, password string, o RunOptions) (string, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return "", err
	}
	dest, err = e.checkRestoreDest(dest)
	if err != nil {
		return "", err
	}
	if err := e.claimDest(dest); err != nil {
		return "", err
	}
	op, octx, err := e.begin(spec, RunRestore, defaultTrigger(o.Trigger), name, o.Progress)
	if err != nil {
		e.releaseDest(dest)
		return "", err
	}
	go func() {
		defer e.releaseDest(dest)
		e.runRestore(octx, op, spec, name, dest, password, o.Actor)
	}()
	return op.ID, nil
}

// RestoreNow 同步恢复（命令行与测试用）。
func (e *Engine) RestoreNow(ctx context.Context, targetID, name, dest, password string, o RunOptions) (*RestoreResult, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return nil, err
	}
	dest, err = e.checkRestoreDest(dest)
	if err != nil {
		return nil, err
	}
	if err := e.claimDest(dest); err != nil {
		return nil, err
	}
	defer e.releaseDest(dest)
	op, octx, err := e.begin(spec, RunRestore, defaultTrigger(o.Trigger), name, o.Progress)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, op.cancel)
	defer stop()
	return e.runRestore(octx, op, spec, name, dest, password, o.Actor)
}

func (e *Engine) runRestore(ctx context.Context, op *operation, spec *targetSpec, name, dest, password, actor string) (res *RestoreResult, err error) {
	p := op.prog
	row := model.BackupRun{ID: op.ID, TargetID: spec.row.ID, Kind: RunRestore, StartedAt: op.StartedAt, Status: StatusRunning, Artifact: name}
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("恢复过程中发生内部错误：%v", v)
		}
		status := statusOf(ctx, err, p)
		row.Error = errText(err)
		if res != nil {
			row.Files, row.Bytes = res.Files, res.Bytes
			if op.Trigger != triggerCLI { // 命令行自己打印提示（覆盖当前数据目录时提示不同）
				res.Hint = e.RestoreHint(res)
				p.Logf("%s", res.Hint)
			}
		} else if err != nil {
			p.Logf("失败：%s", errText(err))
		}
		row.Status = status
		row.FinishedAt = model.Int64Ptr(e.now().UnixMilli())
		row.Log = p.Text()
		e.putRun(row)
		e.pruneRuns(spec.row.ID)
		var result any
		if res != nil {
			result = res
		}
		e.end(op, status, err, result)
	}()
	e.putRun(row)
	p.Logf("开始把「%s」上的备份 %s 恢复到 %s，触发方式：%s", spec.row.Name, name, dest, triggerText(op.Trigger, actor))
	t, err := e.openDriver(spec)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	if password == "" {
		password = spec.password
	}
	key, _, err := OpenKey(ctx, t, password)
	if err != nil {
		return nil, err
	}
	tmp, err := e.tempDir()
	if err != nil {
		return nil, err
	}
	return Restore(ctx, t, name, dest, key, RestoreOptions{TempDir: tmp}, p)
}

// RestoreHint 返回恢复完成后给用户的提示。
func (e *Engine) RestoreHint(res *RestoreResult) string {
	where := "引导配置文件"
	if e != nil && e.o.ConfigPath != "" {
		where = "引导配置文件 " + e.o.ConfigPath + " "
	}
	return fmt.Sprintf("已恢复到 %s。请在%s里把 data_dir 改为这个目录（或用 --data-dir 指定），然后重启服务；原来的数据目录不会被改动。", res.Dir, where)
}

// checkRestoreDest 确认恢复目录是绝对路径，而且不在当前数据目录、缓存目录里面，也不包含它们。
func (e *Engine) checkRestoreDest(dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return "", uerr("请填写恢复到的目录")
	}
	if !filepath.IsAbs(dest) {
		return "", uerrf("恢复目录「%s」必须是绝对路径", dest)
	}
	dest = filepath.Clean(dest)
	for _, p := range []string{e.inst.Dir.Root, e.o.CacheRoot} {
		if p == "" {
			continue
		}
		if within(dest, p) || within(p, dest) {
			return "", uerrf("恢复目录「%s」不能与当前数据目录或缓存目录重叠，请选择一个新的空目录", dest)
		}
	}
	return dest, nil
}

// within 判断 path 是否等于 dir 或在 dir 里面。
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (e *Engine) claimDest(dest string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.restoring[dest] {
		return uerrf("已经有一个恢复任务正在写入「%s」", dest)
	}
	e.restoring[dest] = true
	return nil
}

func (e *Engine) releaseDest(dest string) {
	e.mu.Lock()
	delete(e.restoring, dest)
	e.mu.Unlock()
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------------- 查询 ----------------

// RunView 是执行记录加上进行中的进度与结果。
type RunView struct {
	model.BackupRun
	TargetName string        `json:"target_name"`
	Running    bool          `json:"running"`
	Progress   *ProgressView `json:"progress,omitempty"`
	Result     any           `json:"result,omitempty"`
}

func (e *Engine) attach(v *RunView) {
	e.mu.Lock()
	op := e.ops[v.ID]
	var running bool
	var result any
	if op != nil {
		running, result = op.Status == StatusRunning, op.Result
	}
	e.mu.Unlock()
	if op == nil {
		return
	}
	if running {
		v.Running = true
		pv := op.prog.View()
		v.Progress = &pv
		v.Log = op.prog.Text()
	}
	v.Result = result
}

// Runs 列出执行记录（新的在前）。targetID 为空列出全部。
func (e *Engine) Runs(ctx context.Context, targetID string, limit int) ([]RunView, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var rows []model.BackupRun
	var err error
	if targetID != "" {
		err = e.st.Select(ctx, &rows, "SELECT * FROM backup_runs WHERE target_id = ? ORDER BY started_at DESC LIMIT ?", targetID, limit)
	} else {
		err = e.st.Select(ctx, &rows, "SELECT * FROM backup_runs ORDER BY started_at DESC LIMIT ?", limit)
	}
	if err != nil {
		return nil, err
	}
	names := e.targetNames(ctx)
	out := make([]RunView, 0, len(rows))
	for _, r := range rows {
		r.Log = "" // 列表里不带日志全文
		v := RunView{BackupRun: r, TargetName: names[r.TargetID]}
		e.attach(&v)
		v.Log = ""
		out = append(out, v)
	}
	return out, nil
}

// Run 返回一条执行记录（含日志、进度与结果）。
func (e *Engine) Run(ctx context.Context, id string) (*RunView, error) {
	var r model.BackupRun
	if err := e.st.Get(ctx, &r, "SELECT * FROM backup_runs WHERE id = ?", id); err != nil {
		// 记录可能因为数据库只读没写进去：退回内存里的操作。
		e.mu.Lock()
		op := e.ops[id]
		if op != nil {
			r = model.BackupRun{ID: op.ID, TargetID: op.TargetID, Kind: op.Kind, StartedAt: op.StartedAt, Status: op.Status,
				Error: op.Error, Artifact: op.Backup}
			if op.FinishedAt > 0 {
				r.FinishedAt = model.Int64Ptr(op.FinishedAt)
			}
		}
		e.mu.Unlock()
		if op == nil {
			return nil, uerr("执行记录不存在")
		}
		r.Log = op.prog.Text()
	}
	v := &RunView{BackupRun: r, TargetName: e.targetNames(ctx)[r.TargetID]}
	e.attach(v)
	return v, nil
}

// Wait 等待一个操作结束（测试与命令行用）。
func (e *Engine) Wait(id string) {
	e.mu.Lock()
	op := e.ops[id]
	e.mu.Unlock()
	if op != nil {
		<-op.done
	}
}

func (e *Engine) targetNames(ctx context.Context) map[string]string {
	var rows []model.BackupTarget
	_ = e.st.Select(ctx, &rows, "SELECT * FROM backup_targets")
	m := map[string]string{}
	for _, r := range rows {
		m[r.ID] = r.Name
	}
	return m
}

// ListTargetBackups 列出目标上的备份。password 为空时用目标保存的口令（读不到详细信息不算错误）。
func (e *Engine) ListTargetBackups(ctx context.Context, targetID, password string) ([]BackupInfo, error) {
	spec, err := e.loadSpec(ctx, targetID)
	if err != nil {
		return nil, err
	}
	t, err := e.openDriver(spec)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	if password == "" {
		password = spec.password
	}
	key, hdr, err := OpenKey(ctx, t, password)
	if err != nil && !errors.Is(err, ErrNeedPassword) && !errors.Is(err, ErrWrongPassword) {
		return nil, err
	}
	list, err := ListBackups(ctx, t, key, hdr, e.inst.Info.InstanceID)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []BackupInfo{}
	}
	return list, nil
}
