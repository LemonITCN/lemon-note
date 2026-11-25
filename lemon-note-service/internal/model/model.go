// Package model 定义与表一一对应的结构体（db 标签即列名，必须覆盖表的全部列）。
package model

import (
	"encoding/json"
	"time"
)

func Now() int64 { return time.Now().UnixMilli() }

// 系统角色
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// 用户状态
const (
	UserActive   = "active"
	UserDisabled = "disabled"
)

type User struct {
	ID                string  `db:"id" json:"id"`
	Username          string  `db:"username" json:"username"`
	DisplayName       string  `db:"display_name" json:"display_name"`
	Email             *string `db:"email" json:"email,omitempty"`
	Avatar            string  `db:"avatar" json:"avatar"`
	PasswordHash      string  `db:"password_hash" json:"-"`
	Role              string  `db:"role" json:"role"`
	Status            string  `db:"status" json:"status"`
	Locale            string  `db:"locale" json:"locale"`
	Prefs             string  `db:"prefs" json:"-"`
	TOTPSecret        string  `db:"totp_secret" json:"-"`
	TOTPEnabled       bool    `db:"totp_enabled" json:"totp_enabled"`
	RecoveryCodes     string  `db:"recovery_codes" json:"-"`
	PasswordChangedAt int64   `db:"password_changed_at" json:"-"`
	LastLoginAt       *int64  `db:"last_login_at" json:"last_login_at,omitempty"`
	CreatedAt         int64   `db:"created_at" json:"created_at"`
	UpdatedAt         int64   `db:"updated_at" json:"updated_at"`
}

type APIToken struct {
	ID         string `db:"id" json:"id"`
	UserID     string `db:"user_id" json:"user_id"`
	Name       string `db:"name" json:"name"`
	Prefix     string `db:"prefix" json:"prefix"`
	TokenHash  string `db:"token_hash" json:"-"`
	Scopes     string `db:"scopes" json:"scopes"`
	SpaceIDs   string `db:"space_ids" json:"space_ids"`
	ExpiresAt  *int64 `db:"expires_at" json:"expires_at,omitempty"`
	LastUsedAt *int64 `db:"last_used_at" json:"last_used_at,omitempty"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	RevokedAt  *int64 `db:"revoked_at" json:"revoked_at,omitempty"`
}

// 空间
const (
	SpacePersonal = "personal"
	SpaceShared   = "shared"
)

type Space struct {
	ID        string `db:"id" json:"id"`
	Kind      string `db:"kind" json:"kind"`
	Name      string `db:"name" json:"name"`
	Icon      string `db:"icon" json:"icon"`
	OwnerID   string `db:"owner_id" json:"owner_id"`
	Settings  string `db:"settings" json:"-"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	UpdatedAt int64  `db:"updated_at" json:"updated_at"`
	DeletedAt *int64 `db:"deleted_at" json:"deleted_at,omitempty"`
}

// 空间 / 页面角色，数值越大权限越高
const (
	RoleNone      = ""
	RoleViewer    = "viewer"
	RoleCommenter = "commenter"
	RoleEditor    = "editor"
	RoleManager   = "admin" // 空间管理员
	RoleOwner     = "owner"
)

var roleRank = map[string]int{RoleNone: 0, RoleViewer: 1, RoleCommenter: 2, RoleEditor: 3, RoleManager: 4, RoleOwner: 5}

// RoleRank 返回角色等级。
func RoleRank(r string) int { return roleRank[r] }

// MaxRole 返回两者中较高的角色。
func MaxRole(a, b string) string {
	if roleRank[b] > roleRank[a] {
		return b
	}
	return a
}

// RoleAtLeast 判断 r 是否不低于 need。
func RoleAtLeast(r, need string) bool { return roleRank[r] >= roleRank[need] }

// ValidMemberRole 是否是可分配的成员角色。
func ValidMemberRole(r string) bool {
	return r == RoleViewer || r == RoleCommenter || r == RoleEditor || r == RoleManager
}

