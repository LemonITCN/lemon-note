package app

import (
	"context"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/engine"
)

func init() {
	// 每分钟检查一次备份计划。后台任务启动后会先等 2 分钟再第一次执行，
	// 服务停了很久再启动时，每个目标只补跑最近错过的那一次。
	RegisterJob(Job{Name: "backup-schedule", Interval: time.Minute, Run: func(ctx context.Context, s *Services) error {
		if s.Backup != nil {
			s.Backup.Tick(ctx)
		}
		return nil
	}})
	// 停止服务时先取消并等待进行中的备份、校验、恢复，再关闭实例。
	RegisterStopHook(func(s *Services) {
		if s.Backup != nil {
			s.Backup.Close()
		}
	})
}

// buildBackup 创建备份引擎并挂载接口。
func (a *App) buildBackup(svc *Services) error {
	e, err := engine.New(engine.Options{Inst: svc.Inst, Box: svc.Box, Log: a.Log, ConfigPath: a.Boot.Path(), CacheRoot: a.Boot.CacheDir})
	if err != nil {
		return err
	}
	svc.Backup = e
	svc.Web.AddMount(e.Mount)
	return nil
}
