package app

import (
	"context"
	"time"
)

// Job 是周期性后台任务。
type Job struct {
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context, s *Services) error
}

var jobs []Job

// RegisterJob 注册后台任务。
func RegisterJob(j Job) { jobs = append(jobs, j) }

func init() {
	RegisterJob(Job{Name: "purge-trash", Interval: 6 * time.Hour, Run: func(ctx context.Context, s *Services) error {
		days := s.Settings.System(ctx).TrashRetainDays
		_, err := s.Pages.PurgeExpiredTrash(ctx, time.Duration(days)*24*time.Hour)
		return err
	}})
	RegisterJob(Job{Name: "expire-sessions", Interval: time.Hour, Run: func(ctx context.Context, s *Services) error {
		_, err := s.Inst.Store.ExecLocal(ctx, "DELETE FROM sessions WHERE expires_at < ?", time.Now().UnixMilli())
		return err
	}})
	RegisterJob(Job{Name: "prune-history", Interval: 12 * time.Hour, Run: func(ctx context.Context, s *Services) error {
		days := s.Settings.System(ctx).HistoryRetainDays
		return pruneHistory(ctx, s, time.Duration(days)*24*time.Hour)
	}})
}

func (a *App) startJobs(ctx context.Context, s *Services) {
	for _, j := range jobs {
		j := j
		a.jobsWG.Add(1)
		go func() {
			defer a.jobsWG.Done()
			// 启动后错开一会儿再跑第一次
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Minute):
			}
			t := time.NewTicker(j.Interval)
			defer t.Stop()
			for {
				if err := j.Run(ctx, s); err != nil && ctx.Err() == nil {
					a.Log.Warn("后台任务失败", "job", j.Name, "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
}
