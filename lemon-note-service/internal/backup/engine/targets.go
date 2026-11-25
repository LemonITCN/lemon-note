package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// targetSpec 是解密、解析之后的目标设置。
type targetSpec struct {
	row      model.BackupTarget
	config   []byte // 明文配置 JSON
	sched    Schedule
	ret      Retention
	enc      encStored
	password string // 明文口令（只在内存里）
}

// TargetInput 是新建 / 修改目标时提交的内容。修改时没给的字段保持不变。
type TargetInput struct {
	Name       *string          `json:"name"`
	Kind       *string          `json:"kind"`
	Config     json.RawMessage  `json:"config"` // 驱动配置对象；凭据字段传打码值表示不改
	Mode       *string          `json:"mode"`
	Schedule   *Schedule        `json:"schedule"`
	Retention  *Retention       `json:"retention"`
	Encryption *EncryptionInput `json:"encryption"`
	Enabled    *bool            `json:"enabled"`
}

// TargetView 是返回给界面的目标（凭据已打码）。
type TargetView struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Config     map[string]any `json:"config"`
	Mode       string         `json:"mode"`
	Schedule   Schedule       `json:"schedule"`
	Retention  Retention      `json:"retention"`
	Encryption EncryptionView `json:"encryption"`
	Enabled    bool           `json:"enabled"`
	CreatedBy  string         `json:"created_by"`
	LastRunAt  *int64         `json:"last_run_at,omitempty"`
	LastStatus string         `json:"last_status"`
	NextRunAt  *int64         `json:"next_run_at,omitempty"`
	Running    *RunningView   `json:"running,omitempty"`
	CreatedAt  int64          `json:"created_at"`
	UpdatedAt  int64          `json:"updated_at"`
}

// RunningView 是目标上进行中的操作。
type RunningView struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	StartedAt int64        `json:"started_at"`
	Progress  ProgressView `json:"progress"`
}

var validKinds = map[string]bool{target.KindLocal: true, target.KindS3: true, target.KindCOS: true, target.KindOSS: true,
	target.KindWebDAV: true, target.KindSFTP: true, target.KindBaidu: true}

// isSecretField 判断驱动配置里的字段是否是凭据（接口返回时打码）。
func isSecretField(k string) bool {
	k = strings.ToLower(k)
	switch k {
	case "password", "secret_key", "access_key_secret", "private_key", "passphrase", "access_token", "refresh_token":
		return true
	case "secret_id", "access_key", "access_key_id", "app_key":
		return false
	}
	return strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "token") ||
		strings.Contains(k, "private") || strings.Contains(k, "passphrase")
}

func maskConfig(cfg []byte) map[string]any {
	m := map[string]any{}
	if len(bytes.TrimSpace(cfg)) == 0 {
		return m
	}
	if json.Unmarshal(cfg, &m) != nil {
		return map[string]any{}
	}
	for k, v := range m {
		if s, ok := v.(string); ok && isSecretField(k) && s != "" {
			m[k] = Masked
		}
	}
	return m
}

// mergeConfig 用旧配置补上打码字段；kind 变了时旧凭据不再沿用。
func mergeConfig(newCfg json.RawMessage, oldCfg []byte, sameKind bool) ([]byte, error) {
	in := map[string]json.RawMessage{}
	if err := json.Unmarshal(newCfg, &in); err != nil {
		return nil, uerr("目标配置必须是一个 JSON 对象")
	}
	old := map[string]json.RawMessage{}
	if sameKind && len(oldCfg) > 0 {
		_ = json.Unmarshal(oldCfg, &old)
	}
	maskedJSON, _ := json.Marshal(Masked)
	for k, v := range in {
		if !bytes.Equal(bytes.TrimSpace(v), maskedJSON) {
			continue
		}
		if ov, ok := old[k]; ok {
			in[k] = ov
		} else {
			delete(in, k)
		}
	}
	// 百度网盘的令牌只由授权流程写入：界面提交的配置里没有就沿用旧的。
	if sameKind {
		for _, k := range []string{"access_token", "refresh_token", "expires_at"} {
			if _, ok := in[k]; !ok {
				if ov, ok := old[k]; ok {
					in[k] = ov
				}
			}
		}
		// 换了 AppKey，旧令牌就作废了。
		if ok1, ok2 := in["app_key"], old["app_key"]; ok1 != nil && ok2 != nil && !bytes.Equal(ok1, ok2) {
			delete(in, "access_token")
			delete(in, "refresh_token")
			delete(in, "expires_at")
		}
	}
	return json.Marshal(in)
}

