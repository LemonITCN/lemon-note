package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
)

func init() {
	Default.Register(&Tool{
		Name:  "todo_write",
		Title: "计划清单",
		Description: "维护本次任务的计划清单（整份替换）。多步骤任务（3 步以上）开始前先列计划，执行中及时更新状态：" +
			"同一时间只有一项 in_progress，完成一项立刻标为 completed。简单问答不需要计划。",
		Schema: obj(map[string]any{
			"todos": arr(obj(map[string]any{
				"content": str("这一步要做什么（祈使句，简短）"),
				"status":  enum("状态", "pending", "in_progress", "completed"),
			}, "content", "status"), "完整的计划清单"),
		}, "todos"),
		ReadOnly:  true,
		AgentOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Todos []struct {
					Content string `json:"content"`
					Status  string `json:"status"`
				} `json:"todos"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if env.Agentic == nil {
				return nil, fail("当前环境不支持计划清单")
			}
			if err := env.Agentic.Todo(args); err != nil {
				return nil, err
			}
			done := 0
			for _, t := range in.Todos {
				if t.Status == "completed" {
					done++
				}
			}
			return &Result{Text: fmt.Sprintf("计划已更新（%d/%d 完成）。", done, len(in.Todos))}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "ask_user",
		Title: "向用户提问",
		Description: "需求不明确、有多种合理做法、或者要做影响较大的改动前，用它向用户提 1-4 个结构化问题，等用户回答后再继续。" +
			"每个问题给 2-4 个选项（用户也可以自由输入）。能从笔记内容或常识判断的事情不要问。",
		Schema: obj(map[string]any{
			"questions": arr(obj(map[string]any{
				"question": str("完整的问题"),
				"header":   str("很短的标签（不超过 8 个字）"),
				"multi":    boolean("是否可多选"),
				"options": arr(obj(map[string]any{
					"label":       str("选项（简短）"),
					"description": str("选项说明"),
				}, "label"), "2-4 个选项"),
			}, "question", "options"), "问题列表"),
		}, "questions"),
		ReadOnly:  true,
		AgentOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			if env.Agentic == nil {
				return nil, fail("当前环境不能向用户提问")
			}
			var in struct {
				Questions json.RawMessage `json:"questions"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			ans, err := env.Agentic.Ask(env.Ctx, in.Questions)
			if err != nil {
				return nil, err
			}
			return &Result{Text: "用户的回答：\n" + ans}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "task",
		Title: "子智能体",
		Description: "启动一个独立上下文的子智能体去完成一项可以单独完成的子任务（例如在大量页面里调研、汇总资料），它完成后只把结论交回来，" +
			"不占用你的上下文。kind=research（默认，只读）或 editor（可以修改页面）。同一轮里调用多次会并行执行。prompt 要写清楚目标、范围和要交回的内容。",
		Schema: obj(map[string]any{
			"description": str("3-8 个字的任务名"),
			"prompt":      str("给子智能体的完整任务说明"),
			"kind":        enum("类型", "research", "editor"),
		}, "description", "prompt"),
		ReadOnly:  true,
		AgentOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Description string `json:"description"`
				Prompt      string `json:"prompt"`
				Kind        string `json:"kind"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if env.Agentic == nil {
				return nil, fail("当前环境不支持子智能体")
			}
			if in.Kind == "editor" && !env.CanWrite {
				return nil, fail("当前是只读模式，不能启动可修改页面的子智能体")
			}
			out, err := env.Agentic.Task(env.Ctx, in.Description, in.Prompt, in.Kind)
			if err != nil {
				return nil, err
			}
			return &Result{Text: out}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "ui_open",
		Title:       "在界面中打开",
		Description: "让用户的浏览器打开某个页面（可定位到某段文字），用于把结果展示给用户。只在确实需要用户看某个页面时使用。",
		Schema: obj(map[string]any{
			"page_id": str("页面 id"),
			"text":    str("要定位并高亮的一段文字（可选）"),
		}, "page_id"),
		ReadOnly:  true,
		AgentOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				PageID string `json:"page_id"`
				Text   string `json:"text"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if _, err := env.NeedPage(in.PageID, model.RoleViewer); err != nil {
				return nil, err
			}
			if env.Client == nil {
				return &Result{Text: "用户当前没有打开浏览器界面，已跳过。"}, nil
			}
			if _, err := env.Client.CallClient(env.Ctx, "ui_open", in); err != nil {
				return &Result{Text: "浏览器没有响应：" + err.Error()}, nil
			}
			return &Result{Text: "已在用户界面打开。"}, nil
		},
	})

	Default.Register(&Tool{
		Name:  "render_check",
		Title: "检查图表渲染",
		Description: "让用户的浏览器实际渲染 Mermaid 图、Graphviz 图或图表 JSON，返回是否有语法错误。写入或修改这类图之后用它自检，有错就改。" +
			"浏览器不在线时会返回「跳过」。",
		Schema: obj(map[string]any{
			"kind": enum("类型", "mermaid", "graphviz", "chart"),
			"code": str("图的源码（chart 为 JSON）"),
		}, "kind", "code"),
		ReadOnly:  true,
		AgentOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Kind string `json:"kind"`
				Code string `json:"code"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if in.Kind == "chart" {
				var v any
				if err := json.Unmarshal([]byte(in.Code), &v); err != nil {
					return &Result{Text: "JSON 无效：" + err.Error()}, nil
				}
			}
			if env.Client == nil {
				return &Result{Text: "跳过：用户的浏览器不在线，无法实际渲染。"}, nil
			}
			raw, err := env.Client.CallClient(env.Ctx, "render_check", in)
			if err != nil {
				return &Result{Text: "跳过：" + err.Error()}, nil
			}
			var r struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(raw, &r)
			if r.OK {
				return &Result{Text: "渲染成功，没有语法错误。"}, nil
			}
			return &Result{Text: "渲染失败：" + strings.TrimSpace(r.Error)}, nil
		},
	})
}
