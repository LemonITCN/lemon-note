// Package engine 是备份引擎：备份目标管理、全量备份、增量镜像、计划调度、恢复与校验。
//
// 目标上的布局（相对目标根目录，含配置里的子目录 / prefix）：
//
//	lemon-note-backup.json                         目标头：格式、加密参数（盐与口令校验值），口令本身不落地
//	lemon-note-<实例前8位>-<YYYYMMDD-HHMMSS>.lnbak  全量包（只有一个分卷时就是整个包；时间是 UTC）
//	lemon-note-<实例前8位>-<时间>.lnbak.0001 …      全量包超过分卷大小时，前面的分卷；.lnbak 永远是最后一卷、最后上传，
//	                                               它存在才表示这个包完整。手工恢复：cat 各分卷 .lnbak.0001 … .lnbak | zstd -d | tar -x
//	mirror-<实例前8位>/                             增量镜像：与数据目录同结构（不含 locks/），不加密时可以直接当数据目录打开
//	lemon-note-<实例前8位>-mirror.json              镜像指针，列出备份时不用遍历镜像目录就能发现它
//
// 加密时全量包整体加密（cryptstream），镜像逐个文件加密、文件名保持原样（文件名只是哈希与序号）。
package engine

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 备份模式。
const (
	ModeFull   = "full"
	ModeMirror = "mirror"
)

// 执行记录的类型（backup_runs.kind）。
const (
	RunFull    = "full"
	RunMirror  = "mirror"
	RunVerify  = "verify"
	RunRestore = "restore"
)

// 执行状态（backup_runs.status 与 backup_targets.last_status）。
const (
	StatusRunning  = "running"
	StatusSuccess  = "success"
	StatusWarning  = "warning" // 完成了，但有需要留意的问题（详见日志）
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
)

// Masked 是接口返回凭据字段时的打码值；保存时传回这个值表示「不修改」。
const Masked = "********"

// HeaderName 是目标根目录下的目标头文件。
const HeaderName = "lemon-note-backup.json"

// Error 是可以直接展示给用户的错误（中文、不含凭据）。
type Error struct {
	msg string
	err error
}

func (e *Error) Error() string {
	if e.err != nil && e.msg == "" {
		return e.err.Error()
	}
	return e.msg
}
func (e *Error) Unwrap() error { return e.err }

// UserFacing 让 HTTP 层把内容原样展示给用户。
func (e *Error) UserFacing() bool { return true }

func uerr(msg string) error { return &Error{msg: msg} }

func uerrf(format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	return &Error{msg: err.Error(), err: errors.Unwrap(err)}
}

// wrapUser 把下层（目标驱动、文件系统）返回的、本身可读的错误标记为可展示。
func wrapUser(err error) error {
	if err == nil {
		return nil
	}
	var ue *Error
	if errors.As(err, &ue) {
		return err
	}
	return &Error{msg: err.Error(), err: err}
}

var (
	// ErrBusy 表示同一目标正在执行别的操作。
	ErrBusy = uerr("这个备份目标正在执行备份、校验或恢复，请等它结束后再试")
	// ErrWrongPassword 表示备份口令与目标上记录的校验值不一致。
	ErrWrongPassword = uerr("备份口令不正确")
	// ErrNeedPassword 表示目标上的备份是加密的，但没有提供口令。
	ErrNeedPassword = uerr("这个备份目标上的备份是加密的，需要提供备份口令")
	// ErrTargetNotFound 表示备份目标不存在。
	ErrTargetNotFound = uerr("备份目标不存在")
	// ErrBackupNotFound 表示目标上没有这个备份。
	ErrBackupNotFound = uerr("目标上没有找到这个备份")
	// ErrClosed 表示引擎已停止。
	ErrClosed = uerr("服务正在停止，无法开始新的备份操作")
)

// ---------------- 计划 ----------------

// 计划类型。
const (
	ScheduleManual = "manual"
	ScheduleHourly = "hourly"
	ScheduleDaily  = "daily"
	ScheduleWeekly = "weekly"
)

// Schedule 是一个目标的执行计划。时间按服务所在机器的本地时区。
type Schedule struct {
	Kind       string `json:"kind"`                  // manual | hourly | daily | weekly
	EveryHours int    `json:"every_hours,omitempty"` // hourly：每 N 小时（1–168）
	At         string `json:"at,omitempty"`          // daily / weekly：几点，HH:MM；hourly：起算的分钟偏移（可选）
	Weekdays   []int  `json:"weekdays,omitempty"`    // weekly：周几，0 是周日 … 6 是周六
}

// DefaultSchedule 是没有指定计划时的默认值：每天凌晨 3 点。
func DefaultSchedule() Schedule { return Schedule{Kind: ScheduleDaily, At: "03:00"} }

func parseHM(s string) (h, m int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, uerrf("时间「%s」格式不对，应为 HH:MM", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, uerrf("时间「%s」格式不对，应为 HH:MM", s)
	}
	return h, m, nil
}