func (e *Engine) specFromRow(row model.BackupTarget) (*targetSpec, error) {
	s := &targetSpec{row: row}
	cfg, err := e.box.Open(row.Config)
	if err != nil {
		return nil, uerrf("备份目标「%s」的配置无法解密（config/secret.key 可能被换过），请重新填写", row.Name)
	}
	s.config = []byte(cfg)
	if row.Schedule != "" {
		if json.Unmarshal([]byte(row.Schedule), &s.sched) != nil {
			s.sched = DefaultSchedule()
		}
	}
	if err := s.sched.Normalize(); err != nil {
		s.sched = Schedule{Kind: ScheduleManual}
	}
	if row.Retention != "" {
		_ = json.Unmarshal([]byte(row.Retention), &s.ret)
	}
	if err := s.ret.Normalize(); err != nil {
		s.ret = DefaultRetention()
	}
	if row.Encryption != "" {
		_ = json.Unmarshal([]byte(row.Encryption), &s.enc)
	}
	if s.enc.Password != "" {
		pw, err := e.box.Open(s.enc.Password)
		if err != nil {
			return nil, uerrf("备份目标「%s」保存的备份口令无法解密，请重新设置口令", row.Name)
		}
		s.password = pw
	}
	if row.Mode != ModeMirror {
		s.row.Mode = ModeFull
	}
	return s, nil
}

func (e *Engine) loadSpec(ctx context.Context, id string) (*targetSpec, error) {
	var row model.BackupTarget
	if err := e.st.Get(ctx, &row, "SELECT * FROM backup_targets WHERE id = ?", id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTargetNotFound
		}
		return nil, err
	}
	return e.specFromRow(row)
}

// FindTarget 按 ID 或名称（不区分大小写）查找目标，返回 ID。
func (e *Engine) FindTarget(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	var rows []model.BackupTarget
	if err := e.st.Select(ctx, &rows, "SELECT * FROM backup_targets"); err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.ID == ref {
			return r.ID, nil
		}
	}
	var hit []string
	for _, r := range rows {
		if strings.EqualFold(r.Name, ref) {
			hit = append(hit, r.ID)
		}
	}
	switch len(hit) {
	case 1:
		return hit[0], nil
	case 0:
		return "", uerrf("没有名为「%s」的备份目标", ref)
	default:
		return "", uerrf("有多个名为「%s」的备份目标，请改用 ID", ref)
	}
}

func (e *Engine) view(spec *targetSpec) TargetView {
	v := TargetView{ID: spec.row.ID, Name: spec.row.Name, Kind: spec.row.Kind, Config: maskConfig(spec.config), Mode: spec.row.Mode,
		Schedule: spec.sched, Retention: spec.ret, Encryption: EncryptionView{Enabled: spec.enc.Enabled, HasPassword: spec.password != ""},
		Enabled: spec.row.Enabled, CreatedBy: spec.row.CreatedBy, LastRunAt: spec.row.LastRunAt, LastStatus: spec.row.LastStatus,
		CreatedAt: spec.row.CreatedAt, UpdatedAt: spec.row.UpdatedAt}
	if spec.row.Enabled {
		if next := spec.sched.Next(e.now(), e.loc()); !next.IsZero() {
			v.NextRunAt = model.Int64Ptr(next.UnixMilli())
		}
	}
	if op := e.busyOp(spec.row.ID); op != nil {
		v.Running = &RunningView{ID: op.ID, Kind: op.Kind, StartedAt: op.StartedAt, Progress: op.prog.View()}
	}
	return v
}

