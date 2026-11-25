package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
)

type llmMessage = llm.Message

const maxRefChars = 24000

// buildUserParts 把用户输入与引用整理成消息片段：引用内容在发送时就展开并存下（之后的请求保持一致，也利于缓存）。
func (s *Service) buildUserParts(env *tools.Env, in SendInput) ([]StoredPart, error) {
	parts := []StoredPart{{Part: llm.Part{Type: "text", Text: strings.TrimSpace(in.Text)}, Refs: in.Refs}}
	if len(in.Refs) > 0 {
		var b strings.Builder
		b.WriteString("<用户引用的内容>\n")
		for i, r := range in.Refs {
			if i >= 20 {
				b.WriteString("（引用过多，其余省略）\n")
				break
			}
			b.WriteString(s.resolveRef(env, r))
			b.WriteString("\n")
		}
		b.WriteString("</用户引用的内容>")
		parts = append(parts, StoredPart{Part: llm.Part{Type: "text", Text: b.String()}, Hidden: true})
	}
	for _, img := range in.Images {
		f, err := s.toolSvc.Files.Get(env.Ctx, img.FileID)
		if err != nil || !strings.HasPrefix(f.Mime, "image/") || f.Size > 8<<20 {
			continue
		}
		if f.PageID != nil && env.PageRole(*f.PageID) == model.RoleNone {
			continue
		}
		fh, err := s.toolSvc.Files.Open(f)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(fh, 8<<20))
		fh.Close()
		parts = append(parts, StoredPart{Part: llm.Part{Type: "image", MediaType: f.Mime, Data: base64.StdEncoding.EncodeToString(data)}, Data: mustJSON(map[string]any{"file_id": f.ID, "name": f.Name})})
	}
	return parts, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (s *Service) resolveRef(env *tools.Env, r Ref) string {
	switch r.Kind {
	case "selection":
		meta, err := env.NeedPage(r.PageID, model.RoleViewer)
		if err != nil {
			return fmt.Sprintf("【选中的段落】（页面 %s 无法访问）", r.PageID)
		}
		text := r.Text
		if len([]rune(text)) > maxRefChars {
			text = string([]rune(text)[:maxRefChars]) + "…"
		}
		return fmt.Sprintf("【选中的段落】来自页面「%s」（id：%s）：\n%s", meta.Title, meta.ID, text)
	case "page", "row", "database":
		id := r.ID
		meta, err := env.NeedPage(id, model.RoleViewer)
		if err != nil {
			return fmt.Sprintf("【页面】%s 无法访问", id)
		}
		if meta.Kind == model.KindDatabase {
			return fmt.Sprintf("【数据库】「%s」（id：%s，路径：%s）。用 query_database 读取它的行与结构。", meta.Title, meta.ID, env.PathOf(meta.ID))
		}
		res := s.registry.Call(env, "read_page", mustJSON(map[string]any{"page_id": id, "limit": 2000}))
		text := res.Text
		if len([]rune(text)) > maxRefChars {
			text = string([]rune(text)[:maxRefChars]) + "\n…（内容较长已截断，需要时用 read_page 分段读取）"
		}
		return "【页面】\n" + text
	case "file":
		return fmt.Sprintf("【附件】%s（文件 id：%s）", r.Title, r.ID)
	}
	return fmt.Sprintf("【%s】%s（id：%s）", r.Kind, r.Title, r.ID)
}

