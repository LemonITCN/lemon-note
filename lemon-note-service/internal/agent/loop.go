package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
)

// toolEnv 构造工具执行环境。
func (s *Service) toolEnv(ctx context.Context, user *model.User, conv *Conversation, run *Run) *tools.Env {
	env := &tools.Env{Ctx: ctx, User: user, CanWrite: conv.Cfg.Mode != ModePlan, Svc: s.toolSvc, Reads: s.readTracker(conv.ID), Agent: true}
	if run != nil {
		env.Client = &clientBridge{s: s, run: run}
		env.BeforeWrite = func(pageID string) error { return s.checkpoint(run, user.ID, pageID) }
		env.Created = func(pageID string) {
			run.ckptMu.Lock()
			run.created = append(run.created, pageID)
			run.ckptMu.Unlock()
		}
	}
	return env
}

// checkpoint 一轮里第一次写某个页面之前存历史版本，供「撤销本轮修改」使用。
func (s *Service) checkpoint(run *Run, userID, pageID string) error {
	run.ckptMu.Lock()
	defer run.ckptMu.Unlock()
	if _, ok := run.checkpoints[pageID]; ok {
		return nil
	}
	for _, c := range run.created {
		if c == pageID {
			run.checkpoints[pageID] = ""
			return nil
		}
	}
	vid, err := s.pages.SaveVersion(context.Background(), userID, pageID, "ai", map[string]any{"conversation": run.ConvID, "run": run.ID})
	if err != nil {
		return err
	}
	run.checkpoints[pageID] = vid
	return nil
}

type agentHooks struct {
	s    *Service
	run  *Run
	user *model.User
	conv *Conversation
	sub  bool
}

func (h *agentHooks) Ask(ctx context.Context, questions json.RawMessage) (string, error) {
	if h.sub {
		return "", errors.New("子智能体不能向用户提问")
	}
	rep, err := h.run.wait(ctx, "question", map[string]any{"questions": questions})
	if err != nil {
		return "", err
	}
	return rep.Answer, nil
}

func (h *agentHooks) Todo(items json.RawMessage) error {
	if h.sub {
		return nil
	}
	h.conv.St.Todos = items
	h.run.emit("todos", json.RawMessage(items))
	return nil
}

func (h *agentHooks) Task(ctx context.Context, description, prompt, kind string) (string, error) {
	if h.sub {
		return "", errors.New("子智能体不能再派出子智能体")
	}
	return h.s.runSub(ctx, h.run, h.user, h.conv, description, prompt, kind)
}

const maxToolResultChars = 60000

