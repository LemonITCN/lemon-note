// Package settings 读写系统级设置（存在 settings 表，进日志，随数据目录备份）。
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// System 是系统设置。新增字段必须有合理的零值或在 Defaults 里给默认值。
type System struct {
	SiteName          string    `json:"site_name"`
	AllowRegistration bool      `json:"allow_registration"`
	RequireMFA        bool      `json:"require_mfa"`
	SessionIdleDays   int       `json:"session_idle_days"`
	SessionMaxDays    int       `json:"session_max_days"`
	MaxUploadMB       int       `json:"max_upload_mb"`
	PublicSharing     bool      `json:"public_sharing"`
	DefaultLocale     string    `json:"default_locale"`
	TrashRetainDays   int       `json:"trash_retain_days"`
	HistoryRetainDays int       `json:"history_retain_days"`
	WebSearch         WebSearch `json:"web_search"`
	AgentDefaultModel string    `json:"agent_default_model"`
	AgentMaxSteps     int       `json:"agent_max_steps"`
	AgentBudgetTokens int       `json:"agent_budget_tokens"`
}

// WebSearch 智能体联网搜索配置（APIKey 加密存储）。
type WebSearch struct {
	Provider string `json:"provider"` // "" | searxng | tavily | bocha
	URL      string `json:"url"`
	APIKey   string `json:"api_key"`
}

// Defaults 返回默认系统设置。
func Defaults() System {
	return System{
		SiteName: "Lemon Note", SessionIdleDays: 14, SessionMaxDays: 30, MaxUploadMB: 200, PublicSharing: true,
		DefaultLocale: "zh-CN", TrashRetainDays: 30, HistoryRetainDays: 90, AgentMaxSteps: 200, AgentBudgetTokens: 160000,
	}
}

// Service 带缓存的设置读写。
type Service struct {
	st    *store.Store
	mu    sync.RWMutex
	cache *System
}

func New(st *store.Store) *Service {
	s := &Service{st: st}
	st.Subscribe(func(c store.Commit) {
		for _, op := range c.Ops {
			if op.T == "settings" && op.ID == "system" {
				s.mu.Lock()
				s.cache = nil
				s.mu.Unlock()
			}
		}
	})
	return s
}

// System 返回系统设置。
func (s *Service) System(ctx context.Context) System {
	s.mu.RLock()
	if s.cache != nil {
		v := *s.cache
		s.mu.RUnlock()
		return v
	}
	s.mu.RUnlock()
	v := Defaults()
	var raw string
	err := s.st.QueryRow(ctx, "SELECT value FROM settings WHERE id = ?", "system").Scan(&raw)
	if err == nil {
		_ = json.Unmarshal([]byte(raw), &v)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return v
	}
	s.mu.Lock()
	s.cache = &v
	s.mu.Unlock()
	return v
}

// SaveSystem 保存系统设置。
func (s *Service) SaveSystem(ctx context.Context, actor string, v System) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.st.Write(ctx, actor, func(tx *store.Tx) error {
		return tx.Put("settings", model.Setting{ID: "system", Value: string(b), UpdatedAt: model.Now()})
	})
	return err
}

// GetJSON 读任意键（例如 "user:<id>:agent"）。
func (s *Service) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	var raw string
	err := s.st.QueryRow(ctx, "SELECT value FROM settings WHERE id = ?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), dst)
}

// PutJSON 写任意键。
func (s *Service) PutJSON(ctx context.Context, actor, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.st.Write(ctx, actor, func(tx *store.Tx) error {
		return tx.Put("settings", model.Setting{ID: key, Value: string(b), UpdatedAt: model.Now()})
	})
	return err
}
