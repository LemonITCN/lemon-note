package llm

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// chatAdapter 实现 OpenAI Chat Completions 协议。
type chatAdapter struct {
	baseURL        string
	headers        map[string]string
	reasoningField string // 回传思考内容的字段名（为空表示不回传）
	supportsEffort bool
	temperature    bool
}

func (a *chatAdapter) stream(ctx context.Context, req Request) (Stream, error) {
	msgs := []map[string]any{}
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, a.convert(m)...)
	}
	body := map[string]any{"model": req.Model, "messages": msgs, "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": json.RawMessage(t.Schema)}})
		}
		body["tools"] = tools
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Effort != "" && a.supportsEffort {
		body["reasoning_effort"] = req.Effort
	}
	if req.Temperature != nil && a.temperature {
		body["temperature"] = *req.Temperature
	}
	resp, err := postStream(ctx, strings.TrimRight(a.baseURL, "/")+"/chat/completions", a.headers, body)
	if err != nil {
		return nil, err
	}
	return &chatStream{sse: newSSE(resp.Body), calls: map[int]*callAcc{}}, nil
}

func (a *chatAdapter) convert(m Message) []map[string]any {
	switch m.Role {
	case RoleUser:
		hasImage := false
		for _, p := range m.Parts {
			if p.Type == "image" {
				hasImage = true
			}
		}
		if !hasImage {
			return []map[string]any{{"role": "user", "content": m.Text()}}
		}
		var parts []map[string]any
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				parts = append(parts, map[string]any{"type": "text", "text": p.Text})
			case "image":
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + p.MediaType + ";base64," + p.Data}})
			}
		}
		return []map[string]any{{"role": "user", "content": parts}}
	case RoleAssistant:
		out := map[string]any{"role": "assistant"}
		var text, reasoning strings.Builder
		var calls []map[string]any
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				text.WriteString(p.Text)
			case "reasoning":
				reasoning.WriteString(p.Text)
			case "tool_call":
				args := string(p.Args)
				if args == "" {
					args = "{}"
				}
				calls = append(calls, map[string]any{"id": p.CallID, "type": "function", "function": map[string]any{"name": p.Name, "arguments": args}})
			}
		}
		if text.Len() > 0 {
			out["content"] = text.String()
		} else {
			out["content"] = nil
		}
		if len(calls) > 0 {
			out["tool_calls"] = calls
		}
		if a.reasoningField != "" && reasoning.Len() > 0 {
			out[a.reasoningField] = reasoning.String()
		}
		return []map[string]any{out}
	case RoleTool:
		var out []map[string]any
		var images []map[string]any
		for _, p := range m.Parts {
			if p.Type == "tool_result" {
				out = append(out, map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": p.Text})
			}
			if p.Type == "image" {
				images = append(images, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + p.MediaType + ";base64," + p.Data}})
			}
		}
		if len(images) > 0 {
			out = append(out, map[string]any{"role": "user", "content": append([]map[string]any{{"type": "text", "text": "（以上工具返回的图片）"}}, images...)})
		}
		return out
	}
	return nil
}

type callAcc struct {
	id, name string
	args     strings.Builder
	started  bool
}

type chatStream struct {
	sse     *sseReader
	calls   map[int]*callAcc
	pending []Event
	done    bool
	stop    string
}

func (s *chatStream) Close() error { return s.sse.close() }

func (s *chatStream) Recv() (Event, error) {
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
		if data == "[DONE]" {
			s.finish()
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          *string `json:"content"`
					ReasoningContent *string `json:"reasoning_content"`
					Reasoning        *string `json:"reasoning"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				PromptTokensDetails *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionTokensDetails *struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return Event{}, &APIError{Message: chunk.Error.Message, Retryable: true}
		}
		if chunk.Usage != nil {
			u := Usage{Input: chunk.Usage.PromptTokens, Output: chunk.Usage.CompletionTokens}
			if d := chunk.Usage.PromptTokensDetails; d != nil {
				u.CacheRead = d.CachedTokens
				u.Input -= d.CachedTokens
			}
			if d := chunk.Usage.CompletionTokensDetails; d != nil {
				u.Reasoning = d.ReasoningTokens
			}
			s.pending = append(s.pending, Event{Type: EvUsage, Usage: u})
		}
		for _, ch := range chunk.Choices {
			d := ch.Delta
			if d.ReasoningContent != nil && *d.ReasoningContent != "" {
				s.pending = append(s.pending, Event{Type: EvReasoning, Text: *d.ReasoningContent})
			} else if d.Reasoning != nil && *d.Reasoning != "" {
				s.pending = append(s.pending, Event{Type: EvReasoning, Text: *d.Reasoning})
			}
			if d.Content != nil && *d.Content != "" {
				if t := sanitizeText(*d.Content); t != "" {
					s.pending = append(s.pending, Event{Type: EvText, Text: t})
				}
			}
			for _, tc := range d.ToolCalls {
				acc := s.calls[tc.Index]
				if acc == nil {
					acc = &callAcc{}
					s.calls[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name += tc.Function.Name
				}
				if !acc.started && acc.name != "" {
					acc.started = true
					s.pending = append(s.pending, Event{Type: EvToolCallStart, CallID: acc.id, Name: acc.name, Index: tc.Index})
				}
				if tc.Function.Arguments != "" {
					acc.args.WriteString(tc.Function.Arguments)
					s.pending = append(s.pending, Event{Type: EvToolCallDelta, CallID: acc.id, Text: tc.Function.Arguments, Index: tc.Index})
				}
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				s.stop = *ch.FinishReason
			}
		}
	}
}

func (s *chatStream) finish() {
	if s.done {
		return
	}
	s.done = true
	idx := make([]int, 0, len(s.calls))
	for i := range s.calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		c := s.calls[i]
		if c.id == "" {
			c.id = "call_" + randID()
		}
		args := c.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		s.pending = append(s.pending, Event{Type: EvToolCallEnd, CallID: c.id, Name: c.name, Args: args, Index: i})
	}
	stop := "end_turn"
	switch s.stop {
	case "tool_calls", "function_call":
		stop = "tool_use"
	case "length":
		stop = "max_tokens"
	}
	if len(s.calls) > 0 {
		stop = "tool_use"
	}
	s.pending = append(s.pending, Event{Type: EvDone, StopReason: stop})
}