// execute 是一轮执行的主循环。
func (s *Service) execute(run *Run, user *model.User) {
	ctx := run.ctx
	status, errMsg := "done", ""
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("智能体执行崩溃", "panic", v)
			status, errMsg = "error", "内部错误"
		}
		if ctx.Err() != nil && status == "done" {
			status = "stopped"
		}
		s.finishRun(run, user, status, errMsg)
	}()
	conv, err := s.getConv(ctx, user.ID, run.ConvID)
	if err != nil {
		status, errMsg = "error", err.Error()
		return
	}
	prov, err := s.Provider(ctx)
	if err != nil {
		status, errMsg = "error", err.Error()
		return
	}
	models, err := prov.Models(ctx)
	if err != nil {
		status, errMsg = "error", fmtErr(err)
		return
	}
	if conv.Cfg.Model == "" {
		conv.Cfg.Model = s.DefaultModel(ctx, models)
	}
	var mdl llm.Model
	for _, m := range models {
		if m.ID == conv.Cfg.Model {
			mdl = m
		}
	}
	if mdl.ID == "" {
		status, errMsg = "error", "所选模型 "+conv.Cfg.Model+" 当前不可用，请换一个模型"
		return
	}
	env := s.toolEnv(ctx, user, conv, run)
	env.Agentic = &agentHooks{s: s, run: run, user: user, conv: conv}
	maxSteps := s.settings.System(ctx).AgentMaxSteps
	budget := s.settings.System(ctx).AgentBudgetTokens
	if lim := mdl.Context * 8 / 10; lim > 0 && budget > lim {
		budget = lim
	}
	currentPage := ""
	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			return
		}
		msgs, err := s.loadMessages(ctx, conv.ID)
		if err != nil {
			status, errMsg = "error", err.Error()
			return
		}
		currentPage = lastCurrentPage(msgs)
		// 插话已经入库，清空队列即可
		run.mu.Lock()
		run.steer = nil
		run.mu.Unlock()

		if conv.St.LastContext > budget*85/100 {
			if err := s.shrink(ctx, run, prov, conv, msgs, budget); err != nil {
				s.log.Warn("压缩上下文失败", "err", err)
			}
		}
		system := s.systemPrompt(ctx, env, conv, currentPage, "")
		toolList := s.registry.List(s.toolSvc, true, conv.Cfg.Mode != ModePlan)
		req := llm.Request{Model: mdl.ID, System: system, Messages: toLLM(conv, msgs), Tools: tools.Defs(toolList), Effort: conv.Cfg.Effort, SessionID: conv.ID}
		asst, usage, stop, err := s.streamStep(ctx, run, prov, req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			status, errMsg = "error", fmtErr(err)
			return
		}
		conv.Usage.Input += usage.Input
		conv.Usage.Output += usage.Output
		conv.Usage.CacheRead += usage.CacheRead
		conv.Usage.CacheWrite += usage.CacheWrite
		conv.Usage.Requests++
		if c := usage.Context(); c > 0 {
			conv.St.LastContext = c + usage.Output
		} else {
			conv.St.LastContext = estimateMessages(req.Messages) + estimateTokens(system)
		}
		run.emit("usage", map[string]any{"context": conv.St.LastContext, "budget": budget, "model_context": mdl.Context, "total": conv.Usage})
		var calls []llm.Part
		for _, p := range asst {
			if p.Type == "tool_call" {
				calls = append(calls, p.Part)
			}
		}
		if len(asst) > 0 {
			msg, err := s.appendMessage(context.WithoutCancel(ctx), conv, "assistant", asst, map[string]any{"model": mdl.ID, "usage": usage})
			if err == nil {
				run.emit("message_saved", map[string]any{"id": msg.ID, "seq": msg.Seq, "role": "assistant"})
			}
		}
		_ = s.saveConv(context.WithoutCancel(ctx), conv)
		if len(calls) == 0 {
			if stop == "max_tokens" {
				run.emit("notice", map[string]any{"text": "回复达到长度上限被截断"})
			}
			// 有插话时继续，否则结束
			run.mu.Lock()
			pending := len(run.steer) > 0
			run.mu.Unlock()
			if pending {
				continue
			}
			return
		}
		results := s.runTools(ctx, run, env, conv, calls, toolList)
		tmsg, err := s.appendMessage(context.WithoutCancel(ctx), conv, "tool", results, nil)
		if err != nil {
			status, errMsg = "error", err.Error()
			return
		}
		run.emit("message_saved", map[string]any{"id": tmsg.ID, "seq": tmsg.Seq, "role": "tool"})
		_ = s.saveConv(context.WithoutCancel(ctx), conv)
		if ctx.Err() != nil {
			return
		}
	}
	run.emit("notice", map[string]any{"text": fmt.Sprintf("已达到单轮最多 %d 步的上限，可以发「继续」接着做", maxSteps)})
}

func lastCurrentPage(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && len(msgs[i].Meta) > 0 {
			var m struct {
				CurrentPage string `json:"current_page"`
			}
			if json.Unmarshal(msgs[i].Meta, &m) == nil {
				return m.CurrentPage
			}
		}
	}
	return ""
}

// streamStep 发起一次模型请求，流式推送正文与思考，返回助手消息片段。首个输出之前的错误自动重试。
func (s *Service) streamStep(ctx context.Context, run *Run, prov llm.Provider, req llm.Request) ([]StoredPart, llm.Usage, string, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<attempt) * time.Second
			run.emit("retry", map[string]any{"attempt": attempt, "wait": wait.Seconds(), "error": fmtErr(lastErr)})
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, llm.Usage{}, "", ctx.Err()
			}
		}
		parts, usage, stop, err := s.streamOnce(ctx, run, prov, req)
		if err == nil {
			return parts, usage, stop, nil
		}
		lastErr = err
		if ctx.Err() != nil || !llm.IsRetryable(err) {
			return nil, llm.Usage{}, "", err
		}
		run.emit("step_reset", nil) // 丢弃这一步已推送的半截内容
	}
	return nil, llm.Usage{}, "", lastErr
}

