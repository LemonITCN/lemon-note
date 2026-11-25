package agent

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
)

// shrink 上下文接近预算时：先把较早的工具结果换成占位说明，仍然太大再把较早对话压缩成摘要。
func (s *Service) shrink(ctx context.Context, run *Run, prov llm.Provider, conv *Conversation, msgs []Message, budget int) error {
	// 1. 裁剪：保留最近 3 条助手消息之后的工具结果
	cnt := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			cnt++
			if cnt == 3 {
				if msgs[i].Seq > conv.St.PruneBefore {
					conv.St.PruneBefore = msgs[i].Seq
				}
				break
			}
		}
	}
	est := estimateMessages(toLLM(conv, msgs))
	if est < budget*70/100 {
		conv.St.LastContext = est
		return nil
	}
	// 2. 压缩：保留最近两条用户消息开始的内容，其余做摘要
	keepFrom := int64(0)
	users := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			users++
			if users == 2 {
				keepFrom = msgs[i].Seq
				break
			}
		}
	}
	if keepFrom <= conv.St.SummaryUpTo+1 {
		// 最近两轮本身就很大：只能压缩到最后一条用户消息之前
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				keepFrom = msgs[i].Seq
				break
			}
		}
	}
	if keepFrom <= conv.St.SummaryUpTo+1 {
		return errors.New("没有可压缩的历史")
	}
	var older []Message
	for _, m := range msgs {
		if m.Seq < keepFrom {
			older = append(older, m)
		}
	}
	run.emit("compaction_start", nil)
	zh := true
	prompt := compactZH
	if !zh {
		prompt = compactEN
	}
	hist := toLLM(conv, older)
	hist = append(hist, llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: prompt}}})
	st, err := prov.Stream(ctx, llm.Request{Model: conv.Cfg.Model, System: "你负责把对话压缩成摘要。", Messages: hist, SessionID: conv.ID, MaxTokens: 8000})
	if err != nil {
		run.emit("compaction_done", map[string]any{"ok": false})
		return err
	}
	defer st.Close()
	var b strings.Builder
	for {
		ev, err := st.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			run.emit("compaction_done", map[string]any{"ok": false})
			return err
		}
		if ev.Type == llm.EvText {
			b.WriteString(ev.Text)
		}
	}
	summary := strings.TrimSpace(b.String())
	if summary == "" {
		run.emit("compaction_done", map[string]any{"ok": false})
		return errors.New("模型没有返回摘要")
	}
	conv.St.Summary = summary
	conv.St.SummaryUpTo = keepFrom - 1
	conv.St.LastContext = estimateMessages(toLLM(conv, msgs))
	run.emit("compaction_done", map[string]any{"ok": true, "summary": truncateRunes(summary, 2000)})
	return s.saveConv(context.WithoutCancel(ctx), conv)
}

var subExcluded = map[string]bool{"ask_user": true, "task": true, "ui_open": true, "todo_write": true}

// runSub 运行子智能体：独立上下文，不入库，只把最终结论交回。
func (s *Service) runSub(ctx context.Context, run *Run, user *userT, conv *Conversation, desc, prompt, kind string) (string, error) {
	prov, err := s.Provider(ctx)
	if err != nil {
		return "", err
	}
	subConv := *conv
	if kind != "editor" {
		subConv.Cfg.Mode = ModePlan
	}
	env := s.toolEnv(ctx, user, &subConv, run)
	env.Agentic = &agentHooks{s: s, run: run, user: user, conv: &subConv, sub: true}
	var list []*tools.Tool
	for _, t := range s.registry.List(s.toolSvc, true, kind == "editor") {
		if !subExcluded[t.Name] {
			list = append(list, t)
		}
	}
	system := s.systemPrompt(ctx, env, &subConv, "", "sub")
	msgs := []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: prompt}}}}
	run.emit("sub_start", map[string]any{"task": desc})
	defer run.emit("sub_done", map[string]any{"task": desc})
	for step := 0; step < 60; step++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		req := llm.Request{Model: conv.Cfg.Model, System: system, Messages: msgs, Tools: tools.Defs(list), Effort: conv.Cfg.Effort, SessionID: conv.ID + "-sub"}
		var parts []llm.Part
		var text strings.Builder
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			parts, text, lastErr = s.subStream(ctx, prov, req)
			if lastErr == nil || !llm.IsRetryable(lastErr) {
				break
			}
		}
		if lastErr != nil {
			return "", lastErr
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Parts: parts})
		var calls []llm.Part
		for _, p := range parts {
			if p.Type == "tool_call" {
				calls = append(calls, p)
			}
		}
		if len(calls) == 0 {
			out := strings.TrimSpace(text.String())
			if out == "" {
				out = "（子智能体没有给出结论）"
			}
			return out, nil
		}
		stored := s.runTools(ctx, run, env, &subConv, calls, list, false) // 子智能体不传图片，图片结果换成文字说明
		var res []llm.Part
		for _, sp := range stored {
			res = append(res, sp.Part)
			run.emit("sub_progress", map[string]any{"task": desc, "tool": sp.Name, "title": sp.Title, "status": sp.Status})
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleTool, Parts: res})
		if est := estimateMessages(msgs); est > 120000 {
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: "上下文快满了，请停止调用工具，立即根据已有信息给出最终结论。"}}})
		}
	}
	return "（子智能体达到步数上限，未能完成）", nil
}

func (s *Service) subStream(ctx context.Context, prov llm.Provider, req llm.Request) ([]llm.Part, strings.Builder, error) {
	var text, reasoning strings.Builder
	st, err := prov.Stream(ctx, req)
	if err != nil {
		return nil, text, err
	}
	defer st.Close()
	var parts []llm.Part
	sig := ""
	for {
		ev, err := st.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, text, err
		}
		switch ev.Type {
		case llm.EvText:
			text.WriteString(ev.Text)
		case llm.EvReasoning:
			reasoning.WriteString(ev.Text)
		case llm.EvReasoningSig:
			sig += ev.Text
		case llm.EvToolCallEnd:
			parts = append(parts, llm.Part{Type: "tool_call", CallID: ev.CallID, Name: ev.Name, Args: []byte(ev.Args)})
		}
	}
	head := []llm.Part{}
	if reasoning.Len() > 0 {
		head = append(head, llm.Part{Type: "reasoning", Text: reasoning.String(), Signature: sig})
	}
	if text.Len() > 0 {
		head = append(head, llm.Part{Type: "text", Text: text.String()})
	}
	return append(head, parts...), text, nil
}
