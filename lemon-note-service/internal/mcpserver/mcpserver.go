// Package mcpserver 把工具注册表以 MCP（Streamable HTTP）暴露给外部智能体（Claude Code、Cursor、Codex 等）。
// 认证用个人访问令牌（Authorization: Bearer ln_pat_...）或 OAuth 访问令牌，权限与网页界面完全一致。
package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `Lemon Note 是用户的私有笔记系统（对标 Notion）。用这些工具查找、阅读、整理和修改用户的笔记：
先用 search / list_pages 找到页面，用 read_page 读取（修改前必须先读），用 edit_page 做精确修改、write_page 大段写入、create_pages 新建。
页面正文是增强 Markdown（标准 Markdown 加 <callout>、<details>、<columns>、mermaid 等扩展，read_page 的输出就是这种格式）。
修改会实时出现在用户界面上，修改前的内容自动保存在页面历史里。`

// Auth 解析请求身份（由 web 层实现：个人访问令牌或 OAuth 令牌）。
type Auth func(r *http.Request) (*web.AuthInfo, bool)

// Handler 返回 /mcp 的处理器。
func Handler(svc *tools.Services, auth Auth, resourceMetadataURL func(r *http.Request) string) http.Handler {
	type sessionKey struct{}
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		a, _ := r.Context().Value(sessionKey{}).(*web.AuthInfo)
		if a == nil {
			return nil
		}
		return newServer(svc, a)
	}, &mcp.StreamableHTTPOptions{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := auth(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resourceMetadataURL(r)+`"`)
			web.WriteJSON(w, 401, map[string]any{"error": "unauthorized", "error_description": "需要授权：按客户端提示在浏览器里登录 Lemon Note 并允许访问，或在设置 → 访问令牌里创建令牌"})
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, a)))
	})
}

func newServer(svc *tools.Services, a *web.AuthInfo) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "lemon-note", Title: "Lemon Note", Version: version.Version}, &mcp.ServerOptions{Instructions: instructions, Logger: svc.Log})
	reads := tools.NewReadTracker()
	var mu sync.Mutex
	saved := map[string]bool{}
	user := a.User
	for _, t := range tools.Default.List(svc, false, a.CanWrite) {
		t := t
		ro := t.ReadOnly
		destructive := t.Destructive
		open := t.OpenWorld
		srv.AddTool(&mcp.Tool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: t.Schema,
			Annotations: &mcp.ToolAnnotations{Title: t.Title, ReadOnlyHint: ro, DestructiveHint: &destructive, OpenWorldHint: &open},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// 每次调用都重新确认账号仍然有效（停用、改口令后立刻失效）
			var cur model.User
			if err := svc.Store.Get(ctx, &cur, "SELECT * FROM users WHERE id = ?", user.ID); err != nil || cur.Status != model.UserActive {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "账号已停用"}}}, nil
			}
			env := &tools.Env{Ctx: ctx, User: &cur, CanWrite: a.CanWrite, Spaces: a.Spaces, Svc: svc, Reads: reads,
				BeforeWrite: func(pageID string) error {
					mu.Lock()
					defer mu.Unlock()
					if saved[pageID] {
						return nil
					}
					if _, err := svc.Pages.SaveVersion(ctx, cur.ID, pageID, "mcp", map[string]any{"client": "mcp"}); err != nil {
						return err
					}
					saved[pageID] = true
					return nil
				}}
			var args json.RawMessage
			if req.Params != nil {
				args = req.Params.Arguments
			}
			res := tools.Default.Call(env, t.Name, args)
			content := []mcp.Content{&mcp.TextContent{Text: res.Text}}
			for _, img := range res.Images {
				content = append(content, &mcp.ImageContent{MIMEType: img.MediaType, Data: []byte(img.Data)})
			}
			return &mcp.CallToolResult{Content: content, IsError: res.IsError}, nil
		})
	}
	return srv
}

// BearerToken 取请求里的 Bearer 令牌。
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}