// ListTargets 列出全部目标。
func (e *Engine) ListTargets(ctx context.Context) ([]TargetView, error) {
	var rows []model.BackupTarget
	if err := e.st.Select(ctx, &rows, "SELECT * FROM backup_targets ORDER BY created_at"); err != nil {
		return nil, err
	}
	out := make([]TargetView, 0, len(rows))
	for _, r := range rows {
		spec, err := e.specFromRow(r)
		if err != nil {
			// 解不开的目标也列出来，让管理员能删除或重填。
			out = append(out, TargetView{ID: r.ID, Name: r.Name, Kind: r.Kind, Config: map[string]any{}, Mode: r.Mode,
				Enabled: r.Enabled, LastRunAt: r.LastRunAt, LastStatus: r.LastStatus, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
			continue
		}
		out = append(out, e.view(spec))
	}
	return out, nil
}

// GetTarget 返回一个目标。
func (e *Engine) GetTarget(ctx context.Context, id string) (*TargetView, error) {
	spec, err := e.loadSpec(ctx, id)
	if err != nil {
		return nil, err
	}
	v := e.view(spec)
	return &v, nil
}

// CreateTarget 新建目标。
func (e *Engine) CreateTarget(ctx context.Context, actor string, in TargetInput) (*TargetView, error) {
	now := e.now().UnixMilli()
	row := model.BackupTarget{ID: ids.New(), CreatedBy: actor, CreatedAt: now, UpdatedAt: now, Enabled: true, Mode: ModeFull}
	if in.Kind == nil || in.Name == nil || len(in.Config) == 0 {
		return nil, uerr("请填写目标名称、类型和配置")
	}
	spec := &targetSpec{row: row, sched: DefaultSchedule(), ret: DefaultRetention()}
	if err := e.applyInput(ctx, spec, in, true); err != nil {
		return nil, err
	}
	if err := e.saveSpec(ctx, actor, spec); err != nil {
		return nil, err
	}
	return e.GetTarget(ctx, row.ID)
}

// UpdateTarget 修改目标。正在执行操作时也可以修改，新设置从下一次执行开始生效。
func (e *Engine) UpdateTarget(ctx context.Context, actor, id string, in TargetInput) (*TargetView, error) {
	spec, err := e.loadSpec(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := e.applyInput(ctx, spec, in, false); err != nil {
		return nil, err
	}
	spec.row.UpdatedAt = e.now().UnixMilli()
	if err := e.saveSpec(ctx, actor, spec); err != nil {
		return nil, err
	}
	return e.GetTarget(ctx, id)
}

func (e *Engine) applyInput(ctx context.Context, spec *targetSpec, in TargetInput, create bool) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" || utf8.RuneCountInString(name) > 100 {
			return uerr("目标名称不能为空，最长 100 个字")
		}
		var rows []model.BackupTarget
		if err := e.st.Select(ctx, &rows, "SELECT * FROM backup_targets"); err != nil {
			return err
		}
		for _, r := range rows {
			if r.ID != spec.row.ID && strings.EqualFold(r.Name, name) {
				return uerrf("已经有一个名为「%s」的备份目标", name)
			}
		}
		spec.row.Name = name
	}
	oldKind := spec.row.Kind
	if in.Kind != nil {
		k := strings.TrimSpace(*in.Kind)
		if !validKinds[k] {
			return uerrf("不支持的备份目标类型「%s」", k)
		}
		spec.row.Kind = k
	}
	if len(in.Config) > 0 && string(bytes.TrimSpace(in.Config)) != "null" {
		merged, err := mergeConfig(in.Config, spec.config, !create && oldKind == spec.row.Kind)
		if err != nil {
			return err
		}
		spec.config = merged
	} else if !create && oldKind != spec.row.Kind {
		return uerr("修改目标类型时需要重新填写配置")
	}
	if err := e.validateConfig(spec.row.Kind, spec.config); err != nil {
		return err
	}
	if in.Mode != nil {
		switch *in.Mode {
		case ModeFull, ModeMirror:
			spec.row.Mode = *in.Mode
		default:
			return uerrf("不认识的备份模式「%s」", *in.Mode)
		}
	}
	if in.Schedule != nil {
		s := *in.Schedule
		if err := s.Normalize(); err != nil {
			return err
		}
		spec.sched = s
	}
	if in.Retention != nil {
		r := *in.Retention
		if err := r.Normalize(); err != nil {
			return err
		}
		spec.ret = r
	}
	if in.Encryption != nil {
		pw := in.Encryption.Password
		if pw == Masked {
			pw = ""
		}
		if pw != "" {
			if utf8.RuneCountInString(pw) < MinPasswordLen {
				return uerrf("备份口令至少 %d 个字符", MinPasswordLen)
			}
			spec.password = pw
		}
		spec.enc.Enabled = in.Encryption.Enabled
		if spec.enc.Enabled && spec.password == "" {
			return uerr("开启加密需要设置备份口令")
		}
	}
	if in.Enabled != nil {
		spec.row.Enabled = *in.Enabled
	}
	return nil
}

