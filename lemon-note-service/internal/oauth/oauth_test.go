package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

func TestRedirectRules(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://app.example/cb": true, "http://localhost:3333/callback": true, "http://127.0.0.1/cb": true, "cursor://anysphere/oauth": true,
		"http://evil.example/cb": false, "javascript:alert(1)": false, "data:text/html,x": false, "https://a.example/cb#frag": false, "/relative": false,
	} {
		if got := validRedirect(u) == nil; got != ok {
			t.Errorf("%s: %v want %v", u, got, ok)
		}
	}
	reg := []string{"http://localhost:1234/callback", "https://app.example/cb"}
	for u, ok := range map[string]bool{
		"http://localhost:5555/callback": true, "http://localhost:5555/other": false, "https://app.example/cb": true,
		"https://app.example/cb2": false, "http://127.0.0.1:1234/callback": false,
	} {
		if got := redirectMatches(reg, u); got != ok {
			t.Errorf("match %s: %v want %v", u, got, ok)
		}
	}
}

func TestTokenFlow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: filepath.Join(t.TempDir(), "w.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cat, _ := catalog.Load(ctx, st)
	u, err := accounts.New(st, cat).CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "password-123", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	ws := web.NewServer(web.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Boot: &config.Bootstrap{}, Store: st, Cat: cat})
	s := New(st, cat, ws, func(*http.Request) string { return "http://notes.test" })
	r := chi.NewRouter()
	s.Public(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// 注册
	resp, err := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(`{"client_name":"测试客户端","redirect_uris":["http://localhost:9999/callback"]}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("注册失败 %v %v", err, resp.Status)
	}
	var reg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	cid := reg["client_id"].(string)
	if resp, _ := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`)); resp.StatusCode != 400 {
		t.Fatal("非本机 http 回调应拒绝")
	}

	// 模拟用户在确认页同意：直接写授权码
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	ch := base64.RawURLEncoding.EncodeToString(sum[:])
	raw := randToken("")
	d := codeData{RedirectURI: "http://localhost:4444/callback", Challenge: ch, Write: false}
	if err := st.PutLocal(ctx, "oauth_codes", code{ID: hash(raw), ClientID: cid, UserID: u.ID, Data: toJSON(d), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	post := func(form url.Values) (int, map[string]any) {
		resp, err := http.PostForm(srv.URL+"/oauth/token", form)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	// 错的 verifier
	if c, _ := post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {d.RedirectURI}, "code_verifier": {strings.Repeat("x", 50)}}); c != 400 {
		t.Fatal("verifier 不对应拒绝")
	}
	// 授权码一次性：上面失败也已经作废，重新发一个
	raw = randToken("")
	_ = st.PutLocal(ctx, "oauth_codes", code{ID: hash(raw), ClientID: cid, UserID: u.ID, Data: toJSON(d), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()})
	c, tok := post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {d.RedirectURI}, "code_verifier": {verifier}})
	if c != 200 || tok["access_token"] == nil {
		t.Fatalf("换令牌失败 %d %v", c, tok)
	}
	if tok["scope"] != ScopeRead {
		t.Fatalf("只读授权的 scope 不对: %v", tok["scope"])
	}
	a, ok := s.Lookup(ctx, tok["access_token"].(string))
	if !ok || a.User.ID != u.ID || a.CanWrite {
		t.Fatal("访问令牌校验不对")
	}
	// 授权码重放：拒绝，并作废已发出的令牌
	if c, _ := post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {d.RedirectURI}, "code_verifier": {verifier}}); c != 400 {
		t.Fatal("授权码不能用两次")
	}
	if _, ok := s.Lookup(ctx, tok["access_token"].(string)); ok {
		t.Fatal("授权码重放后应作废已发出的令牌")
	}

	// 重新授权，测刷新轮换与重用检测
	raw = randToken("")
	_ = st.PutLocal(ctx, "oauth_codes", code{ID: hash(raw), ClientID: cid, UserID: u.ID, Data: toJSON(codeData{RedirectURI: d.RedirectURI, Challenge: ch, Write: true}), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()})
	_, tok = post(url.Values{"grant_type": {"authorization_code"}, "code": {raw}, "client_id": {cid}, "redirect_uri": {d.RedirectURI}, "code_verifier": {verifier}})
	r1 := tok["refresh_token"].(string)
	c, tok2 := post(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r1}, "client_id": {cid}})
	if c != 200 || tok2["refresh_token"] == r1 {
		t.Fatal("刷新应换新的刷新令牌")
	}
	if a, ok := s.Lookup(ctx, tok2["access_token"].(string)); !ok || !a.CanWrite {
		t.Fatal("新访问令牌应可用且可写")
	}
	if c, _ := post(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r1}, "client_id": {cid}}); c != 400 {
		t.Fatal("旧刷新令牌不能再用")
	}
	if _, ok := s.Lookup(ctx, tok2["access_token"].(string)); ok {
		t.Fatal("旧刷新令牌被重用后整条链应作废")
	}
	// 元数据
	resp, _ = http.Get(srv.URL + "/.well-known/oauth-authorization-server")
	var meta map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	if meta["token_endpoint"] != "http://notes.test/oauth/token" {
		t.Fatalf("元数据不对: %v", meta)
	}
}
