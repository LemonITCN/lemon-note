// Package agent 是内置智能体：服务端 agent loop、上下文组装与压缩、会话存储、检查点与撤销、子智能体、审批与提问。
package agent

import (
	"encoding/json"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
)

// 权限模式
const (
	ModePlan           = "plan"            // 只读规划：不能修改
	ModeAskAll         = "ask_all"         // 每次写入都确认
	ModeAskDestructive = "ask_destructive" // 删除类确认（默认）
	ModeAuto           = "auto"            // 全自动
)

// Ref 是聊天里引用的内容。
type Ref struct {
	Kind     string   `json:"kind"` // page | database | row | selection | file | view
	ID       string   `json:"id"`
	Title    string   `json:"title,omitempty"`
	Icon     string   `json:"icon,omitempty"`
	PageID   string   `json:"page_id,omitempty"`
	BlockIDs []string `json:"block_ids,omitempty"`
	Text     string   `json:"text,omitempty"`
	ViewID   string   `json:"view_id,omitempty"`
}

// StoredPart 是存库的消息片段：llm.Part 加界面需要的字段。
type StoredPart struct {
	llm.Part
	Hidden   bool            `json:"hidden,omitempty"`   // 不在界面显示（例如引用展开后的上下文）
	Refs     []Ref           `json:"refs,omitempty"`     // 用户消息里的引用
	Title    string          `json:"title,omitempty"`    // 工具的中文名
	Data     json.RawMessage `json:"data,omitempty"`     // 工具结果给界面的结构化数据
	Status   string          `json:"status,omitempty"`   // 工具：done | error | denied | interrupted
	Duration int64           `json:"duration,omitempty"` // 工具耗时毫秒
}

// ConvSettings 会话设置。
type ConvSettings struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Mode   string `json:"mode"`
}

// ConvState 会话的上下文管理状态。
type ConvState struct {
	Summary      string          `json:"summary,omitempty"`       // 压缩后的摘要
	SummaryUpTo  int64           `json:"summary_upto,omitempty"`  // 摘要覆盖到的消息序号（含）
	PruneBefore  int64           `json:"prune_before,omitempty"`  // 此序号之前的工具结果用占位说明代替
	LastContext  int             `json:"last_context,omitempty"`  // 最近一次请求的上下文 token
	AllowedTools []string        `json:"allowed_tools,omitempty"` // 本会话已「总是允许」的工具
	Todos        json.RawMessage `json:"todos,omitempty"`
}

// ConvUsage 会话累计用量。
type ConvUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Requests   int `json:"requests"`
}

// Event 是推给界面的事件。
type Event struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}
