package llm

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// 真模型联调：设置 LEMON_NOTE_TEST_OPENCODE_KEY 才运行。
func TestLiveOpenCode(t *testing.T) {
	key := os.Getenv("LEMON_NOTE_TEST_OPENCODE_KEY")
	if key == "" {
		t.Skip("未设置 LEMON_NOTE_TEST_OPENCODE_KEY")
	}
	p := &OpenCodeGo{APIKey: key}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	models := os.Getenv("LEMON_NOTE_TEST_MODELS")
	if models == "" {
		models = "glm-5.3,qwen4-max,gpt-6-luna"
	}
	tool := ToolDef{Name: "get_weather", Description: "查询城市天气", Schema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}
	for _, id := range strings.Split(models, ",") {
		t.Run(id, func(t *testing.T) {
			req := Request{Model: id, System: "你是测试助手。需要天气时必须调用工具。", Tools: []ToolDef{tool}, Effort: "low", SessionID: "lemon-test",
				Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: "text", Text: "北京天气怎么样？"}}}}}
			call := run(t, p, ctx, req)
			if call == nil {
				t.Fatal("模型没有调用工具")
			}
			req.Messages = append(req.Messages, Message{Role: RoleAssistant, Parts: []Part{{Type: "tool_call", CallID: call.CallID, Name: call.Name, Args: json.RawMessage(call.Args)}}},
				Message{Role: RoleTool, Parts: []Part{{Type: "tool_result", CallID: call.CallID, Text: "晴，12 度"}}})
			run(t, p, ctx, req)
		})
	}
}

func run(t *testing.T, p *OpenCodeGo, ctx context.Context, req Request) *Event {
	s, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var text strings.Builder
	var call *Event
	for {
		e, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case EvText:
			text.WriteString(e.Text)
		case EvToolCallEnd:
			ev := e
			call = &ev
		case EvUsage:
			t.Logf("用量 %+v", e.Usage)
		case EvDone:
			t.Logf("结束 %s", e.StopReason)
		}
	}
	t.Logf("正文：%s", text.String())
	if call != nil {
		t.Logf("工具调用：%s %s", call.Name, call.Args)
	}
	return call
}