type SpaceMember struct {
	ID        string `db:"id" json:"id"`
	SpaceID   string `db:"space_id" json:"space_id"`
	UserID    string `db:"user_id" json:"user_id"`
	Role      string `db:"role" json:"role"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	UpdatedAt int64  `db:"updated_at" json:"updated_at"`
}

// 页面类型
const (
	KindPage     = "page"
	KindDatabase = "database"
)

type Page struct {
	ID         string  `db:"id" json:"id"`
	SpaceID    string  `db:"space_id" json:"space_id"`
	ParentID   *string `db:"parent_id" json:"parent_id"`
	DatabaseID *string `db:"database_id" json:"database_id"`
	Kind       string  `db:"kind" json:"kind"`
	Title      string  `db:"title" json:"title"`
	Icon       string  `db:"icon" json:"icon"`
	Cover      string  `db:"cover" json:"cover"`
	Props      string  `db:"props" json:"-"`
	Position   string  `db:"position" json:"position"`
	Flags      string  `db:"flags" json:"-"`
	IsTemplate bool    `db:"is_template" json:"is_template"`
	ContentRev int64   `db:"content_rev" json:"content_rev"`
	CreatedAt  int64   `db:"created_at" json:"created_at"`
	CreatedBy  string  `db:"created_by" json:"created_by"`
	UpdatedAt  int64   `db:"updated_at" json:"updated_at"`
	UpdatedBy  string  `db:"updated_by" json:"updated_by"`
	TrashedAt  *int64  `db:"trashed_at" json:"trashed_at,omitempty"`
	TrashedBy  *string `db:"trashed_by" json:"trashed_by,omitempty"`
	TrashRoot  *string `db:"trash_root" json:"trash_root,omitempty"`
}

// PageFlags 是页面的各种开关。
type PageFlags struct {
	FullWidth   bool   `json:"full_width,omitempty"`
	SmallText   bool   `json:"small_text,omitempty"`
	Font        string `json:"font,omitempty"` // default | serif | mono
	Locked      bool   `json:"locked,omitempty"`
	Inline      bool   `json:"inline,omitempty"` // 内嵌数据库
	HideTitle   bool   `json:"hide_title,omitempty"`
	CoverOffset int    `json:"cover_offset,omitempty"`
}

func (p *Page) GetFlags() PageFlags {
	var f PageFlags
	if p.Flags != "" {
		_ = json.Unmarshal([]byte(p.Flags), &f)
	}
	return f
}

func (p *Page) SetFlags(f PageFlags) {
	b, _ := json.Marshal(f)
	p.Flags = string(b)
}

type Block struct {
	ID        string `db:"id" json:"id"`
	PageID    string `db:"page_id" json:"page_id"`
	Position  string `db:"position" json:"position"`
	Type      string `db:"type" json:"type"`
	Data      string `db:"data" json:"-"`
	Version   int64  `db:"version" json:"version"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	CreatedBy string `db:"created_by" json:"created_by"`
	UpdatedAt int64  `db:"updated_at" json:"updated_at"`
	UpdatedBy string `db:"updated_by" json:"updated_by"`
}

type Database struct {
	ID         string `db:"id" json:"id"`
	Schema     string `db:"schema" json:"-"`
	Settings   string `db:"settings" json:"-"`
	RowCounter int64  `db:"row_counter" json:"row_counter"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	UpdatedAt  int64  `db:"updated_at" json:"updated_at"`
}

type DBView struct {
	ID         string  `db:"id" json:"id"`
	DatabaseID string  `db:"database_id" json:"database_id"`
	HostID     *string `db:"host_id" json:"host_id,omitempty"`
	Name       string  `db:"name" json:"name"`
	Type       string  `db:"type" json:"type"`
	Position   string  `db:"position" json:"position"`
	Config     string  `db:"config" json:"-"`
	CreatedBy  string  `db:"created_by" json:"created_by"`
	CreatedAt  int64   `db:"created_at" json:"created_at"`
	UpdatedAt  int64   `db:"updated_at" json:"updated_at"`
}

type PageVersion struct {
	ID        string `db:"id" json:"id"`
	PageID    string `db:"page_id" json:"page_id"`
	Reason    string `db:"reason" json:"reason"`
	Title     string `db:"title" json:"title"`
	Data      []byte `db:"data" json:"-"`
	Size      int64  `db:"size" json:"size"`
	Meta      string `db:"meta" json:"-"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	CreatedBy string `db:"created_by" json:"created_by"`
}

type CommentThread struct {
	ID         string  `db:"id" json:"id"`
	PageID     string  `db:"page_id" json:"page_id"`
	BlockID    *string `db:"block_id" json:"block_id,omitempty"`
	Anchor     string  `db:"anchor" json:"anchor"`
	ResolvedAt *int64  `db:"resolved_at" json:"resolved_at,omitempty"`
	ResolvedBy *string `db:"resolved_by" json:"resolved_by,omitempty"`
	CreatedAt  int64   `db:"created_at" json:"created_at"`
	CreatedBy  string  `db:"created_by" json:"created_by"`
}

