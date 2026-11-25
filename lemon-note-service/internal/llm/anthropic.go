package llm

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// anthropicAdapter 实现 Anthropic Messages 协议。
type anthropicAdapter struct {
	baseURL     string
	headers     map[string]string
	thinking    bool // 模型支持思考
	temperature bool
}

var effortBudget = map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384, "xhigh": 32768, "max": 60000}

func (a *anthropicAdapter) stream(ctx context.Context, req Request) (Stream, error) {
	msgs := mergeSameRole(a.convertAll(req.Messages))
	// 提示词缓存：系统提示与最后一条消息打缓存点
	if n := len(msgs); n > 0 {
		if blocks, ok := msgs[n-1]["content"].([]map[string]any); ok && len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		}
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	body := map[string]any{"model": req.Model, "messages": msgs, "stream": true, "max_tokens": maxTokens}
	if req.System != "" {
		body["system"] = []map[string]any{{"type": "text", "text": req.System, "cache_control": map[string]any{"type": "ephemeral"}}}
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": json.RawMessage(t.Schema)})
		}
		body["tools"] = tools
	}
	if a.thinking && req.Effort != "" && req.Effort != "none" {
		budget := effortBudget[req.Effort]
		if budget == 0 {
			budget = 8192
		}
		if budget >= maxTokens {
			body["max_tokens"] = budget + 8000
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	} else if req.Temperature != nil && a.temperature {
		body["temperature"] = *req.Temperature
	}
	h := map[string]string{"anthropic-version": "2023-06-01"}
	for k, v := range a.headers {
		h[k] = v
	}
	resp, err := postStream(ctx, strings.TrimRight(a.baseURL, "/")+"/messages", h, body)
	if err != nil {
		return nil, err
	}
	return &anthropicStream{sse: newSSE(resp.Body), blocks: map[int]*anthBlock{}}, nil
}

func (a *anthropicAdapter) convertAll(ms []Message) []map[string]any {
	var out []map[string]any
	for _, m := range ms {
		var blocks []map[string]any
		role := "user"
		switch m.Role {
		case RoleAssistant:
			role = "assistant"
			for _, p := range m.Parts {
				switch p.Type {
				case "reasoning":
					if p.Signature != "" {
						blocks = append(blocks, map[string]any{"type": "thinking", "thinking": p.Text, "signature": p.Signature})
					}
				case "text":
					if p.Text != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
					}
				case "tool_call":
					var input any = map[string]any{}
					if len(p.Args) > 0 {
						if json.Unmarshal(p.Args, &input) != nil {
							input = map[string]any{}
						}
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": p.CallID, "name": p.Name, "input": input})
				}
			}
		case RoleTool:
			// 工具结果在 Anthropic 协议里是 user 消息里的 tool_result 块，图片放进结果内容里
			images := map[string][]map[string]any{}
			for _, p := range m.Parts {
				if p.Type == "image" && p.CallID != "" {
					images[p.CallID] = append(images[p.CallID], map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": p.MediaType, "data": p.Data}})
				}
			}
			for _, p := range m.Parts {
				if p.Type == "tool_result" {
					content := []map[string]any{{"type": "text", "text": nonEmpty(p.Text)}}
					content = append(content, images[p.CallID]...)
					blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": p.CallID, "content": content, "is_error": p.IsError})
				}
			}
		default:
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					if p.Text != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
					}
				case "image":
					blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": p.MediaType, "data": p.Data}})
				}
			}
		}
		if len(blocks) == 0 {
			blocks = []map[string]any{{"type": "text", "text": "（空）"}}
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	return out
}

func nonEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无输出）"
	}
	return s
}

// mergeSameRole 合并相邻同角色消息（Anthropic 要求角色交替）。
func mergeSameRole(ms []map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range ms {
		if n := len(out); n > 0 && out[n-1]["role"] == m["role"] {
			prev := out[n-1]["content"].([]map[string]any)
			out[n-1]["content"] = append(prev, m["content"].([]map[string]any)...)
			continue
		}
		out = append(out, m)
	}
	return out
}

type anthBlock struct {
	typ, id, name string
	args          strings.Builder
}

type anthropicStream struct {
	sse     *sseReader
	blocks  map[int]*anthBlock
	pending []Event
	done    bool
	usage   Usage
	stop    string
}

func (s *anthropicStream) Close() error { return s.sse.close() }

func (s *anthropicStream) Recv() (Event, error) {
	for {
		if len(s.pending) > 0 {
			e := s.pending[0]
			s.pending = s.pending[1:]
			return e, nil
		}
		if s.done {
			return Event{}, io.EOF
		}
		_, data, err := s.sse.next()
		if err == io.EOF {
			s.finish()
			continue
		}
		if err != nil {
			return Event{}, err
		}
		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage anthUsage `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage anthUsage `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			s.usage.Input = ev.Message.Usage.InputTokens
			s.usage.CacheRead = ev.Message.Usage.CacheRead
			s.usage.CacheWrite = ev.Message.Usage.CacheCreate
		case "content_block_start":
			b := &anthBlock{typ: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
			s.blocks[ev.Index] = b
			if b.typ == "tool_use" {
				s.pending = append(s.pending, Event{Type: EvToolCallStart, CallID: b.id, Name: b.name, Index: ev.Index})
			}
		case "content_block_delta":
			b := s.blocks[ev.Index]
			switch ev.Delta.Type {
			case "text_delta":
				if t := sanitizeText(ev.Delta.Text); t != "" {
					s.pending = append(s.pending, Event{Type: EvText, Text: t})
				}
			case "thinking_delta":
				s.pending = append(s.pending, Event{Type: EvReasoning, Text: ev.Delta.Thinking})
			case "signature_delta":
				s.pending = append(s.pending, Event{Type: EvReasoningSig, Text: ev.Delta.Signature})
			case "input_json_delta":
				if b != nil {
					b.args.WriteString(ev.Delta.PartialJSON)
					s.pending = append(s.pending, Event{Type: EvToolCallDelta, CallID: b.id, Text: ev.Delta.PartialJSON, Index: ev.Index})
				}
			}
		case "content_block_stop":
			if b := s.blocks[ev.Index]; b != nil && b.typ == "tool_use" {
				args := b.args.String()
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				s.pending = append(s.pending, Event{Type: EvToolCallEnd, CallID: b.id, Name: b.name, Args: args, Index: ev.Index})
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				s.stop = ev.Delta.StopReason
			}
			if ev.Usage.OutputTokens > 0 {
				s.usage.Output = ev.Usage.OutputTokens
			}
		case "message_stop":
			s.finish()
		case "error":
			return Event{}, &APIError{Message: ev.Error.Message, Retryable: ev.Error.Type == "overloaded_error" || ev.Error.Type == "api_error"}
		}
	}
}

type anthUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read_input_tokens"`
	CacheCreate  int `json:"cache_creation_input_tokens"`
}

func (s *anthropicStream) finish() {
	if s.done {
		return
	}
	s.done = true
	stop := "end_turn"
	switch s.stop {
	case "tool_use":
		stop = "tool_use"
	case "max_tokens":
		stop = "max_tokens"
	}
	s.pending = append(s.pending, Event{Type: EvUsage, Usage: s.usage}, Event{Type: EvDone, StopReason: stop})
}
