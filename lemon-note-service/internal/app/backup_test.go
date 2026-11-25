package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/persist"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// 通过真实的路由与中间件走一遍备份接口：只有管理员能用、执行与恢复是异步任务、可以查询进度与结果。
func TestBackupHTTP(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	discard := slog.New(slog.DiscardHandler)
	boot, err := config.Load(filepath.Join(root, "lemon-note.toml"), config.UserMode)
	if err != nil {
		t.Fatal(err)
	}
	boot.DataDir, boot.CacheDir = filepath.Join(root, "data"), filepath.Join(root, "cache")
	pc := persist.Config{DataDir: boot.DataDir, CacheDir: boot.CacheDir, HostID: "http-test", Version: "test", Logger: discard}
	if err := persist.Initialize(ctx, pc, store.SQLite); err != nil {
		t.Fatal(err)
	}
	inst, err := persist.Open(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	a := New(boot, config.UserMode, discard)
	svc, err := a.buildServices(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		svc.Backup.Close()
		svc.Search.Close()
		inst.Close(ctx)
	}()
	admin, err := svc.Accounts.CreateUser(ctx, "", accounts.NewUser{Username: "admin", DisplayName: "管理员", Password: "Admin-Pass-2026!x", Role: model.RoleAdmin, Locale: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := svc.Accounts.CreateUser(ctx, "", accounts.NewUser{Username: "member", DisplayName: "成员", Password: "Member-Pass-2026!x", Role: model.RoleMember, Locale: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	adminTok, adminSess, _ := svc.Auth.CreateSession(ctx, admin.ID, "127.0.0.1", "test", true)
	memberTok, memberSess, _ := svc.Auth.CreateSession(ctx, member.ID, "127.0.0.1", "test", true)
	h := svc.Web.Handler()
	call := func(tok string, sess *model.Session, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		req.AddCookie(&http.Cookie{Name: "ln_session", Value: tok})
		req.Header.Set("X-CSRF-Token", sess.CSRF)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	adminCall := func(method, path string, body any) (int, map[string]any) {
		return call(adminTok, adminSess, method, path, body)
	}
	waitRun := func(id string) map[string]any {
		t.Helper()
		for i := 0; i < 500; i++ {
			code, v := adminCall("GET", "/api/admin/backup/runs/"+id, nil)
			if code != 200 {
				t.Fatalf("查询执行记录失败：%d %v", code, v)
			}
			if v["running"] != true {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("等待超时")
		return nil
	}

	if code, _ := call(memberTok, memberSess, "GET", "/api/admin/backup/targets", nil); code != http.StatusForbidden {
		t.Fatalf("普通成员不应能访问备份接口：%d", code)
	}
	nas := filepath.Join(root, "nas")
	if err := mkdir(nas); err != nil {
		t.Fatal(err)
	}
	code, v := adminCall("POST", "/api/admin/backup/targets", map[string]any{"name": "NAS", "kind": "local", "config": map[string]any{"path": nas},
		"mode": "full", "schedule": map[string]any{"kind": "daily", "at": "03:00"}, "encryption": map[string]any{"enabled": true, "password": "backup-pass-1"}})
	if code != 200 {
		t.Fatalf("新建目标失败：%d %v", code, v)
	}
	id := v["id"].(string)
	if enc := v["encryption"].(map[string]any); enc["has_password"] != true {
		t.Fatalf("加密设置不对：%v", enc)
	}
	if code, v = adminCall("POST", "/api/admin/backup/targets/"+id+"/test", nil); code != 200 || v["ok"] != true {
		t.Fatalf("测试连接失败：%d %v", code, v)
	}
	code, v = adminCall("POST", "/api/admin/backup/targets/"+id+"/run", nil)
	if code != 200 {
		t.Fatalf("立即执行失败：%d %v", code, v)
	}
	run := waitRun(v["run_id"].(string))
	if run["status"] != "success" || !strings.HasSuffix(run["artifact"].(string), ".lnbak") {
		t.Fatalf("备份没有成功：%v", run)
	}
	pkg := run["artifact"].(string)
	code, v = adminCall("GET", "/api/admin/backup/targets/"+id+"/backups", nil)
	if code != 200 || len(v["backups"].([]any)) != 1 {
		t.Fatalf("列出备份不对：%d %v", code, v)
	}
	code, v = adminCall("POST", "/api/admin/backup/targets/"+id+"/verify", map[string]any{"backup": pkg})
	if code != 200 {
		t.Fatalf("校验失败：%d %v", code, v)
	}
	vr := waitRun(v["run_id"].(string))
	if vr["status"] != "success" || vr["result"].(map[string]any)["ok"] != true {
		t.Fatalf("校验结果不对：%v", vr)
	}
	// 恢复到当前数据目录里面：拒绝
	if code, v = adminCall("POST", "/api/admin/backup/targets/"+id+"/restore", map[string]any{"backup": pkg, "to": filepath.Join(boot.DataDir, "x")}); code != 400 {
		t.Fatalf("恢复到数据目录里应被拒绝：%d %v", code, v)
	}
	dest := filepath.Join(root, "restored")
	code, v = adminCall("POST", "/api/admin/backup/targets/"+id+"/restore", map[string]any{"backup": pkg, "to": dest})
	if code != 200 {
		t.Fatalf("恢复失败：%d %v", code, v)
	}
	rr := waitRun(v["run_id"].(string))
	res, _ := rr["result"].(map[string]any)
	if rr["status"] != "success" || res == nil || !strings.Contains(res["hint"].(string), "data_dir") {
		t.Fatalf("恢复结果不对：%v", rr)
	}
	// 口令错误
	code, v = adminCall("POST", "/api/admin/backup/targets/"+id+"/backups", map[string]any{"password": "wrong"})
	if code != 200 {
		t.Fatalf("口令错误时仍应能列出包名：%d %v", code, v)
	}
	code, v = adminCall("GET", "/api/admin/backup/runs?target_id="+id, nil)
	if code != 200 || len(v["runs"].([]any)) != 3 {
		t.Fatalf("执行记录不对：%d %v", code, v)
	}
	if code, v = adminCall("DELETE", "/api/admin/backup/targets/"+id, nil); code != 200 {
		t.Fatalf("删除目标失败：%d %v", code, v)
	}
	if code, _ = adminCall("GET", "/api/admin/backup/targets/"+id, nil); code != 404 {
		t.Fatalf("删除后应查不到：%d", code)
	}
	var n int
	inst.Store.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action LIKE 'backup.%'").Scan(&n)
	if n < 5 {
		t.Fatalf("备份操作应写审计日志，实际 %d 条", n)
	}
}

func mkdir(p string) error { return os.MkdirAll(p, 0o755) }