type Comment struct {
	ID        string `db:"id" json:"id"`
	ThreadID  string `db:"thread_id" json:"thread_id"`
	PageID    string `db:"page_id" json:"page_id"`
	Body      string `db:"body" json:"body"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	CreatedBy string `db:"created_by" json:"created_by"`
	UpdatedAt int64  `db:"updated_at" json:"updated_at"`
	DeletedAt *int64 `db:"deleted_at" json:"deleted_at,omitempty"`
}

type Favorite struct {
	ID        string `db:"id" json:"id"`
	UserID    string `db:"user_id" json:"user_id"`
	PageID    string `db:"page_id" json:"page_id"`
	Position  string `db:"position" json:"position"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
}

type File struct {
	ID        string  `db:"id" json:"id"`
	SpaceID   string  `db:"space_id" json:"space_id"`
	PageID    *string `db:"page_id" json:"page_id,omitempty"`
	SHA256    string  `db:"sha256" json:"sha256"`
	Size      int64   `db:"size" json:"size"`
	Mime      string  `db:"mime" json:"mime"`
	Name      string  `db:"name" json:"name"`
	Width     int64   `db:"width" json:"width,omitempty"`
	Height    int64   `db:"height" json:"height,omitempty"`
	CreatedAt int64   `db:"created_at" json:"created_at"`
	CreatedBy string  `db:"created_by" json:"created_by"`
	DeletedAt *int64  `db:"deleted_at" json:"deleted_at,omitempty"`
}

// 分享类型
const (
	ShareUser   = "user"
	SharePublic = "public"
)

type PageShare struct {
	ID              string  `db:"id" json:"id"`
	PageID          string  `db:"page_id" json:"page_id"`
	SpaceID         string  `db:"space_id" json:"space_id"`
	Kind            string  `db:"kind" json:"kind"`
	UserID          *string `db:"user_id" json:"user_id,omitempty"`
	Role            string  `db:"role" json:"role"`
	Token           *string `db:"token" json:"token,omitempty"`
	PasswordHash    string  `db:"password_hash" json:"-"`
	ExpiresAt       *int64  `db:"expires_at" json:"expires_at,omitempty"`
	IncludeChildren bool    `db:"include_children" json:"include_children"`
	AllowIndex      bool    `db:"allow_index" json:"allow_index"`
	Settings        string  `db:"settings" json:"-"`
	CreatedAt       int64   `db:"created_at" json:"created_at"`
	CreatedBy       string  `db:"created_by" json:"created_by"`
	RevokedAt       *int64  `db:"revoked_at" json:"revoked_at,omitempty"`
}

type Notification struct {
	ID        string  `db:"id" json:"id"`
	UserID    string  `db:"user_id" json:"user_id"`
	Kind      string  `db:"kind" json:"kind"`
	PageID    *string `db:"page_id" json:"page_id,omitempty"`
	ActorID   *string `db:"actor_id" json:"actor_id,omitempty"`
	Data      string  `db:"data" json:"data"`
	ReadAt    *int64  `db:"read_at" json:"read_at,omitempty"`
	CreatedAt int64   `db:"created_at" json:"created_at"`
}

type AgentConversation struct {
	ID         string  `db:"id" json:"id"`
	UserID     string  `db:"user_id" json:"user_id"`
	SpaceID    *string `db:"space_id" json:"space_id,omitempty"`
	Title      string  `db:"title" json:"title"`
	Model      string  `db:"model" json:"model"`
	Settings   string  `db:"settings" json:"-"`
	State      string  `db:"state" json:"-"`
	Usage      string  `db:"usage" json:"-"`
	CreatedAt  int64   `db:"created_at" json:"created_at"`
	UpdatedAt  int64   `db:"updated_at" json:"updated_at"`
	ArchivedAt *int64  `db:"archived_at" json:"archived_at,omitempty"`
}

type AgentMessage struct {
	ID             string `db:"id" json:"id"`
	ConversationID string `db:"conversation_id" json:"conversation_id"`
	Seq            int64  `db:"seq" json:"seq"`
	Role           string `db:"role" json:"role"`
	Parts          string `db:"parts" json:"-"`
	Meta           string `db:"meta" json:"-"`
	CreatedAt      int64  `db:"created_at" json:"created_at"`
}