// validateConfig 用驱动解析一遍配置（不连网），并拒绝把备份放进数据目录或缓存目录。
func (e *Engine) validateConfig(kind string, cfg []byte) error {
	check := cfg
	if kind == target.KindBaidu {
		// 授权之前没有令牌，先用占位令牌校验其余字段。
		m := map[string]any{}
		_ = json.Unmarshal(cfg, &m)
		if s, _ := m["access_token"].(string); s == "" {
			m["access_token"] = "placeholder"
		}
		check, _ = json.Marshal(m)
	}
	t, err := e.o.OpenTarget(kind, check, target.Deps{HTTPClient: e.o.HTTPClient})
	if err != nil {
		return wrapUser(err)
	}
	t.Close()
	if kind == target.KindLocal {
		var lc target.LocalConfig
		_ = json.Unmarshal(cfg, &lc)
		p := filepath.Clean(strings.TrimSpace(lc.Path))
		for _, dir := range []string{e.inst.Dir.Root, e.o.CacheRoot} {
			if dir != "" && within(p, dir) {
				return uerr("备份目录不能放在数据目录或缓存目录里面")
			}
		}
	}
	return nil
}

func (e *Engine) saveSpec(ctx context.Context, actor string, spec *targetSpec) error {
	row := spec.row
	row.Config = e.box.Seal(string(spec.config))
	sched, _ := json.Marshal(spec.sched)
	ret, _ := json.Marshal(spec.ret)
	enc := encStored{Enabled: spec.enc.Enabled}
	if spec.password != "" {
		enc.Password = e.box.Seal(spec.password)
	}
	encJSON, _ := json.Marshal(enc)
	row.Schedule, row.Retention, row.Encryption = string(sched), string(ret), string(encJSON)
	if actor == "" {
		actor = "system"
	}
	_, err := e.st.Write(ctx, actor, func(tx *store.Tx) error {
		// 最近执行时间与状态由执行过程维护：以库里最新的为准，避免覆盖掉编辑期间刚结束的执行结果。
		var cur model.BackupTarget
		if err := tx.Get(&cur, "SELECT * FROM backup_targets WHERE id = ?", row.ID); err == nil {
			row.LastRunAt, row.LastStatus, row.CreatedBy, row.CreatedAt = cur.LastRunAt, cur.LastStatus, cur.CreatedBy, cur.CreatedAt
		}
		return tx.Put("backup_targets", row)
	})
	return err
}

// DeleteTarget 删除目标与它的执行记录。目标上已有的备份文件不会被删除。
func (e *Engine) DeleteTarget(ctx context.Context, actor, id string) error {
	if _, err := e.loadSpec(ctx, id); err != nil && errors.Is(err, ErrTargetNotFound) {
		return err
	}
	if e.busyOp(id) != nil {
		return uerr("这个目标正在执行备份、校验或恢复，请先取消或等它结束")
	}
	var runIDs []string
	if err := e.st.Select(ctx, &runIDs, "SELECT id FROM backup_runs WHERE target_id = ?", id); err != nil {
		return err
	}
	if actor == "" {
		actor = "system"
	}
	_, err := e.st.Write(ctx, actor, func(tx *store.Tx) error {
		for _, rid := range runIDs {
			if err := tx.Delete("backup_runs", rid); err != nil {
				return err
			}
		}
		return tx.Delete("backup_targets", id)
	})
	e.mu.Lock()
	delete(e.baidu, id)
	e.mu.Unlock()
	return err
}

// openDriver 打开目标驱动；百度网盘刷新令牌后把新配置加密写回数据库。
func (e *Engine) openDriver(spec *targetSpec) (target.Target, error) {
	deps := target.Deps{HTTPClient: e.o.HTTPClient}
	id := spec.row.ID
	deps.SaveConfig = func(cfg []byte) error { return e.saveDriverConfig(id, cfg) }
	t, err := e.o.OpenTarget(spec.row.Kind, spec.config, deps)
	if err != nil {
		return nil, wrapUser(err)
	}
	return t, nil
}