// Normalize 校验并补齐计划。
func (s *Schedule) Normalize() error {
	s.Kind = strings.TrimSpace(s.Kind)
	if s.Kind == "" {
		*s = DefaultSchedule()
		return nil
	}
	h, m, err := parseHM(s.At)
	if err != nil {
		return err
	}
	switch s.Kind {
	case ScheduleManual:
		*s = Schedule{Kind: ScheduleManual}
	case ScheduleHourly:
		if s.EveryHours < 1 || s.EveryHours > 168 {
			return uerr("每隔几小时执行一次需要在 1 到 168 之间")
		}
		s.Weekdays = nil
		s.At = fmt.Sprintf("%02d:%02d", h, m)
	case ScheduleDaily:
		s.EveryHours, s.Weekdays = 0, nil
		s.At = fmt.Sprintf("%02d:%02d", h, m)
	case ScheduleWeekly:
		s.EveryHours = 0
		if len(s.Weekdays) == 0 {
			return uerr("每周执行需要至少选择一天")
		}
		seen := map[int]bool{}
		var days []int
		for _, d := range s.Weekdays {
			if d < 0 || d > 6 {
				return uerr("星期只能是 0（周日）到 6（周六）")
			}
			if !seen[d] {
				seen[d] = true
				days = append(days, d)
			}
		}
		s.Weekdays = days
		s.At = fmt.Sprintf("%02d:%02d", h, m)
	default:
		return uerrf("不认识的计划类型「%s」", s.Kind)
	}
	return nil
}

// ---------------- 保留策略 ----------------

// 保留策略类型。
const (
	RetainLast   = "last"   // 保留最近 N 个
	RetainTiered = "tiered" // 按天 / 周 / 月分层
)

// 分卷大小上下限（MB）。百度网盘单文件最大 4 GB，所以上限取 4096。
const (
	DefaultVolumeMB = 1024
	MinVolumeMB     = 16
	MaxVolumeMB     = 4096
)

// Retention 是全量模式的保留策略，同时记录全量包的分卷大小（镜像模式不用）。
type Retention struct {
	Kind     string `json:"kind"`      // last | tiered
	KeepLast int    `json:"keep_last"` // last：保留最近几个
	Daily    int    `json:"daily"`     // tiered：最近几天每天留最新一个
	Weekly   int    `json:"weekly"`    // tiered：最近几周每周留最新一个
	Monthly  int    `json:"monthly"`   // tiered：最近几个月每月留最新一个
	VolumeMB int    `json:"volume_mb"` // 全量包分卷大小（MB）；单个文件超过这个大小就分卷上传
}

// DefaultRetention 是默认保留策略：保留最近 7 个，分卷 1 GB。
func DefaultRetention() Retention {
	return Retention{Kind: RetainLast, KeepLast: 7, VolumeMB: DefaultVolumeMB}
}

// Normalize 校验并补齐保留策略。
func (r *Retention) Normalize() error {
	if r.VolumeMB == 0 {
		r.VolumeMB = DefaultVolumeMB
	}
	if r.VolumeMB < MinVolumeMB || r.VolumeMB > MaxVolumeMB {
		return uerrf("分卷大小需要在 %d MB 到 %d MB 之间", MinVolumeMB, MaxVolumeMB)
	}
	switch strings.TrimSpace(r.Kind) {
	case "", RetainLast:
		r.Kind = RetainLast
		if r.KeepLast == 0 {
			r.KeepLast = 7
		}
		if r.KeepLast < 1 || r.KeepLast > 1000 {
			return uerr("保留个数需要在 1 到 1000 之间")
		}
		r.Daily, r.Weekly, r.Monthly = 0, 0, 0
	case RetainTiered:
		r.Kind = RetainTiered
		r.KeepLast = 0
		for _, v := range []int{r.Daily, r.Weekly, r.Monthly} {
			if v < 0 || v > 1000 {
				return uerr("按天 / 周 / 月保留的个数需要在 0 到 1000 之间")
			}
		}
		if r.Daily+r.Weekly+r.Monthly == 0 {
			return uerr("分层保留至少要保留一天、一周或一个月")
		}
	default:
		return uerrf("不认识的保留策略「%s」", r.Kind)
	}
	return nil
}

// ---------------- 加密设置 ----------------

// encStored 是 backup_targets.encryption 里存的内容。口令用数据目录密钥（crypt.Box）加密后存放，
// 只为了让计划任务能自动执行；目标上的 lemon-note-backup.json 只记录盐与口令校验值。
type encStored struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password,omitempty"` // crypt.Box 密文
}

// EncryptionView 是接口返回的加密设置（不含口令）。
type EncryptionView struct {
	Enabled     bool `json:"enabled"`
	HasPassword bool `json:"has_password"`
}

// EncryptionInput 是保存目标时的加密设置。Password 为空或为打码值表示不修改。
type EncryptionInput struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"`
}

// MinPasswordLen 是备份口令的最短长度。
const MinPasswordLen = 8

// ---------------- 名称 ----------------

const pkgTimeLayout = "20060102-150405"

var (
	// lemon-note-<id8>-<YYYYMMDD-HHMMSS>.lnbak[.NNNN]
	pkgRe     = regexp.MustCompile(`^lemon-note-([0-9a-z]{8})-([0-9]{8}-[0-9]{6})\.lnbak(?:\.([0-9]{4,6}))?$`)
	pointerRe = regexp.MustCompile(`^lemon-note-([0-9a-z]{8})-mirror\.json$`)
	mirrorRe  = regexp.MustCompile(`^mirror-([0-9a-z]{8})$`)
)

func instanceShort(id string) string {
	if len(id) >= 8 {
		return strings.ToLower(id[:8])
	}
	return strings.ToLower(id)
}

func packageBase(id8 string, t time.Time) string {
	return "lemon-note-" + id8 + "-" + t.UTC().Format(pkgTimeLayout)
}

func tailName(base string) string { return base + ".lnbak" }

func partName(base string, n int) string { return fmt.Sprintf("%s.lnbak.%04d", base, n) }

func mirrorDir(id8 string) string { return "mirror-" + id8 }

func pointerName(id8 string) string { return "lemon-note-" + id8 + "-mirror.json" }