func (s *Service) streamOnce(ctx context.Context, run *Run, prov llm.Provider, req llm.Request) ([]StoredPart, llm.Usage, string, error) {
	st, err := prov.Stream(ctx, req)
	if err != nil {
		return nil, llm.Usage{}, "", err
	}
	defer st.Close()
	var text, reasoning strings.Builder
	sig := ""
	var calls []StoredPart
	var usage llm.Usage
	stop := ""
	msgID := ids.New()
	run.emit("assistant_start", map[string]any{"id": msgID})
	for {
		ev, err := st.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, usage, "", err
		}
		switch ev.Type {
		case llm.EvText:
			text.WriteString(ev.Text)
			run.emit("text", map[string]any{"id": msgID, "text": ev.Text})
		case llm.EvReasoning:
			reasoning.WriteString(ev.Text)
			run.emit("reasoning", map[string]any{"id": msgID, "text": ev.Text})
		case llm.EvReasoningSig:
			sig += ev.Text
		case llm.EvToolCallStart:
			run.emit("tool_start", map[string]any{"call_id": ev.CallID, "name": ev.Name, "title": toolTitle(ev.Name)})
		case llm.EvToolCallEnd:
			args := json.RawMessage(ev.Args)
			if !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			calls = append(calls, StoredPart{Part: llm.Part{Type: "tool_call", CallID: ev.CallID, Name: ev.Name, Args: args}, Title: toolTitle(ev.Name)})
			run.emit("tool_call", map[string]any{"call_id": ev.CallID, "name": ev.Name, "title": toolTitle(ev.Name), "args": args})
		case llm.EvUsage:
			usage = ev.Usage
		case llm.EvDone:
			stop = ev.StopReason
		}
	}
	var parts []StoredPart
	if reasoning.Len() > 0 {
		parts = append(parts, StoredPart{Part: llm.Part{Type: "reasoning", Text: reasoning.String(), Signature: sig}})
	}
	if t := strings.TrimSpace(text.String()); t != "" {
		parts = append(parts, StoredPart{Part: llm.Part{Type: "text", Text: text.String()}})
	}
	parts = append(parts, calls...)
	run.emit("assistant_done", map[string]any{"id": msgID})
	return parts, usage, stop, nil
}

func toolTitle(name string) string {
	if t, ok := toolsRegistry().Get(name); ok && t.Title != "" {
		return t.Title
	}
	return name
}

func toolsRegistry() *tools.Registry { return tools.Default }

// needsApproval 按权限模式判断工具是否需要用户确认。
func needsApproval(mode string, t *tools.Tool, conv *Conversation) bool {
	if t.ReadOnly || t.AgentOnly {
		return false
	}
	for _, a := range conv.St.AllowedTools {
		if a == t.Name {
			return false
		}
	}
	switch mode {
	case ModeAskAll:
		return true
	case ModeAskDestructive:
		return t.Destructive
	}
	return false
}