// saveDriverConfig 只更新目标的驱动配置（百度网盘令牌刷新、授权完成时用）。
func (e *Engine) saveDriverConfig(id string, cfg []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sealed := e.box.Seal(string(cfg))
	_, err := e.st.Write(ctx, "system", func(tx *store.Tx) error {
		var row model.BackupTarget
		if err := tx.Get(&row, "SELECT * FROM backup_targets WHERE id = ?", id); err != nil {
			return ErrTargetNotFound
		}
		row.Config = sealed
		row.UpdatedAt = e.now().UnixMilli()
		return tx.Put("backup_targets", row)
	})
	if err != nil {
		return fmt.Errorf("保存百度网盘令牌失败：%w", err)
	}
	return nil
}

// TestResult 是「测试连接」的结果。
type TestResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	HasHeader bool   `json:"has_header"`         // 目标上是否已有 lemon-note-backup.json
	Encrypted bool   `json:"encrypted"`          // 目标上已有的备份是否加密
	Warning   string `json:"warning,omitempty"`  // 加密设置与目标上已有备份不一致等提醒
	HostKey   string `json:"host_key,omitempty"` // SFTP 未确认主机密钥时服务器出示的指纹
}

// TestTarget 测试一个已保存的目标：读写探测，并检查加密设置与目标上已有备份是否一致。
func (e *Engine) TestTarget(ctx context.Context, id string) (*TestResult, error) {
	spec, err := e.loadSpec(ctx, id)
	if err != nil {
		return nil, err
	}
	return e.test(ctx, spec), nil
}

// TestConfig 测试一个还没保存（或正在编辑）的配置。id 非空时用已保存的配置补上打码字段。
func (e *Engine) TestConfig(ctx context.Context, id, kind string, cfg json.RawMessage) (*TestResult, error) {
	spec := &targetSpec{row: model.BackupTarget{Kind: strings.TrimSpace(kind)}}
	var old []byte
	if id != "" {
		s, err := e.loadSpec(ctx, id)
		if err != nil {
			return nil, err
		}
		if s.row.Kind == spec.row.Kind {
			old = s.config
		}
		spec.row.ID = s.row.ID
	}
	if !validKinds[spec.row.Kind] {
		return nil, uerrf("不支持的备份目标类型「%s」", kind)
	}
	merged, err := mergeConfig(cfg, old, old != nil)
	if err != nil {
		return nil, err
	}
	spec.config = merged
	if err := e.validateConfig(spec.row.Kind, merged); err != nil {
		return &TestResult{Error: err.Error()}, nil
	}
	return e.test(ctx, spec), nil
}

func (e *Engine) test(ctx context.Context, spec *targetSpec) *TestResult {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res := &TestResult{}
	deps := target.Deps{HTTPClient: e.o.HTTPClient}
	if spec.row.ID != "" {
		id := spec.row.ID
		deps.SaveConfig = func(cfg []byte) error { return e.saveDriverConfig(id, cfg) }
	}
	t, err := e.o.OpenTarget(spec.row.Kind, spec.config, deps)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer t.Close()
	if err := target.Probe(ctx, t); err != nil {
		res.Error = err.Error()
		var hk *target.UnknownHostKeyError
		if errors.As(err, &hk) {
			res.HostKey = hk.Fingerprint
		}
		return res
	}
	res.OK = true
	h, err := ReadHeader(ctx, t)
	if err != nil {
		res.Warning = err.Error()
		return res
	}
	if h != nil {
		res.HasHeader, res.Encrypted = true, h.Encrypted()
		if spec.row.Mode != "" { // 已保存的目标：核对加密设置
			switch {
			case h.Encrypted() && !spec.enc.Enabled:
				res.Warning = "目标上已有加密的备份，但这个目标没有开启加密，执行备份时会被拒绝"
			case !h.Encrypted() && spec.enc.Enabled:
				res.Warning = "目标上已有未加密的备份，开启加密后执行备份会被拒绝（除非目标上还没有备份文件）"
			case h.Encrypted():
				if _, err := h.Key(spec.password); errors.Is(err, ErrWrongPassword) {
					res.Warning = "这个目标设置的备份口令与目标上已有备份的口令不一致"
				}
			}
		}
	}
	return res
}
