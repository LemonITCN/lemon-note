// Package importer 在后台任务里把外部内容导入成页面树：Notion 导出包、Notion API、Obsidian 库、
// 通用 Markdown 与 CSV。进度与报告写进本机表 import_jobs（不进日志）。
//
// 导入分两步：先读来源，生成一棵「计划树」并预先分配全部页面 id（这样正文里的页面链接、关联属性可以
// 直接写成最终 id）；再按层级分批建页。页面服务按内存目录校验上级页面，目录只在事务提交后更新，
// 所以上级必须先提交，下级在后面的批次里建。
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// 导入来源
const (
	KindNotionZip = "notion_zip"
	KindNotionAPI = "notion_api"
	KindObsidian  = "obsidian"
	KindMarkdown  = "markdown"
	KindCSV       = "csv"
)

// ValidFileKind 判断是不是上传文件类的来源。
func ValidFileKind(k string) bool {
	switch k {
	case KindNotionZip, KindObsidian, KindMarkdown, KindCSV:
		return true
	}
	return false
}

// 任务状态
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Limits 是导入的各种上限（防 zip 炸弹、防超大导入拖垮服务）。
type Limits struct {
	MaxPackageBytes int64 // 上传的导入包大小
	MaxTotalBytes   int64 // 解压后的总大小（含内层 zip）
	MaxEntries      int   // 文件数（含内层 zip 里的）
	MaxTextBytes    int64 // 单个 Markdown / CSV / Canvas 文件
	MaxNestedZips   int   // 内层 zip 个数
	MaxPages        int   // 一次导入最多新建多少页面（含数据库行）
	MaxCSVRows      int   // 单个 CSV 的行数
}

// DefaultLimits 默认上限。
var DefaultLimits = Limits{
	MaxPackageBytes: 4 << 30,
	MaxTotalBytes:   16 << 30,
	MaxEntries:      200000,
	MaxTextBytes:    32 << 20,
	MaxNestedZips:   200,
	MaxPages:        100000,
	MaxCSVRows:      100000,
}

// Target 是导入的目标位置：空间根，或某个页面下。
type Target struct {
	SpaceID  string
	ParentID string
}

// Progress 是任务进度。
type Progress struct {
	Phase   string `json:"phase"` // queued | reading | creating | done | failed
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current,omitempty"`
}

// Warning 是一条降级或跳过记录。Code 是稳定的英文标识（前端据此翻译），Message 是中文说明。
type Warning struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Message string `json:"message"`
}

// Report 是任务报告。
type Report struct {
	Pages     int       `json:"pages"`
	Databases int       `json:"databases"`
	Rows      int       `json:"rows"`
	Files     int       `json:"files"`
	RootIDs   []string  `json:"root_ids"`
	Warnings  []Warning `json:"warnings"`
	Omitted   int       `json:"omitted_warnings,omitempty"` // 超过上限没有列出的条数
	Error     string    `json:"error,omitempty"`
}

const maxWarnings = 500