type AgentCheckpoint struct {
	ID             string  `db:"id" json:"id"`
	ConversationID string  `db:"conversation_id" json:"conversation_id"`
	RunID          string  `db:"run_id" json:"run_id"`
	MessageID      *string `db:"message_id" json:"message_id,omitempty"`
	Changes        string  `db:"changes" json:"-"`
	CreatedAt      int64   `db:"created_at" json:"created_at"`
	RevertedAt     *int64  `db:"reverted_at" json:"reverted_at,omitempty"`
}

type AIProvider struct {
	ID        string `db:"id" json:"id"`
	Kind      string `db:"kind" json:"kind"`
	Name      string `db:"name" json:"name"`
	BaseURL   string `db:"base_url" json:"base_url"`
	APIKey    string `db:"api_key" json:"-"`
	Settings  string `db:"settings" json:"-"`
	Enabled   bool   `db:"enabled" json:"enabled"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	UpdatedAt int64  `db:"updated_at" json:"updated_at"`
}

type Setting struct {
	ID        string `db:"id" json:"id"`
	Value     string `db:"value" json:"value"`
	UpdatedAt int64  `db:"updated_at" json:"updated_at"`
}

type BackupTarget struct {
	ID         string `db:"id" json:"id"`
	Name       string `db:"name" json:"name"`
	Kind       string `db:"kind" json:"kind"`
	Config     string `db:"config" json:"-"`
	Mode       string `db:"mode" json:"mode"`
	Schedule   string `db:"schedule" json:"-"`
	Retention  string `db:"retention" json:"-"`
	Encryption string `db:"encryption" json:"-"`
	Enabled    bool   `db:"enabled" json:"enabled"`
	CreatedBy  string `db:"created_by" json:"created_by"`
	LastRunAt  *int64 `db:"last_run_at" json:"last_run_at,omitempty"`
	LastStatus string `db:"last_status" json:"last_status"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	UpdatedAt  int64  `db:"updated_at" json:"updated_at"`
}

type BackupRun struct {
	ID         string `db:"id" json:"id"`
	TargetID   string `db:"target_id" json:"target_id"`
	Kind       string `db:"kind" json:"kind"`
	StartedAt  int64  `db:"started_at" json:"started_at"`
	FinishedAt *int64 `db:"finished_at" json:"finished_at,omitempty"`
	Status     string `db:"status" json:"status"`
	Files      int64  `db:"files" json:"files"`
	Bytes      int64  `db:"bytes" json:"bytes"`
	Error      string `db:"error" json:"error"`
	Log        string `db:"log" json:"log"`
	Artifact   string `db:"artifact" json:"artifact"`
}

type AuditLog struct {
	ID     string  `db:"id" json:"id"`
	TS     int64   `db:"ts" json:"ts"`
	UserID *string `db:"user_id" json:"user_id,omitempty"`
	Action string  `db:"action" json:"action"`
	IP     string  `db:"ip" json:"ip"`
	UA     string  `db:"ua" json:"ua"`
	Target string  `db:"target" json:"target"`
	Detail string  `db:"detail" json:"detail"`
}

type Session struct {
	ID         string `db:"id" json:"-"`
	UserID     string `db:"user_id" json:"user_id"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	LastSeenAt int64  `db:"last_seen_at" json:"last_seen_at"`
	ExpiresAt  int64  `db:"expires_at" json:"expires_at"`
	IP         string `db:"ip" json:"ip"`
	UA         string `db:"ua" json:"ua"`
	MFAOK      bool   `db:"mfa_ok" json:"mfa_ok"`
	CSRF       string `db:"csrf" json:"-"`
	Data       string `db:"data" json:"-"`
}

type LoginAttempt struct {
	ID          string `db:"id"`
	Failures    int64  `db:"failures"`
	FirstAt     int64  `db:"first_at"`
	LastAt      int64  `db:"last_at"`
	LockedUntil int64  `db:"locked_until"`
}

type RecentPage struct {
	ID        string `db:"id"`
	UserID    string `db:"user_id"`
	PageID    string `db:"page_id"`
	VisitedAt int64  `db:"visited_at"`
}

// StrPtr 返回字符串指针（空串返回 nil）。
func StrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Int64Ptr 返回整数指针。
func Int64Ptr(v int64) *int64 { return &v }

// Deref 解引用字符串指针。
func Deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
