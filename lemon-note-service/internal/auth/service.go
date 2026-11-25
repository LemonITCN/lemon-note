package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Service 提供登录、会话、令牌。
type Service struct {
	st  *store.Store
	set *settings.Service
}

func New(st *store.Store, set *settings.Service) *Service { return &Service{st: st, set: set} }

var (
	ErrBadCredentials = errors.New("用户名或口令不正确")
	ErrDisabled       = errors.New("账号已停用")
	ErrSessionInvalid = errors.New("登录已失效，请重新登录")
)

// ErrLocked 登录被暂时锁定。
type ErrLocked struct{ RetryAfter time.Duration }

func (e *ErrLocked) Error() string {
	return fmt.Sprintf("尝试次数过多，请 %d 秒后再试", int(e.RetryAfter.Seconds())+1)
}

// NormalizeUsername 用户名统一小写、去空白。
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidUsername 用户名规则：3-32 位字母、数字、下划线、点、减号。
func ValidUsername(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// ---------- 登录限流 ----------

func lockDuration(failures int64) time.Duration {
	if failures < 5 {
		return 0
	}
	d := 30 * time.Second << uint(min64(failures-5, 5))
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (s *Service) checkLock(ctx context.Context, keys ...string) error {
	now := time.Now().UnixMilli()
	for _, k := range keys {
		var a model.LoginAttempt
		if err := s.st.Get(ctx, &a, "SELECT * FROM login_attempts WHERE id = ?", k); err == nil && a.LockedUntil > now {
			return &ErrLocked{RetryAfter: time.Duration(a.LockedUntil-now) * time.Millisecond}
		}
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, keys ...string) {
	now := time.Now().UnixMilli()
	for _, k := range keys {
		var a model.LoginAttempt
		if err := s.st.Get(ctx, &a, "SELECT * FROM login_attempts WHERE id = ?", k); err != nil || now-a.LastAt > int64(time.Hour/time.Millisecond) {
			a = model.LoginAttempt{ID: k, FirstAt: now}
		}
		a.Failures++
		a.LastAt = now
		if d := lockDuration(a.Failures); d > 0 {
			a.LockedUntil = now + d.Milliseconds()
		}
		_ = s.st.PutLocal(ctx, "login_attempts", a)
	}
}

func (s *Service) clearFailures(ctx context.Context, keys ...string) {
	for _, k := range keys {
		_, _ = s.st.ExecLocal(ctx, "DELETE FROM login_attempts WHERE id = ?", k)
	}
}

// ---------- 登录 ----------

// CheckPassword 校验用户名口令（带限流），成功返回用户。
func (s *Service) CheckPassword(ctx context.Context, username, password, ip string) (*model.User, error) {
	username = NormalizeUsername(username)
	keys := []string{"u:" + username, "ip:" + ip}
	if err := s.checkLock(ctx, keys...); err != nil {
		return nil, err
	}
	var u model.User
	err := s.st.Get(ctx, &u, "SELECT * FROM users WHERE username = ?", username)
	if err != nil {
		VerifyPassword(password, dummyHash)
		s.recordFailure(ctx, keys...)
		return nil, ErrBadCredentials
	}
	if !VerifyPassword(password, u.PasswordHash) {
		s.recordFailure(ctx, keys...)
		return nil, ErrBadCredentials
	}
	if u.Status != model.UserActive {
		return nil, ErrDisabled
	}
	s.clearFailures(ctx, "u:"+username)
	return &u, nil
}

// CheckMFA 校验两步验证码或恢复码（带限流）。成功后消耗恢复码。
func (s *Service) CheckMFA(ctx context.Context, u *model.User, secretPlain, code, ip string) error {
	keys := []string{"mfa:" + u.ID, "ip:" + ip}
	if err := s.checkLock(ctx, keys...); err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if len(code) == 6 && VerifyTOTP(u.ID, secretPlain, code) {
		s.clearFailures(ctx, "mfa:"+u.ID)
		return nil
	}
	if strings.Contains(code, "-") || len(code) >= 10 {
		var hashes []string
		_ = json.Unmarshal([]byte(u.RecoveryCodes), &hashes)
		h := HashRecoveryCode(code)
		for i, x := range hashes {
			if x == h {
				hashes = append(hashes[:i], hashes[i+1:]...)
				b, _ := json.Marshal(hashes)
				_, err := s.st.Write(ctx, u.ID, func(tx *store.Tx) error {
					var cur model.User
					if err := tx.Get(&cur, "SELECT * FROM users WHERE id = ?", u.ID); err != nil {
						return err
					}
					cur.RecoveryCodes = string(b)
					cur.UpdatedAt = model.Now()
					return tx.Put("users", cur)
				})
				if err == nil {
					s.clearFailures(ctx, "mfa:"+u.ID)
				}
				return err
			}
		}
	}
	s.recordFailure(ctx, keys...)
	return errors.New("验证码不正确")
}

// ---------- 会话 ----------

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// CreateSession 新建会话，返回 Cookie 里放的令牌。
func (s *Service) CreateSession(ctx context.Context, userID, ip, ua string, mfaOK bool) (string, *model.Session, error) {
	sys := s.set.System(ctx)
	tok := ids.RandomToken(43)
	now := time.Now()
	sess := &model.Session{ID: hashToken(tok), UserID: userID, CreatedAt: now.UnixMilli(), LastSeenAt: now.UnixMilli(),
		ExpiresAt: now.Add(time.Duration(sys.SessionMaxDays) * 24 * time.Hour).UnixMilli(),
		IP:        ip, UA: truncate(ua, 300), MFAOK: mfaOK, CSRF: ids.RandomToken(32), Data: "{}"}
	if err := s.st.PutLocal(ctx, "sessions", sess); err != nil {
		return "", nil, err
	}
	return tok, sess, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// LookupSession 通过令牌找会话与用户；过期、闲置超时、改过口令、账号停用都视为失效。
func (s *Service) LookupSession(ctx context.Context, tok string) (*model.Session, *model.User, error) {
	if tok == "" || len(tok) > 100 {
		return nil, nil, ErrSessionInvalid
	}
	var sess model.Session
	if err := s.st.Get(ctx, &sess, "SELECT * FROM sessions WHERE id = ?", hashToken(tok)); err != nil {
		return nil, nil, ErrSessionInvalid
	}
	now := time.Now()
	sys := s.set.System(ctx)
	idle := time.Duration(sys.SessionIdleDays) * 24 * time.Hour
	if now.UnixMilli() > sess.ExpiresAt || now.Sub(time.UnixMilli(sess.LastSeenAt)) > idle {
		_ = s.RevokeSession(ctx, sess.ID)
		return nil, nil, ErrSessionInvalid
	}
	var u model.User
	if err := s.st.Get(ctx, &u, "SELECT * FROM users WHERE id = ?", sess.UserID); err != nil {
		return nil, nil, ErrSessionInvalid
	}
	if u.Status != model.UserActive || u.PasswordChangedAt > sess.CreatedAt {
		_ = s.RevokeSession(ctx, sess.ID)
		return nil, nil, ErrSessionInvalid
	}
	if now.UnixMilli()-sess.LastSeenAt > 60_000 {
		sess.LastSeenAt = now.UnixMilli()
		_, _ = s.st.ExecLocal(ctx, "UPDATE sessions SET last_seen_at = ? WHERE id = ?", sess.LastSeenAt, sess.ID)
	}
	return &sess, &u, nil
}

// MarkMFA 两步验证通过：换一个新令牌（防会话固定）。
func (s *Service) MarkMFA(ctx context.Context, old *model.Session) (string, *model.Session, error) {
	tok, sess, err := s.CreateSession(ctx, old.UserID, old.IP, old.UA, true)
	if err != nil {
		return "", nil, err
	}
	_ = s.RevokeSession(ctx, old.ID)
	return tok, sess, nil
}

// RevokeSession 注销会话。
func (s *Service) RevokeSession(ctx context.Context, id string) error {
	_, err := s.st.ExecLocal(ctx, "DELETE FROM sessions WHERE id = ?", id)
	return err
}

// RevokeUserSessions 注销用户全部会话（except 除外）。
func (s *Service) RevokeUserSessions(ctx context.Context, userID, except string) error {
	_, err := s.st.ExecLocal(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", userID, except)
	return err
}

// ListSessions 列出用户会话。
func (s *Service) ListSessions(ctx context.Context, userID string) ([]model.Session, error) {
	var out []model.Session
	err := s.st.Select(ctx, &out, "SELECT * FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC", userID)
	return out, err
}

// SessionPublicID 给界面用的会话标识（会话 id 本身是令牌哈希，再哈希一次再给前端）。
func SessionPublicID(id string) string { return hashToken("pub:" + id)[:16] }

// ---------- 访问令牌 ----------

const TokenPrefix = "ln_pat_"

// TokenScopes 令牌权限范围。
type TokenScopes struct {
	Write bool `json:"write"`
}

// CreateToken 新建个人访问令牌，明文只返回一次。
func (s *Service) CreateToken(ctx context.Context, userID, name string, write bool, spaceIDs []string, expiresAt *int64) (string, *model.APIToken, error) {
	raw := TokenPrefix + ids.RandomToken(40)
	scopes, _ := json.Marshal(TokenScopes{Write: write})
	if spaceIDs == nil {
		spaceIDs = []string{}
	}
	sp, _ := json.Marshal(spaceIDs)
	t := &model.APIToken{ID: ids.New(), UserID: userID, Name: truncate(strings.TrimSpace(name), 100), Prefix: raw[:12],
		TokenHash: hashToken(raw), Scopes: string(scopes), SpaceIDs: string(sp), ExpiresAt: expiresAt, CreatedAt: model.Now()}
	_, err := s.st.Write(ctx, userID, func(tx *store.Tx) error { return tx.Put("api_tokens", t) })
	if err != nil {
		return "", nil, err
	}
	return raw, t, nil
}

// LookupToken 校验个人访问令牌。
func (s *Service) LookupToken(ctx context.Context, raw string) (*model.APIToken, *model.User, error) {
	if !strings.HasPrefix(raw, TokenPrefix) || len(raw) > 100 {
		return nil, nil, ErrSessionInvalid
	}
	var t model.APIToken
	if err := s.st.Get(ctx, &t, "SELECT * FROM api_tokens WHERE token_hash = ?", hashToken(raw)); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		return nil, nil, ErrSessionInvalid
	}
	now := model.Now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && *t.ExpiresAt < now) {
		return nil, nil, ErrSessionInvalid
	}
	var u model.User
	if err := s.st.Get(ctx, &u, "SELECT * FROM users WHERE id = ?", t.UserID); err != nil || u.Status != model.UserActive {
		return nil, nil, ErrSessionInvalid
	}
	if t.LastUsedAt == nil || now-*t.LastUsedAt > int64(time.Hour/time.Millisecond) {
		t.LastUsedAt = &now
		_, _ = s.st.Write(ctx, u.ID, func(tx *store.Tx) error { return tx.Put("api_tokens", t) })
	}
	return &t, &u, nil
}