// JobView 是返回给前端的任务。
type JobView struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Progress  Progress `json:"progress"`
	Report    *Report  `json:"report,omitempty"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

// jobRow 对应 import_jobs 表。
type jobRow struct {
	ID        string `db:"id"`
	UserID    string `db:"user_id"`
	Kind      string `db:"kind"`
	Status    string `db:"status"`
	Progress  string `db:"progress"`
	Report    string `db:"report"`
	CreatedAt int64  `db:"created_at"`
	UpdatedAt int64  `db:"updated_at"`
}

// job 是运行中的任务（内存里的最新状态）。
type job struct {
	mu        sync.Mutex
	id        string
	userID    string
	kind      string
	status    string
	progress  Progress
	report    Report
	createdAt int64
	updatedAt int64
	lastSave  time.Time
	warnSeen  map[string]bool
}

func (j *job) view() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	rep := j.report
	rep.Warnings = append([]Warning{}, j.report.Warnings...)
	rep.RootIDs = append([]string{}, j.report.RootIDs...)
	v := JobView{ID: j.id, Kind: j.kind, Status: j.status, Progress: j.progress, CreatedAt: j.createdAt, UpdatedAt: j.updatedAt}
	v.Report = &rep
	return v
}

// warn 记录一条降级或跳过（同一条只记一次，超过上限只计数）。
func (j *job) warn(w Warning) {
	j.mu.Lock()
	defer j.mu.Unlock()
	k := w.Code + "\x00" + w.Path + "\x00" + w.Detail
	if j.warnSeen[k] {
		return
	}
	j.warnSeen[k] = true
	if len(j.report.Warnings) >= maxWarnings {
		j.report.Omitted++
		return
	}
	j.report.Warnings = append(j.report.Warnings, w)
}

// Service 是导入服务。
type Service struct {
	st     *store.Store
	cat    *catalog.Catalog
	pages  *pages.Service
	dbs    *dbs.Service
	files  *files.Service
	set    *settings.Service
	log    *slog.Logger
	tmpDir string

	// Limits 各种上限（测试里可以调小）
	Limits Limits
	// NotionBaseURL Notion API 地址（测试里换成模拟服务器）
	NotionBaseURL string
	// NotionInterval 两次 Notion 请求之间的最小间隔（Notion 限速约每秒 3 次）
	NotionInterval time.Duration

	mu     sync.Mutex
	jobs   map[string]*job
	wg     sync.WaitGroup
	base   context.Context
	cancel context.CancelFunc
}

// New 创建导入服务。cacheDir 是缓存目录（上传的导入包、内层 zip 临时放在这里）。
func New(st *store.Store, cat *catalog.Catalog, pg *pages.Service, db *dbs.Service, fs *files.Service, set *settings.Service, cacheDir string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	base, cancel := context.WithCancel(context.Background())
	tmp := filepath.Join(cacheDir, "tmp", "import")
	// 上次运行留下的临时文件（服务启动时不会有任务在跑）
	if ents, err := os.ReadDir(tmp); err == nil {
		for _, e := range ents {
			os.Remove(filepath.Join(tmp, e.Name()))
		}
	}
	return &Service{st: st, cat: cat, pages: pg, dbs: db, files: fs, set: set, log: log, tmpDir: filepath.Join(cacheDir, "tmp", "import"),
		Limits: DefaultLimits, NotionBaseURL: "https://api.notion.com", NotionInterval: 350 * time.Millisecond, jobs: map[string]*job{},
		base: base, cancel: cancel}
}

// Stop 中断运行中的任务并等它们退出（服务停止时调用）。
func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()
}

// ErrBusy 表示同一用户已有导入任务在进行。
var ErrBusy = errors.New("已有导入任务正在进行，请等它完成后再试")

// ErrServerBusy 表示整个服务同时进行的导入太多。
var ErrServerBusy = errors.New("服务器上正在进行的导入任务较多，请稍后再试")

// maxRunning 整个服务同时进行的导入任务上限。
const maxRunning = 3

// TempFile 在缓存目录里建一个临时文件（上传的导入包先落到这里）。
func (s *Service) TempFile() (*os.File, error) {
	if err := os.MkdirAll(s.tmpDir, 0o700); err != nil {
		return nil, err
	}
	return os.CreateTemp(s.tmpDir, "~$up-*.tmp")
}

// newJob 建任务记录。同一用户同时只允许一个任务。
func (s *Service) newJob(ctx context.Context, userID, kind string) (*job, error) {
	s.mu.Lock()
	running := 0
	for _, j := range s.jobs {
		j.mu.Lock()
		active := j.status == StatusQueued || j.status == StatusRunning
		mine := j.userID == userID
		j.mu.Unlock()
		if active && mine {
			s.mu.Unlock()
			return nil, ErrBusy
		}
		if active {
			running++
		}
	}
	if running >= maxRunning {
		s.mu.Unlock()
		return nil, ErrServerBusy
	}
	now := model.Now()
	j := &job{id: ids.New(), userID: userID, kind: kind, status: StatusQueued, progress: Progress{Phase: "queued"},
		createdAt: now, updatedAt: now, warnSeen: map[string]bool{}, report: Report{RootIDs: []string{}, Warnings: []Warning{}}}
	s.jobs[j.id] = j
	s.mu.Unlock()
	if err := s.save(ctx, j); err != nil {
		s.mu.Lock()
		delete(s.jobs, j.id)
		s.mu.Unlock()
		return nil, err
	}
	s.cleanupOld(ctx)
	return j, nil
}

// save 把任务状态写进 import_jobs（本机表，不进日志）。不能在写事务里调用。
func (s *Service) save(ctx context.Context, j *job) error {
	v := j.view()
	p, _ := json.Marshal(v.Progress)
	r, _ := json.Marshal(v.Report)
	j.mu.Lock()
	j.lastSave = time.Now()
	j.mu.Unlock()
	return s.st.PutLocal(context.WithoutCancel(ctx), "import_jobs", jobRow{ID: v.ID, UserID: j.userID, Kind: v.Kind, Status: v.Status,
		Progress: string(p), Report: string(r), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt})
}

// touch 更新进度（最多每秒落盘一次）。
func (s *Service) touch(ctx context.Context, j *job, fn func(p *Progress)) {
	j.mu.Lock()
	fn(&j.progress)
	j.updatedAt = model.Now()
	due := time.Since(j.lastSave) > time.Second
	j.mu.Unlock()
	if due {
		if err := s.save(ctx, j); err != nil {
			s.log.Warn("保存导入进度失败", "job", j.id, "err", err)
		}
	}
}

// cleanupOld 删除 30 天前的任务记录。
func (s *Service) cleanupOld(ctx context.Context) {
	cut := time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	if _, err := s.st.ExecLocal(ctx, "DELETE FROM import_jobs WHERE updated_at < ? AND status IN ('done', 'failed')", cut); err != nil {
		s.log.Debug("清理旧导入任务失败", "err", err)
	}
}

// Get 读任务（只能看自己的）。
func (s *Service) Get(ctx context.Context, userID, id string) (*JobView, error) {
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j != nil {
		if j.userID != userID {
			return nil, ErrNotFound
		}
		v := j.view()
		return &v, nil
	}
	var row jobRow
	if err := s.st.Get(ctx, &row, "SELECT * FROM import_jobs WHERE id = ?", id); err != nil || row.UserID != userID {
		return nil, ErrNotFound
	}
	v := JobView{ID: row.ID, Kind: row.Kind, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	_ = json.Unmarshal([]byte(row.Progress), &v.Progress)
	var rep Report
	if json.Unmarshal([]byte(row.Report), &rep) == nil {
		v.Report = &rep
	}
	if v.Status == StatusQueued || v.Status == StatusRunning {
		// 内存里没有却还在运行：服务重启过，任务已中断
		v.Status = StatusFailed
		v.Progress.Phase = "failed"
		if v.Report == nil {
			v.Report = &Report{}
		}
		v.Report.Error = "服务重启，导入已中断（已导入的部分保留）"
	}
	return &v, nil
}

// ErrNotFound 任务不存在。
var ErrNotFound = errors.New("导入任务不存在")

// Wait 等所有任务结束（测试与停机用）。
func (s *Service) Wait() { s.wg.Wait() }

// work 是一种来源的导入过程：读来源、生成计划树，交给 builder 建页。
type work func(ctx context.Context, b *builder) error

// start 在后台执行任务。cleanup 在结束后调用（删临时文件）。
func (s *Service) start(j *job, actor string, tgt Target, cleanup func(), fn work) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if cleanup != nil {
			defer cleanup()
		}
		ctx, cancel := context.WithTimeout(s.base, 12*time.Hour)
		defer cancel()
		j.mu.Lock()
		j.status = StatusRunning
		j.progress.Phase = "reading"
		j.mu.Unlock()
		_ = s.save(ctx, j)
		err := func() (err error) {
			defer func() {
				if v := recover(); v != nil {
					s.log.Error("导入任务崩溃", "job", j.id, "panic", v, "stack", string(debug.Stack()))
					err = errors.New("导入过程出现内部错误")
				}
			}()
			b, err := s.newBuilder(ctx, j, actor, tgt)
			if err != nil {
				return err
			}
			return fn(ctx, b)
		}()
		j.mu.Lock()
		j.updatedAt = model.Now()
		if err != nil {
			j.status = StatusFailed
			j.progress.Phase = "failed"
			j.report.Error = userMessage(err)
			s.log.Warn("导入失败", "job", j.id, "kind", j.kind, "err", err)
		} else {
			j.status = StatusDone
			j.progress.Phase = "done"
			j.progress.Current = ""
		}
		j.mu.Unlock()
		if err := s.save(ctx, j); err != nil {
			s.log.Warn("保存导入结果失败", "job", j.id, "err", err)
		}
		// 任务结束后留在内存里一会儿，前端轮询能拿到最终结果；之后只从数据库读
		time.AfterFunc(10*time.Minute, func() {
			s.mu.Lock()
			delete(s.jobs, j.id)
			s.mu.Unlock()
		})
	}()
}

// userMessage 把错误转成给用户看的说明：本包产生的中文错误原样给出，底层错误不外泄细节。
func userMessage(err error) string {
	var ue *userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	var ae *abortError
	if errors.As(err, &ae) {
		return ae.msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "导入超时，已导入的部分保留"
	}
	if errors.Is(err, context.Canceled) {
		return "服务停止，导入已中断（已导入的部分保留）"
	}
	msg := err.Error()
	for _, bad := range []string{"sql", "SQL", "database", "/", "\\"} {
		if strings.Contains(msg, bad) {
			return "导入过程出错，详情见服务日志（已导入的部分保留）"
		}
	}
	for _, r := range msg {
		if r >= 0x4e00 && r <= 0x9fff {
			return msg
		}
	}
	return "导入过程出错，详情见服务日志（已导入的部分保留）"
}

// userError 是可以直接展示给用户的错误。
type userError struct{ msg string }

func (e *userError) Error() string    { return e.msg }
func (e *userError) UserFacing() bool { return true }

func fail(format string, a ...any) error { return &userError{msg: fmt.Sprintf(format, a...)} }
