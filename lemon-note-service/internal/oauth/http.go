package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// oauthErr 按 RFC 6749 的格式回错误。
func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	web.WriteJSON(w, status, map[string]any{"error": code, "error_description": desc})
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 元数据、注册、换令牌不依赖 Cookie，允许任意来源（浏览器里的 MCP 客户端也能用）
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next(w, r)
	}
}

// Public 挂载不需要登录的端点（顶层路由）。
func (s *Service) Public(r chi.Router) {
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		r.Get(p, cors(s.resourceMeta))
		r.Options(p, cors(s.resourceMeta))
	}
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp"} {
		r.Get(p, cors(s.serverMeta))
		r.Options(p, cors(s.serverMeta))
	}
	r.Post("/oauth/register", cors(s.register))
	r.Options("/oauth/register", cors(s.register))
	r.Post("/oauth/token", cors(s.tokenEndpoint))
	r.Options("/oauth/token", cors(s.tokenEndpoint))
	r.Post("/oauth/revoke", cors(s.revoke))
	r.Options("/oauth/revoke", cors(s.revoke))
	// 授权页是前端页面（要登录、要用户确认），这里只输出前端外壳
	r.Get("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		web.ServeStatic(w, r)
	})
}

func (s *Service) resourceMeta(w http.ResponseWriter, r *http.Request) {
	base := s.base(r)
	web.WriteJSON(w, 200, map[string]any{
		"resource": base + "/mcp", "authorization_servers": []string{base}, "bearer_methods_supported": []string{"header"},
		"scopes_supported": []string{ScopeRead, ScopeWrite}, "resource_name": "Lemon Note",
	})
}

func (s *Service) serverMeta(w http.ResponseWriter, r *http.Request) {
	base := s.base(r)
	web.WriteJSON(w, 200, map[string]any{
		"issuer": base, "authorization_endpoint": base + "/oauth/authorize", "token_endpoint": base + "/oauth/token",
		"registration_endpoint": base + "/oauth/register", "revocation_endpoint": base + "/oauth/revoke",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
		"revocation_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{ScopeRead, ScopeWrite},
	})
}

