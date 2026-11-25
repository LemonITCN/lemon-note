package llm

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// responsesAdapter 实现 OpenAI Responses 协议（无状态，不依赖服务端存储）。
type responsesAdapter struct {
	baseURL        string
	headers        map[string]string
	supportsEffort bool
}

func (a *responsesAdapter) stream(ctx context.Context, req Request) (Stream, error) {
	var input []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			var content []map[string]any
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					content = append(content, map[string]any{"type": "input_text", "text": p.Text})
				case "image":
					content = append(content, map[string]any{"type": "input_image", "image_url": "data:" + p.MediaType + ";base64," + p.Data})
				}
			}
			input = append(input, map[string]any{"role": "user", "content": content})
		case RoleAssistant:
			var text strings.Builder
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					text.WriteString(p.Text)
				case "tool_call":
					args := string(p.Args)
					if args == "" {
						args = "{}"
					}
					if text.Len() > 0 {
						input = append(input, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": text.String()}}})
						text.Reset()
					}
					input = append(input, map[string]any{"type": "function_call", "call_id": p.CallID, "name": p.Name, "arguments": args})
				}
			}
			if text.Len() > 0 {
				input = append(input, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": text.String()}}})
			}
		case RoleTool:
			var images []map[string]any
			for _, p := range m.Parts {
				switch p.Type {
				case "tool_result":
					input = append(input, map[string]any{"type": "function_call_output", "call_id": p.CallID, "output": nonEmpty(p.Text)})
				case "image":
					images = append(images, map[string]any{"type": "input_image", "image_url": "data:" + p.MediaType + ";base64," + p.Data})
				}
			}
			if len(images) > 0 {
				input = append(input, map[string]any{"role": "user", "content": append([]map[string]any{{"type": "input_text", "text": "（以上工具返回的图片）"}}, images...)})
			}
		}
	}
	body := map[string]any{"model": req.Model, "input": input, "stream": true, "store": false}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": json.RawMessage(t.Schema)})
		}
		body["tools"] = tools
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if req.Effort != "" && a.supportsEffort {
		body["reasoning"] = map[string]any{"effort": req.Effort, "summary": "auto"}
	}
	resp, err := postStream(ctx, strings.TrimRight(a.baseURL, "/")+"/responses", a.headers, body)
	if err != nil {
		return nil, err
	}
	return &responsesStream{sse: newSSE(resp.Body), calls: map[string]*callAcc{}}, nil
}

type responsesStream struct {
	sse     *sseReader
	calls   map[string]*callAcc // item id → 调用
	order   []string
	pending []Event
	done    bool
}

func (s *responsesStream) Close() error { return s.sse.close() }

func (s *responsesStream) Recv() (Event, error) {
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
			s.finish(Usage{}, "")
			continue
		}
		if err != nil {
			return Event{}, err
		}
		var ev struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			ItemID string `json:"item_id"`
			Item   struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			Response struct {
				Status string `json:"status"`
				Usage  struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					InputDetails struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
					OutputDetails struct {
						ReasoningTokens int `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			if t := sanitizeText(ev.Delta); t != "" {
				s.pending = append(s.pending, Event{Type: EvText, Text: t})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			s.pending = append(s.pending, Event{Type: EvReasoning, Text: ev.Delta})
		case "response.output_item.added":
			if ev.Item.Type == "function_call" {
				c := &callAcc{id: ev.Item.CallID, name: ev.Item.Name, started: true}
				s.calls[ev.Item.ID] = c
				s.order = append(s.order, ev.Item.ID)
				s.pending = append(s.pending, Event{Type: EvToolCallStart, CallID: c.id, Name: c.name})
			}
		case "response.function_call_arguments.delta":
			if c := s.calls[ev.ItemID]; c != nil {
				c.args.WriteString(ev.Delta)
				s.pending = append(s.pending, Event{Type: EvToolCallDelta, CallID: c.id, Text: ev.Delta})
			}
		case "response.output_item.done":
			if ev.Item.Type == "function_call" {
				c := s.calls[ev.Item.ID]
				if c == nil {
					c = &callAcc{id: ev.Item.CallID, name: ev.Item.Name}
					s.calls[ev.Item.ID] = c
					s.order = append(s.order, ev.Item.ID)
				}
				if ev.Item.Arguments != "" {
					c.args.Reset()
					c.args.WriteString(ev.Item.Arguments)
				}
			}
		case "response.completed", "response.incomplete":
			u := ev.Response.Usage
			usage := Usage{Input: u.InputTokens - u.InputDetails.CachedTokens, CacheRead: u.InputDetails.CachedTokens, Output: u.OutputTokens, Reasoning: u.OutputDetails.ReasoningTokens}
			reason := ""
			if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "max_output_tokens" {
				reason = "max_tokens"
			}
			s.finish(usage, reason)
		case "response.failed":
			msg := "请求失败"
			if ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			return Event{}, &APIError{Message: msg, Retryable: true}
		case "error":
			return Event{}, &APIError{Message: ev.Message, Retryable: true}
		}
	}
}

func (s *responsesStream) finish(u Usage, reason string) {
	if s.done {
		return
	}
	s.done = true
	for _, id := range s.order {
		c := s.calls[id]
		args := c.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		s.pending = append(s.pending, Event{Type: EvToolCallEnd, CallID: c.id, Name: c.name, Args: args})
	}
	stop := "end_turn"
	if len(s.order) > 0 {
		stop = "tool_use"
	} else if reason != "" {
		stop = reason
	}
	s.pending = append(s.pending, Event{Type: EvUsage, Usage: u}, Event{Type: EvDone, StopReason: stop})
}
