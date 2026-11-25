// Package llm 是模型供应商层：统一的消息、工具定义与流事件，三种协议适配器（OpenAI Chat Completions、
// Anthropic Messages、OpenAI Responses），以及 OpenCode Go 供应商。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // 工具结果（Anthropic 协议里会并到 user 消息）
)

// Part 是消息的一个片段。
type Part struct {
	Type string `json:"type"` // text | image | reasoning | tool_call | tool_result

	Text string `json:"text,omitempty"`

	// image
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // base64

	// reasoning：部分协议要求把带签名的思考原样回传
	Signature string `json:"signature,omitempty"`

	// tool_call / tool_result
	CallID  string          `json:"call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

// Message 是一条消息。
type Message struct {
	Role  Role   `json:"role"`
	Parts []Part `json:"parts"`
}

// Text 返回消息里的全部文字。
func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ToolDef 是给模型的工具定义。
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Request 是一次模型请求。
type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolDef
	MaxTokens   int
	Effort      string // 推理档位：none | low | medium | high | xhigh | max（按模型支持的取值）
	Temperature *float64
	SessionID   string // 会话标识（OpenCode Go 必带）
}

// Usage 是用量。
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
	Reasoning  int `json:"reasoning,omitempty"`
}

// Total 返回本次请求占用的上下文（输入 + 缓存）。
func (u Usage) Context() int { return u.Input + u.CacheRead + u.CacheWrite }

// EventType 流事件类型。
type EventType string

const (
	EvText          EventType = "text"
	EvReasoning     EventType = "reasoning"
	EvReasoningSig  EventType = "reasoning_signature"
	EvToolCallStart EventType = "tool_call_start"
	EvToolCallDelta EventType = "tool_call_delta"
	EvToolCallEnd   EventType = "tool_call_end"
	EvUsage         EventType = "usage"
	EvDone          EventType = "done"
)

// Event 是流式响应里的一个事件。
type Event struct {
	Type       EventType
	Text       string
	CallID     string
	Name       string
	Args       string // tool_call_end 时是完整参数
	Index      int
	Usage      Usage
	StopReason string // end_turn | tool_use | max_tokens | ...
}

// Stream 逐个读取事件；读完返回 io.EOF。
type Stream interface {
	Recv() (Event, error)
	Close() error
}

// Model 是模型信息。
type Model struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Context        int      `json:"context"`
	MaxOutput      int      `json:"max_output"`
	Vision         bool     `json:"vision"`
	PDF            bool     `json:"pdf"`
	Efforts        []string `json:"efforts,omitempty"`
	DefaultEffort  string   `json:"default_effort,omitempty"`
	API            string   `json:"api"`
	BaseURL        string   `json:"-"`
	ReasoningField string   `json:"-"`
	Temperature    bool     `json:"-"`
	Featured       bool     `json:"featured,omitempty"`
	Quota          int      `json:"quota,omitempty"` // 套餐里每月可用额度（相对值，越大越耐用）
	Type           string   `json:"type,omitempty"`  // chat | embedding
}

// Provider 是一个模型供应商。
type Provider interface {
	Models(ctx context.Context) ([]Model, error)
	Stream(ctx context.Context, req Request) (Stream, error)
}

// APIError 是供应商返回的错误。
type APIError struct {
	Status    int
	Message   string
	Retryable bool
}

func (e *APIError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("模型服务返回 %d：%s", e.Status, e.Message)
	}
	return "模型服务错误：" + e.Message
}

// IsRetryable 判断错误是否值得重试。
func IsRetryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Retryable
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"connection reset", "EOF", "timeout", "terminated", "broken pipe", "TLS handshake", "no such host", "connection refused", "stream error"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func retryableStatus(code int) bool {
	return code == 408 || code == 409 || code == 425 || code == 429 || code >= 500
}

// sanitizeText 清洗个别模型把工具调用标记漏进正文的情况。
func sanitizeText(s string) string {
	for _, bad := range []string{"</tool_call>", "<tool_call>", "<|tool_call_end|>", "<|tool_calls_section_end|>"} {
		s = strings.ReplaceAll(s, bad, "")
	}
	return s
}