// systemPrompt 组装系统提示：行为准则 + 当前环境 + 用户与空间的自定义指令。
func (s *Service) systemPrompt(ctx context.Context, env *tools.Env, conv *Conversation, currentPage string, sub string) string {
	zh := env.User.Locale != "en"
	var b strings.Builder
	if sub != "" {
		if zh {
			b.WriteString(subagentZH)
		} else {
			b.WriteString(subagentEN)
		}
		b.WriteString("\n\n")
		if zh {
			b.WriteString(promptZH[strings.Index(promptZH, "# 增强 Markdown"):])
		} else {
			b.WriteString(promptEN[strings.Index(promptEN, "# Enhanced Markdown"):])
		}
	} else if zh {
		b.WriteString(promptZH)
	} else {
		b.WriteString(promptEN)
	}
	now := time.Now()
	if zh {
		fmt.Fprintf(&b, "\n# 当前环境\n- 现在：%s（%s）\n- 用户：%s（@%s）\n", now.Format("2006-01-02 15:04 Monday"), now.Format("MST -07:00"), env.User.DisplayName, env.User.Username)
	} else {
		fmt.Fprintf(&b, "\n# Environment\n- Now: %s (%s)\n- User: %s (@%s)\n", now.Format("2006-01-02 15:04 Monday"), now.Format("MST -07:00"), env.User.DisplayName, env.User.Username)
	}
	b.WriteString("- ")
	if zh {
		b.WriteString("可访问的空间：")
	} else {
		b.WriteString("Spaces: ")
	}
	for i, sp := range env.VisibleSpaces() {
		if i > 0 {
			b.WriteString("；")
		}
		fmt.Fprintf(&b, "%s（id：%s）", sp.Name, sp.ID)
	}
	b.WriteString("\n")
	if currentPage != "" {
		if meta, err := env.NeedPage(currentPage, model.RoleViewer); err == nil {
			if zh {
				fmt.Fprintf(&b, "- 用户当前打开的页面：「%s」（id：%s，路径：%s）。用户说「这个页面」「这里」通常指它。\n", meta.Title, meta.ID, env.PathOf(meta.ID))
			} else {
				fmt.Fprintf(&b, "- Page currently open: \"%s\" (id: %s). \"This page\" usually refers to it.\n", meta.Title, meta.ID)
			}
		}
	}
	switch conv.Cfg.Mode {
	case ModePlan:
		if zh {
			b.WriteString("- 当前是只读规划模式：不能修改任何内容，只能查阅、分析并给出方案。\n")
		} else {
			b.WriteString("- Read-only planning mode: you cannot modify anything; investigate and propose a plan.\n")
		}
	}
	// 自定义指令
	var ui struct {
		Instructions string `json:"instructions"`
	}
	if ok, _ := s.settings.GetJSON(ctx, "user:"+env.User.ID+":agent", &ui); ok && strings.TrimSpace(ui.Instructions) != "" {
		if zh {
			b.WriteString("\n# 用户的自定义指令\n")
		} else {
			b.WriteString("\n# User instructions\n")
		}
		b.WriteString(strings.TrimSpace(ui.Instructions))
		b.WriteString("\n")
	}
	if conv.SpaceID != nil {
		if sp, ok := s.cat.Space(*conv.SpaceID); ok && sp.Settings != "" {
			var ss struct {
				AgentInstructions string `json:"agent_instructions"`
			}
			if json.Unmarshal([]byte(sp.Settings), &ss) == nil && strings.TrimSpace(ss.AgentInstructions) != "" {
				if zh {
					fmt.Fprintf(&b, "\n# 空间「%s」的约定\n%s\n", sp.Name, strings.TrimSpace(ss.AgentInstructions))
				} else {
					fmt.Fprintf(&b, "\n# Conventions of space \"%s\"\n%s\n", sp.Name, strings.TrimSpace(ss.AgentInstructions))
				}
			}
		}
	}
	return b.String()
}

// toLLM 把存储的消息转成模型消息，应用摘要与工具结果裁剪。
func toLLM(conv *Conversation, msgs []Message) []llm.Message {
	var out []llm.Message
	zh := true
	if conv.St.Summary != "" {
		head := "（以下是此前对话的摘要，之前的详细记录已省略）\n"
		if !zh {
			head = "(Summary of the earlier conversation)\n"
		}
		out = append(out, llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: head + conv.St.Summary}}},
			llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "好的，我已了解之前的进展，继续。"}}})
	}
	pendingCalls := map[string]string{}
	for _, m := range msgs {
		if m.Seq <= conv.St.SummaryUpTo {
			continue
		}
		var parts []llm.Part
		for _, p := range m.Parts {
			lp := p.Part
			if lp.Type == "tool_result" && m.Seq < conv.St.PruneBefore && len(lp.Text) > 400 {
				lp.Text = fmt.Sprintf("（此前 %s 的结果已省略以节省上下文；需要时重新调用）", p.Name)
			}
			if lp.Type == "image" && m.Seq < conv.St.PruneBefore {
				continue
			}
			if lp.Type == "text" && lp.Text == "" {
				continue
			}
			parts = append(parts, lp)
		}
		switch m.Role {
		case "user":
			// 上一条助手消息的工具调用没有结果（中断）时补上
			if len(pendingCalls) > 0 {
				out = append(out, interruptedResults(pendingCalls))
				pendingCalls = map[string]string{}
			}
			if len(parts) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == llm.RoleUser {
				out[n-1].Parts = append(out[n-1].Parts, parts...)
			} else {
				out = append(out, llm.Message{Role: llm.RoleUser, Parts: parts})
			}
		case "assistant":
			if len(pendingCalls) > 0 {
				out = append(out, interruptedResults(pendingCalls))
				pendingCalls = map[string]string{}
			}
			for _, p := range parts {
				if p.Type == "tool_call" {
					pendingCalls[p.CallID] = p.Name
				}
			}
			if len(parts) > 0 {
				out = append(out, llm.Message{Role: llm.RoleAssistant, Parts: parts})
			}
		case "tool":
			for _, p := range parts {
				if p.Type == "tool_result" {
					delete(pendingCalls, p.CallID)
				}
			}
			out = append(out, llm.Message{Role: llm.RoleTool, Parts: parts})
		}
	}
	if len(pendingCalls) > 0 {
		out = append(out, interruptedResults(pendingCalls))
	}
	return out
}

func interruptedResults(calls map[string]string) llm.Message {
	var parts []llm.Part
	for id, name := range calls {
		parts = append(parts, llm.Part{Type: "tool_result", CallID: id, Name: name, Text: "（执行被中断，没有结果）", IsError: true})
	}
	return llm.Message{Role: llm.RoleTool, Parts: parts}
}
