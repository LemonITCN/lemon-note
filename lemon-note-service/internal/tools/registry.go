// Package tools 是工具注册表：同一份实现同时给内置智能体与 MCP 使用，权限检查与界面完全一致。
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/search"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Services 是工具用到的服务。
type Services struct {
	Store    *store.Store
	Cat      *catalog.Catalog
	Pages    *pages.Service
	Search   *search.Index
	Files    *files.Service
	Settings *settings.Service
	Box      *crypt.Box
	Log      *slog.Logger
	// DB 数据库模块（可选，注册后数据库工具才可用）
	DB any
	// Comments 评论模块（可选）
	Comments any
}

// ReadTracker 记录本会话读过哪些页面、读时的版本号（先读后改检查用）。
type ReadTracker struct {
	mu   sync.Mutex
	revs map[string]int64
}

func NewReadTracker() *ReadTracker { return &ReadTracker{revs: map[string]int64{}} }

func (r *ReadTracker) Mark(pageID string, rev int64) {
	r.mu.Lock()
	r.revs[pageID] = rev
	r.mu.Unlock()
}

func (r *ReadTracker) Get(pageID string) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.revs[pageID]
	return v, ok
}

// ClientBridge 让工具在用户的浏览器里执行（智能体专用）。
type ClientBridge interface {
	CallClient(ctx context.Context, tool string, args any) (json.RawMessage, error)
}

// Env 是一次工具调用的上下文。
type Env struct {
	Ctx      context.Context
	User     *model.User
	CanWrite bool
	Spaces   []string // 令牌限定的空间（空表示不限）
	Svc      *Services
	Reads    *ReadTracker
	Agent    bool   // 内置智能体调用
	Origin   string // 实时广播去回声
	Client   ClientBridge
	// BeforeWrite 写某个页面之前调用（检查点）；Created 记录新建的页面（撤销用）
	BeforeWrite func(pageID string) error
	Created     func(pageID string)
	// Ask / Todo / Task 由智能体实现
	Agentic AgentHooks
}

// AgentHooks 智能体专用工具的实现。
type AgentHooks interface {
	Ask(ctx context.Context, questions json.RawMessage) (string, error)
	Todo(items json.RawMessage) error
	Task(ctx context.Context, description, prompt, kind string) (string, error)
}

// Result 是工具结果。
type Result struct {
	Text    string         `json:"text"`
	Images  []llm.Part     `json:"-"`
	Data    map[string]any `json:"data,omitempty"`
	IsError bool           `json:"is_error,omitempty"`
}

// Tool 是一个工具。
type Tool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	ReadOnly    bool
	Destructive bool
	AgentOnly   bool
	OpenWorld   bool // 访问外部网络
	Handler     func(env *Env, args json.RawMessage) (*Result, error)
	// Available 可选：返回 false 时不出现在工具列表（例如未配置搜索服务）
	Available func(svc *Services) bool
}

// Registry 是工具注册表。
type Registry struct {
	tools map[string]*Tool
	order []string
}

var Default = &Registry{tools: map[string]*Tool{}}