// runTools 执行一批工具调用：只读工具并发（最多 4 个），写入工具按顺序。
func (s *Service) runTools(ctx context.Context, run *Run, env *tools.Env, conv *Conversation, calls []llm.Part, allowed []*tools.Tool) []StoredPart {
	allowedSet := map[string]*tools.Tool{}
	for _, t := range allowed {
		allowedSet[t.Name] = t
	}
	results := make([]StoredPart, len(calls))
	exec := func(i int) {
		c := calls[i]
		start := time.Now()
		t, ok := allowedSet[c.Name]
		var res *tools.Result
		status := "done"
		switch {
		case ctx.Err() != nil:
			res, status = &tools.Result{Text: "（用户已停止，未执行）", IsError: true}, "interrupted"
		case !ok:
			res, status = &tools.Result{Text: "错误：当前模式下不能使用工具 " + c.Name, IsError: true}, "error"
		default:
			if needsApproval(conv.Cfg.Mode, t, conv) {
				rep, err := run.wait(ctx, "approval", map[string]any{"call_id": c.CallID, "name": c.Name, "title": t.Title, "args": c.Args, "destructive": t.Destructive})
				if err != nil {
					res, status = &tools.Result{Text: "（用户已停止，未执行）", IsError: true}, "interrupted"
					break
				}
				if !rep.Approve {
					msg := "用户拒绝了这次操作。"
					if strings.TrimSpace(rep.Answer) != "" {
						msg += "用户的说明：" + rep.Answer
					}
					res, status = &tools.Result{Text: msg, IsError: true}, "denied"
					break
				}
				if rep.Always {
					conv.St.AllowedTools = append(conv.St.AllowedTools, t.Name)
				}
			}
			res = s.registry.Call(env, c.Name, c.Args)
			if res.IsError {
				status = "error"
			}
		}
		text := res.Text
		if len(text) > maxToolResultChars {
			text = text[:maxToolResultChars] + "\n…（结果过长已截断）"
		}
		var data json.RawMessage
		if res.Data != nil {
			data, _ = json.Marshal(res.Data)
		}
		sp := StoredPart{Part: llm.Part{Type: "tool_result", CallID: c.CallID, Name: c.Name, Text: text, IsError: res.IsError}, Data: data, Status: status, Duration: time.Since(start).Milliseconds(), Title: toolTitle(c.Name)}
		results[i] = sp
		run.emit("tool_result", map[string]any{"call_id": c.CallID, "name": c.Name, "status": status, "text": truncateRunes(text, 4000), "data": data, "duration": sp.Duration})
	}
	// 分组：连续的只读工具并发，遇到写工具就排队顺序执行
	i := 0
	for i < len(calls) {
		t, ok := allowedSet[calls[i].Name]
		if ok && t.ReadOnly && t.Name != "ask_user" {
			j := i
			for j < len(calls) {
				tj, okj := allowedSet[calls[j].Name]
				if !okj || !tj.ReadOnly || tj.Name == "ask_user" {
					break
				}
				j++
			}
			var wg sync.WaitGroup
			sem := make(chan struct{}, 4)
			for k := i; k < j; k++ {
				wg.Add(1)
				sem <- struct{}{}
				go func(k int) {
					defer wg.Done()
					defer func() { <-sem }()
					exec(k)
				}(k)
			}
			wg.Wait()
			i = j
			continue
		}
		exec(i)
		i++
	}
	// 工具结果里的图片随结果一起给模型
	var out []StoredPart
	for _, r := range results {
		out = append(out, r)
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// finishRun 收尾：记录检查点、推送结束事件。
func (s *Service) finishRun(run *Run, user *model.User, status, errMsg string) {
	run.ckptMu.Lock()
	changes := map[string]any{"pages": run.checkpoints, "created": run.created, "trashed": run.trashed}
	hasChanges := len(run.checkpoints) > 0 || len(run.created) > 0
	run.ckptMu.Unlock()
	var ckptID string
	if hasChanges {
		b, _ := json.Marshal(changes)
		ck := model.AgentCheckpoint{ID: ids.New(), ConversationID: run.ConvID, RunID: run.ID, Changes: string(b), CreatedAt: model.Now()}
		if _, err := s.st.Write(context.Background(), user.ID, func(tx *store.Tx) error { return tx.Put("agent_checkpoints", ck) }); err == nil {
			ckptID = ck.ID
		}
	}
	if status == "error" && errMsg != "" {
		conv, err := s.getConv(context.Background(), user.ID, run.ConvID)
		if err == nil {
			_, _ = s.appendMessage(context.Background(), conv, "assistant", []StoredPart{{Part: llm.Part{Type: "text"}, Status: "error", Data: mustJSON(map[string]any{"error": errMsg})}}, map[string]any{"error": true})
		}
	}
	run.setState(status, errMsg)
	run.emit("run_done", map[string]any{"status": status, "error": errMsg, "checkpoint_id": ckptID, "changed_pages": len(run.checkpoints) + len(run.created)})
	s.mu.Lock()
	if s.byConv[run.ConvID] == run {
		delete(s.byConv, run.ConvID)
	}
	s.mu.Unlock()
	s.gc()
}

// RevertCheckpoint 撤销一轮的修改：页面恢复到修改前，新建的移入回收站。
func (s *Service) RevertCheckpoint(ctx context.Context, user *model.User, ckptID string) (int, error) {
	var ck model.AgentCheckpoint
	if err := s.st.Get(ctx, &ck, "SELECT * FROM agent_checkpoints WHERE id = ?", ckptID); err != nil {
		return 0, errors.New("检查点不存在")
	}
	if _, err := s.getConv(ctx, user.ID, ck.ConversationID); err != nil {
		return 0, err
	}
	if ck.RevertedAt != nil {
		return 0, errors.New("这一轮的修改已经撤销过了")
	}
	var ch struct {
		Pages   map[string]string `json:"pages"`
		Created []string          `json:"created"`
	}
	_ = json.Unmarshal([]byte(ck.Changes), &ch)
	n := 0
	for pid, vid := range ch.Pages {
		if vid == "" || !s.cat.CanEdit(user.ID, pid) {
			continue
		}
		if err := s.pages.RestoreVersion(ctx, user.ID, pid, vid); err != nil {
			return n, err
		}
		n++
	}
	for _, pid := range ch.Created {
		if s.cat.CanEdit(user.ID, pid) {
			if err := s.pages.Trash(ctx, user.ID, pid); err == nil {
				n++
			}
		}
	}
	now := model.Now()
	ck.RevertedAt = &now
	_, err := s.st.Write(ctx, user.ID, func(tx *store.Tx) error { return tx.Put("agent_checkpoints", ck) })
	return n, err
}