// register 动态注册公开客户端（RFC 7591）。
func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	if !s.ws.Allow("oauthreg:"+web.ClientIP(r), 10.0/3600, 10) {
		oauthErr(w, 429, "slow_down", "注册过于频繁")
		return
	}
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		ClientURI    string   `json:"client_uri"`
		Software     string   `json:"software_id"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		GrantTypes   []string `json:"grant_types"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		oauthErr(w, 400, "invalid_client_metadata", "请求体不是合法的 JSON")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthErr(w, 400, "invalid_redirect_uri", "需要 1 到 10 个 redirect_uris")
		return
	}
	for _, u := range in.RedirectURIs {
		if err := validRedirect(u); err != nil {
			oauthErr(w, 400, "invalid_redirect_uri", err.Error()+"："+u)
			return
		}
	}
	if in.AuthMethod != "" && in.AuthMethod != "none" {
		oauthErr(w, 400, "invalid_client_metadata", "只支持公开客户端（token_endpoint_auth_method=none）")
		return
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "MCP 客户端"
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	now := time.Now()
	c := client{ID: "lnc_" + ids.New(), Name: name, RedirectURIs: toJSON(in.RedirectURIs),
		Data: toJSON(map[string]any{"client_uri": in.ClientURI, "software_id": in.Software, "ip": web.ClientIP(r)}), CreatedAt: now.UnixMilli()}
	if err := s.st.PutLocal(r.Context(), "oauth_clients", c); err != nil {
		oauthErr(w, 500, "server_error", "保存失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	web.WriteJSON(w, 201, map[string]any{
		"client_id": c.ID, "client_id_issued_at": now.Unix(), "client_name": name, "redirect_uris": in.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}

// ---------- 授权（前端确认页调用，需要登录） ----------

type authzReq struct {
	ResponseType  string `json:"response_type"`
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	State         string `json:"state"`
	Scope         string `json:"scope"`
	Challenge     string `json:"code_challenge"`
	ChallengeMeth string `json:"code_challenge_method"`
	Resource      string `json:"resource"`
}

// check 校验授权请求。返回的 redirectOK 表示回调地址已确认可信，出错时可以带 error 跳回去。
func (s *Service) check(r *http.Request, q authzReq) (c *client, redirectOK bool, errCode, errDesc string) {
	c, err := s.client(r.Context(), q.ClientID)
	if err != nil {
		return nil, false, "invalid_client", err.Error()
	}
	if q.RedirectURI == "" || !redirectMatches(c.uris(), q.RedirectURI) {
		return c, false, "invalid_request", "回调地址与注册的不一致"
	}
	if q.ResponseType != "code" {
		return c, true, "unsupported_response_type", "只支持 response_type=code"
	}
	if q.ChallengeMeth != "S256" || len(q.Challenge) < 43 || len(q.Challenge) > 128 {
		return c, true, "invalid_request", "必须使用 PKCE（code_challenge_method=S256）"
	}
	if q.Resource != "" && !strings.HasPrefix(strings.TrimRight(q.Resource, "/"), s.base(r)) {
		return c, true, "invalid_target", "resource 不是本服务"
	}
	return c, true, "", ""
}

func wantsWrite(scope string) bool {
	if strings.TrimSpace(scope) == "" {
		return true // 没写范围按读写处理，确认页上用户可以去掉写权限
	}
	return slices.Contains(strings.Fields(scope), ScopeWrite)
}

func withQuery(raw string, kv map[string]string) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	for k, v := range kv {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Mount 挂载需要登录的接口（授权确认页、已授权应用管理）。
func (s *Service) Mount(ws *web.Server, r chi.Router) {
	h := ws.H
	r.Post("/api/oauth/request", h(func(w http.ResponseWriter, r *http.Request) error {
		if web.AuthOf(r).Session == nil {
			return web.Forbidden()
		}
		var q authzReq
		if err := web.ReadJSONLimit(r, &q, 16<<10); err != nil {
			return err
		}
		c, ok, ecode, desc := s.check(r, q)
		out := map[string]any{"ok": ecode == "", "error": ecode, "error_description": desc}
		if c != nil {
			out["client_name"] = c.Name
		}
		if ok {
			u, _ := url.Parse(q.RedirectURI)
			out["redirect_host"] = u.Scheme + "://" + u.Host
		}
		out["write"] = wantsWrite(q.Scope)
		return web.OK(w, out)
	}))
	r.Post("/api/oauth/decide", h(func(w http.ResponseWriter, r *http.Request) error {
		a := web.AuthOf(r)
		if a.Session == nil { // 只能在网页里由本人确认，令牌不能给自己授权
			return web.Forbidden()
		}
		var in struct {
			authzReq
			Approve bool     `json:"approve"`
			Write   bool     `json:"write"`
			Spaces  []string `json:"spaces"`
		}
		if err := web.ReadJSONLimit(r, &in, 16<<10); err != nil {
			return err
		}
		_, ok, ecode, desc := s.check(r, in.authzReq)
		if !ok {
			return web.Bad(desc)
		}
		if ecode != "" {
			return web.OK(w, map[string]any{"redirect": withQuery(in.RedirectURI, map[string]string{"error": ecode, "error_description": desc, "state": in.State})})
		}
		if !in.Approve {
			return web.OK(w, map[string]any{"redirect": withQuery(in.RedirectURI, map[string]string{"error": "access_denied", "state": in.State})})
		}
		spaces := []string{}
		for _, sp := range in.Spaces {
			if s.cat.SpaceRole(a.User.ID, sp) != model.RoleNone {
				spaces = append(spaces, sp)
			}
		}
		raw := randToken("")
		cid := ids.New()
		d := codeData{RedirectURI: in.RedirectURI, Challenge: in.Challenge, Write: in.Write && wantsWrite(in.Scope), Spaces: spaces, Resource: in.Resource, CodeID: cid}
		if err := s.st.PutLocal(r.Context(), "oauth_codes", code{ID: hash(raw), ClientID: in.ClientID, UserID: a.User.ID, Data: toJSON(d),
			ExpiresAt: time.Now().Add(codeTTL).UnixMilli()}); err != nil {
			return err
		}
		ws.Audit(r, a.User.ID, "oauth_authorized", in.ClientID, map[string]any{"write": d.Write, "spaces": len(spaces)})
		return web.OK(w, map[string]any{"redirect": withQuery(in.RedirectURI, map[string]string{"code": raw, "state": in.State, "iss": s.base(r)})})
	}))
	r.Get("/api/me/oauth-grants", h(func(w http.ResponseWriter, r *http.Request) error {
		uid := web.UserOf(r).ID
		var ts []token
		if err := s.st.Select(r.Context(), &ts, "SELECT * FROM oauth_tokens WHERE user_id = ? AND kind = 'refresh' AND revoked_at IS NULL AND expires_at > ?", uid, model.Now()); err != nil {
			return err
		}
		type grant struct {
			ClientID string `json:"client_id"`
			Name     string `json:"name"`
			Write    bool   `json:"write"`
			Spaces   int    `json:"spaces"`
			LastUsed int64  `json:"last_used"`
		}
		by := map[string]*grant{}
		out := []*grant{}
		for _, t := range ts {
			g := by[t.ClientID]
			if g == nil {
				name := t.ClientID
				if c, err := s.client(r.Context(), t.ClientID); err == nil {
					name = c.Name
				}
				d := t.data()
				g = &grant{ClientID: t.ClientID, Name: name, Write: d.Write, Spaces: len(d.Spaces)}
				by[t.ClientID] = g
				out = append(out, g)
			}
			if t.CreatedAt > g.LastUsed {
				g.LastUsed = t.CreatedAt
			}
		}
		return web.OK(w, map[string]any{"grants": out})
	}))
	r.Delete("/api/me/oauth-grants/{client}", h(func(w http.ResponseWriter, r *http.Request) error {
		uid := web.UserOf(r).ID
		cid := chi.URLParam(r, "client")
		if _, err := s.st.ExecLocal(r.Context(), "UPDATE oauth_tokens SET revoked_at = ? WHERE user_id = ? AND client_id = ? AND revoked_at IS NULL", model.Now(), uid, cid); err != nil {
			return err
		}
		_, _ = s.st.ExecLocal(r.Context(), "DELETE FROM oauth_codes WHERE user_id = ? AND client_id = ?", uid, cid)
		ws.Audit(r, uid, "oauth_revoked", cid, nil)
		return web.OK(w, nil)
	}))
}

// ---------- 换令牌 ----------

func (s *Service) tokenEndpoint(w http.ResponseWriter, r *http.Request) {
	if !s.ws.Allow("oauthtok:"+web.ClientIP(r), 2, 30) {
		oauthErr(w, 429, "slow_down", "请求过于频繁")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthErr(w, 400, "invalid_request", "请求格式不对")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refresh(w, r)
	default:
		oauthErr(w, 400, "unsupported_grant_type", "只支持 authorization_code 与 refresh_token")
	}
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func (s *Service) exchangeCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := r.PostForm
	raw, clientID := f.Get("code"), f.Get("client_id")
	var c code
	if err := s.st.Get(ctx, &c, "SELECT * FROM oauth_codes WHERE id = ?", hash(raw)); err != nil {
		// 授权码可能已经用过：按 RFC 6749 §4.1.2，把用它换出的令牌全部作废
		s.revokeByCode(r, hash(raw))
		oauthErr(w, 400, "invalid_grant", "授权码无效或已使用")
		return
	}
	// 一次性：先删再用
	res, err := s.st.ExecLocal(ctx, "DELETE FROM oauth_codes WHERE id = ?", c.ID)
	if n, _ := res.RowsAffected(); err != nil || n == 0 {
		oauthErr(w, 400, "invalid_grant", "授权码无效或已使用")
		return
	}
	var d codeData
	_ = json.Unmarshal([]byte(c.Data), &d)
	if c.ExpiresAt < model.Now() || c.ClientID != clientID || d.RedirectURI != f.Get("redirect_uri") || !pkceOK(f.Get("code_verifier"), d.Challenge) {
		oauthErr(w, 400, "invalid_grant", "授权码、回调地址或 code_verifier 不匹配")
		return
	}
	s.issue(w, r, c.ClientID, c.UserID, tokenData{Write: d.Write, Spaces: d.Spaces, Family: ids.New(), CodeID: c.ID})
}

func (s *Service) revokeByCode(r *http.Request, codeHash string) {
	var ts []token
	if s.st.Select(r.Context(), &ts, "SELECT * FROM oauth_tokens WHERE revoked_at IS NULL") != nil {
		return
	}
	for _, t := range ts {
		if t.data().CodeID == codeHash {
			s.revokeFamily(r, t.data().Family)
			return
		}
	}
}

func (s *Service) revokeFamily(r *http.Request, family string) {
	var ts []token
	if s.st.Select(r.Context(), &ts, "SELECT * FROM oauth_tokens WHERE revoked_at IS NULL") != nil {
		return
	}
	now := model.Now()
	for _, t := range ts {
		if t.data().Family == family {
			_, _ = s.st.ExecLocal(r.Context(), "UPDATE oauth_tokens SET revoked_at = ? WHERE id = ?", now, t.ID)
		}
	}
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := r.PostForm.Get("refresh_token")
	var t token
	if !strings.HasPrefix(raw, RefreshPrefix) || s.st.Get(ctx, &t, "SELECT * FROM oauth_tokens WHERE id = ? AND kind = 'refresh'", hash(raw)) != nil {
		oauthErr(w, 400, "invalid_grant", "刷新令牌无效")
		return
	}
	d := t.data()
	if t.RevokedAt != nil {
		s.revokeFamily(r, d.Family) // 用过的刷新令牌又被拿来用：可能泄露，整条链作废
		oauthErr(w, 400, "invalid_grant", "刷新令牌已失效")
		return
	}
	if t.ExpiresAt < model.Now() || t.ClientID != r.PostForm.Get("client_id") {
		oauthErr(w, 400, "invalid_grant", "刷新令牌无效")
		return
	}
	var u model.User
	if s.st.Get(ctx, &u, "SELECT * FROM users WHERE id = ?", t.UserID) != nil || u.Status != model.UserActive {
		oauthErr(w, 400, "invalid_grant", "账号已停用")
		return
	}
	res, err := s.st.ExecLocal(ctx, "UPDATE oauth_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", model.Now(), t.ID)
	if n, _ := res.RowsAffected(); err != nil || n == 0 {
		oauthErr(w, 400, "invalid_grant", "刷新令牌已失效")
		return
	}
	s.issue(w, r, t.ClientID, t.UserID, d)
}

func (s *Service) issue(w http.ResponseWriter, r *http.Request, clientID, userID string, d tokenData) {
	ctx := r.Context()
	now := time.Now()
	access, refresh := randToken(AccessPrefix), randToken(RefreshPrefix)
	err := s.putTokens(ctx, []token{
		{ID: hash(access), Kind: "access", ClientID: clientID, UserID: userID, Data: toJSON(d), ExpiresAt: now.Add(accessTTL).UnixMilli(), CreatedAt: now.UnixMilli()},
		{ID: hash(refresh), Kind: "refresh", ClientID: clientID, UserID: userID, Data: toJSON(d), ExpiresAt: now.Add(refreshTTL).UnixMilli(), CreatedAt: now.UnixMilli()},
	})
	if err != nil {
		oauthErr(w, 500, "server_error", "签发令牌失败")
		return
	}
	scope := ScopeRead
	if d.Write {
		scope += " " + ScopeWrite
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	web.WriteJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
		"refresh_token": refresh, "scope": scope})
}

func (s *Service) putTokens(ctx context.Context, ts []token) error {
	for _, t := range ts {
		if err := s.st.PutLocal(ctx, "oauth_tokens", t); err != nil {
			return err
		}
	}
	return nil
}

// revoke 撤销令牌（RFC 7009）：不管令牌存不存在都回 200。
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	_ = r.ParseForm()
	raw := r.PostForm.Get("token")
	var t token
	if s.st.Get(r.Context(), &t, "SELECT * FROM oauth_tokens WHERE id = ?", hash(raw)) == nil {
		if t.Kind == "refresh" {
			s.revokeFamily(r, t.data().Family)
		} else {
			_, _ = s.st.ExecLocal(r.Context(), "UPDATE oauth_tokens SET revoked_at = ? WHERE id = ?", model.Now(), t.ID)
		}
	}
	w.WriteHeader(200)
}
