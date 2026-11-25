package engine

import (
	"context"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
)

// hourlyAnchor 是「每 N 小时」计划的起算点（本地时间 2000-01-01 00:00 加上 At 的偏移），
// 计划时间点 = 起算点 + k × N 小时，与服务何时启动无关。
func hourlyAnchor(s Schedule, loc *time.Location) time.Time {
	h, m, _ := parseHM(s.At)
	return time.Date(2000, 1, 1, h, m, 0, 0, loc)
}

// Last 返回不晚于 now 的最近一个计划时间点；手动计划返回零值。
func (s Schedule) Last(now time.Time, loc *time.Location) time.Time {
	now = now.In(loc)
	h, m, _ := parseHM(s.At)
	switch s.Kind {
	case ScheduleHourly:
		if s.EveryHours < 1 {
			return time.Time{}
		}
		anchor := hourlyAnchor(s, loc)
		period := time.Duration(s.EveryHours) * time.Hour
		if now.Before(anchor) {
			return time.Time{}
		}
		k := now.Sub(anchor) / period
		return anchor.Add(k * period)
	case ScheduleDaily:
		t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
		if t.After(now) {
			t = time.Date(now.Year(), now.Month(), now.Day()-1, h, m, 0, 0, loc)
		}
		return t
	case ScheduleWeekly:
		days := map[int]bool{}
		for _, d := range s.Weekdays {
			days[d] = true
		}
		for i := 0; i <= 7; i++ {
			t := time.Date(now.Year(), now.Month(), now.Day()-i, h, m, 0, 0, loc)
			if !t.After(now) && days[int(t.Weekday())] {
				return t
			}
		}
	}
	return time.Time{}
}

// Next 返回晚于 now 的下一个计划时间点；手动计划返回零值。
func (s Schedule) Next(now time.Time, loc *time.Location) time.Time {
	now = now.In(loc)
	h, m, _ := parseHM(s.At)
	switch s.Kind {
	case ScheduleHourly:
		if s.EveryHours < 1 {
			return time.Time{}
		}
		anchor := hourlyAnchor(s, loc)
		period := time.Duration(s.EveryHours) * time.Hour
		if now.Before(anchor) {
			return anchor
		}
		k := now.Sub(anchor)/period + 1
		return anchor.Add(k * period)
	case ScheduleDaily:
		t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
		if !t.After(now) {
			t = time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, loc)
		}
		return t
	case ScheduleWeekly:
		days := map[int]bool{}
		for _, d := range s.Weekdays {
			days[d] = true
		}
		for i := 0; i <= 7; i++ {
			t := time.Date(now.Year(), now.Month(), now.Day()+i, h, m, 0, 0, loc)
			if t.After(now) && days[int(t.Weekday())] {
				return t
			}
		}
	}
	return time.Time{}
}

// 失败重试：同一个计划时间点最多重试 2 次，间隔至少 30 分钟，离下一个计划时间点不足 30 分钟就不再重试。
const (
	retryDelay        = 30 * time.Minute
	maxRetriesPerSlot = 2
)

// due 判断目标现在是否该执行。只看最近一个计划时间点：服务停了很久再启动，错过的计划只补跑一次。
// 基准时间取最近一次执行与目标创建时间中较晚的那个，新建的目标等到下一个计划时间点才第一次执行。
func (e *Engine) due(ctx context.Context, spec *targetSpec, now time.Time) bool {
	if !spec.row.Enabled || spec.sched.Kind == ScheduleManual {
		return false
	}
	slot := spec.sched.Last(now, e.loc())
	if slot.IsZero() {
		return false
	}
	slotMs := slot.UnixMilli()
	base := spec.row.CreatedAt
	if spec.row.LastRunAt != nil && *spec.row.LastRunAt > base {
		base = *spec.row.LastRunAt
	}
	if base < slotMs {
		return true
	}
	// 这个时间点已经执行过：失败了就按间隔重试。
	if spec.row.LastStatus != StatusFailed || spec.row.LastRunAt == nil {
		return false
	}
	if now.Sub(time.UnixMilli(*spec.row.LastRunAt)) < retryDelay {
		return false
	}
	if next := spec.sched.Next(now, e.loc()); !next.IsZero() && next.Sub(now) < retryDelay {
		return false
	}
	var failed int
	if err := e.st.QueryRow(ctx, "SELECT count(*) FROM backup_runs WHERE target_id = ? AND started_at >= ? AND status = ? AND kind IN (?, ?)",
		spec.row.ID, slotMs, StatusFailed, RunFull, RunMirror).Scan(&failed); err != nil {
		return false
	}
	return failed <= maxRetriesPerSlot
}

// Tick 检查计划，到点的目标在后台开始备份。由后台任务每分钟调用一次。
func (e *Engine) Tick(ctx context.Context) {
	var rows []model.BackupTarget
	if err := e.st.Select(ctx, &rows, "SELECT * FROM backup_targets WHERE enabled = 1 ORDER BY created_at"); err != nil {
		e.log.Warn("读取备份目标失败", "err", err)
		return
	}
	now := e.now()
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		spec, err := e.specFromRow(r)
		if err != nil || !e.due(ctx, spec, now) {
			continue
		}
		e.mu.Lock()
		full := e.scheduled >= e.o.MaxScheduled
		_, busy := e.busy[r.ID]
		e.mu.Unlock()
		if full {
			return
		}
		if busy {
			continue
		}
		op, octx, err := e.begin(spec, spec.row.Mode, triggerSchedule, "", nil)
		if err != nil {
			continue
		}
		e.mu.Lock()
		e.scheduled++
		e.mu.Unlock()
		go e.runBackup(octx, op, spec, "")
	}
}