// Register 注册工具。
func (r *Registry) Register(t *Tool) {
	if _, dup := r.tools[t.Name]; dup {
		panic("工具重名：" + t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
}

// Get 取工具。
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List 列出可用工具。agent 为假时排除智能体专用工具；write 为假时排除写工具。
func (r *Registry) List(svc *Services, agent, write bool) []*Tool {
	var out []*Tool
	for _, n := range r.order {
		t := r.tools[n]
		if t.AgentOnly && !agent {
			continue
		}
		if !write && !t.ReadOnly {
			continue
		}
		if t.Available != nil && !t.Available(svc) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Defs 返回给模型的工具定义。
func Defs(ts []*Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(ts))
	for _, t := range ts {
		b, _ := json.Marshal(t.Schema)
		out = append(out, llm.ToolDef{Name: t.Name, Description: t.Description, Schema: b})
	}
	return out
}

// ToolError 是给模型看的工具错误（不是系统错误）。
type ToolError struct{ Msg string }

func (e *ToolError) Error() string { return e.Msg }

func fail(format string, a ...any) error { return &ToolError{Msg: fmt.Sprintf(format, a...)} }

// Call 执行工具，所有错误都转成给模型看的结果。
func (r *Registry) Call(env *Env, name string, args json.RawMessage) (res *Result) {
	t, ok := r.tools[name]
	if !ok {
		return &Result{Text: "错误：没有名为 " + name + " 的工具", IsError: true}
	}
	defer func() {
		if v := recover(); v != nil {
			env.Svc.Log.Error("工具执行崩溃", "tool", name, "panic", v)
			res = &Result{Text: "错误：工具执行时发生内部错误", IsError: true}
		}
	}()
	if !t.ReadOnly && !env.CanWrite {
		return &Result{Text: "错误：当前只有读取权限，不能执行修改类工具", IsError: true}
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	out, err := t.Handler(env, args)
	if err != nil {
		var te *ToolError
		if errors.As(err, &te) || isUserFacing(err) {
			return &Result{Text: "错误：" + err.Error(), IsError: true}
		}
		env.Svc.Log.Warn("工具执行失败", "tool", name, "err", err)
		return &Result{Text: "错误：执行失败（" + truncate(err.Error(), 200) + "）", IsError: true}
	}
	if out == nil {
		out = &Result{Text: "完成"}
	}
	return out
}

func isUserFacing(err error) bool {
	for _, r := range err.Error() {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// bind 解析参数。
func bind(args json.RawMessage, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	if err := dec.Decode(dst); err != nil {
		return fail("参数格式不正确：%v", err)
	}
	return nil
}

// ---------- 权限 ----------

func (env *Env) spaceAllowed(spaceID string) bool {
	if len(env.Spaces) == 0 {
		return true
	}
	for _, s := range env.Spaces {
		if s == spaceID {
			return true
		}
	}
	return false
}

// PageRole 返回对页面的角色。
func (env *Env) PageRole(pageID string) string {
	p, ok := env.Svc.Cat.Page(pageID)
	if !ok || !env.spaceAllowed(p.SpaceID) {
		return model.RoleNone
	}
	r := env.Svc.Cat.PageRole(env.User.ID, pageID)
	if !env.CanWrite && model.RoleAtLeast(r, model.RoleCommenter) {
		r = model.RoleViewer
	}
	return r
}

// NeedPage 要求页面权限。
func (env *Env) NeedPage(pageID, need string) (catalog.PageMeta, error) {
	pageID = strings.TrimSpace(pageID)
	p, ok := env.Svc.Cat.Page(pageID)
	if !ok || env.PageRole(pageID) == model.RoleNone {
		return p, fail("页面 %s 不存在或没有权限", pageID)
	}
	if !model.RoleAtLeast(env.PageRole(pageID), need) {
		return p, fail("没有修改页面「%s」的权限", p.Title)
	}
	return p, nil
}

// SpaceRole 返回空间角色。
func (env *Env) SpaceRole(spaceID string) string {
	if !env.spaceAllowed(spaceID) {
		return model.RoleNone
	}
	r := env.Svc.Cat.SpaceRole(env.User.ID, spaceID)
	if !env.CanWrite && model.RoleAtLeast(r, model.RoleCommenter) {
		r = model.RoleViewer
	}
	return r
}

// Spaces 返回可访问的空间。
func (env *Env) VisibleSpaces() []model.Space {
	var out []model.Space
	for _, s := range env.Svc.Cat.VisibleSpaces(env.User.ID) {
		if env.spaceAllowed(s.ID) {
			out = append(out, s)
		}
	}
	return out
}

// DefaultSpace 返回默认空间（个人空间）。
func (env *Env) DefaultSpace() string {
	sp := env.VisibleSpaces()
	if len(sp) == 0 {
		return ""
	}
	return sp[0].ID
}

// BeforeWriteSafe 写页面之前的检查点（给其他包里的工具用）。
func (env *Env) BeforeWriteSafe(pageID string) error { return env.beforeWrite(pageID) }

func (env *Env) beforeWrite(pageID string) error {
	if env.BeforeWrite != nil {
		return env.BeforeWrite(pageID)
	}
	return nil
}

// PathOf 返回页面路径（「空间 / 上级 / 页面」）。
func (env *Env) PathOf(pageID string) string {
	var parts []string
	for _, a := range env.Svc.Cat.Ancestors(pageID) {
		if env.PageRole(a.ID) != model.RoleNone {
			parts = append(parts, titleOr(a.Title))
		}
	}
	if p, ok := env.Svc.Cat.Page(pageID); ok {
		if sp, ok := env.Svc.Cat.Space(p.SpaceID); ok {
			parts = append([]string{sp.Name}, parts...)
		}
	}
	return strings.Join(parts, " / ")
}

func titleOr(t string) string {
	if strings.TrimSpace(t) == "" {
		return "无标题"
	}
	return t
}

// schema 构造辅助
func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Strings(required)
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func arr(items map[string]any, desc string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": desc}
}
func enum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": desc}
}
