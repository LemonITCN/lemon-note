package share

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

const unlockTTL = 12 * time.Hour

type ctxKey struct{}

type pubCtx struct {
	share  model.PageShare
	token  string
	scope  *scope
	locked bool // 设了口令且还没解锁
}

func pubOf(r *http.Request) *pubCtx { return r.Context().Value(ctxKey{}).(*pubCtx) }

var errNoShare = &web.APIError{Status: 404, Code: "not_found", Message: "链接不存在或已失效"}

// Public 挂载匿名可访问的公开分享接口与 /share 页面外壳。
func (s *Service) Public(ws *web.Server) func(r chi.Router) {
	return func(r chi.Router) {
		h := ws.H
		r.Get("/share/{token}", s.serveShell)
		r.Get("/share/{token}/*", s.serveShell)
		r.Route("/api/public/{token}", func(r chi.Router) {
			r.Use(s.resolve(ws))
			r.Get("/", h(s.hInfo))
			r.Post("/unlock", h(func(w http.ResponseWriter, r *http.Request) error { return s.hUnlock(ws, w, r) }))
			r.Group(func(r chi.Router) {
				r.Use(s.requireUnlocked(ws))
				r.Get("/pages/{id}", h(s.hPage))
				r.Get("/databases/{id}", h(s.hDatabase))
				r.Post("/databases/{id}/query", h(s.hQuery))
				r.Post("/charts/query", h(s.hChart))
				r.Get("/files/{fid}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hFile(ws, w, r) }))
			})
		})
	}
}

// serveShell 输出前端页面外壳；默认禁止收录、不带来源地址（令牌在路径里）。
func (s *Service) serveShell(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	if sh, ok := s.lookup(r.Context(), chi.URLParam(r, "token")); ok && sh.AllowIndex && sh.PasswordHash == "" {
		h.Del("X-Robots-Tag")
	}
	web.ServeStatic(w, r)
}

func (s *Service) lookup(ctx context.Context, token string) (model.PageShare, bool) {
	if len(token) < 16 || len(token) > 64 || !s.publicEnabled(ctx) {
		return model.PageShare{}, false
	}
	sh, ok := s.cat.FindPublicShare(token)
	if !ok || !s.cat.PublicCanView(sh, sh.PageID) {
		return model.PageShare{}, false
	}
	return sh, true
}

func (s *Service) resolve(ws *web.Server) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
			w.Header().Set("Cache-Control", "no-store")
			if !ws.Allow("pub:"+web.ClientIP(r), 30, 300) {
				web.WriteJSON(w, 429, map[string]any{"error": map[string]any{"code": "too_many_attempts", "message": "请求过于频繁"}})
				return
			}
			tok := chi.URLParam(r, "token")
			sh, ok := s.lookup(r.Context(), tok)
			if !ok {
				web.WriteJSON(w, 404, map[string]any{"error": errNoShare})
				return
			}
			pc := &pubCtx{share: sh, token: tok, scope: s.newScope(sh)}
			pc.locked = sh.PasswordHash != "" && !s.unlocked(r, sh)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, pc)))
		})
	}
}

func (s *Service) requireUnlocked(ws *web.Server) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if pubOf(r).locked {
				web.WriteJSON(w, 401, map[string]any{"error": map[string]any{"code": "share_password", "message": "需要输入访问口令"}})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- 口令解锁 ----------

type unlockClaim struct {
	ShareID string `json:"s"`
	PW      string `json:"p"` // 口令哈希的尾部：改口令后旧凭据作废
	Exp     int64  `json:"e"`
}

func cookieName(sh model.PageShare) string { return "ln_pub_" + sh.ID }

func pwTag(hash string) string {
	if len(hash) > 16 {
		return hash[len(hash)-16:]
	}
	return hash
}

func (s *Service) unlocked(r *http.Request, sh model.PageShare) bool {
	c, err := r.Cookie(cookieName(sh))
	if err != nil {
		return false
	}
	plain, err := s.box.Open(c.Value)
	if err != nil {
		return false
	}
	var cl unlockClaim
	if json.Unmarshal([]byte(plain), &cl) != nil {
		return false
	}
	return cl.ShareID == sh.ID && cl.PW == pwTag(sh.PasswordHash) && cl.Exp > time.Now().Unix()
}

func (s *Service) hUnlock(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	pc := pubOf(r)
	if pc.share.PasswordHash == "" {
		return web.OK(w, nil)
	}
	if !ws.Allow("pubpw:"+web.ClientIP(r)+":"+pc.share.ID, 0.1, 8) {
		return &web.APIError{Status: 429, Code: "too_many_attempts", Message: "尝试次数过多，请稍后再试"}
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := web.ReadJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if !auth.VerifyPassword(in.Password, pc.share.PasswordHash) {
		return &web.APIError{Status: 403, Code: "bad_share_password", Message: "口令不正确"}
	}
	exp := time.Now().Add(unlockTTL)
	b, _ := json.Marshal(unlockClaim{ShareID: pc.share.ID, PW: pwTag(pc.share.PasswordHash), Exp: exp.Unix()})
	http.SetCookie(w, &http.Cookie{Name: cookieName(pc.share), Value: s.box.Seal(string(b)), Path: "/api/public/" + pc.token,
		Expires: exp, HttpOnly: true, Secure: ws.IsHTTPS(r), SameSite: http.SameSiteLaxMode})
	return web.OK(w, nil)
}

// ---------- 读取 ----------

type treeItem struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Title    string `json:"title"`
	Icon     string `json:"icon,omitempty"`
	Kind     string `json:"kind"`
	Position string `json:"position"`
}

func (s *Service) hInfo(w http.ResponseWriter, r *http.Request) error {
	pc := pubOf(r)
	sys := s.set.System(r.Context())
	out := map[string]any{"site_name": sys.SiteName, "page_id": pc.share.PageID, "needs_password": pc.locked,
		"include_children": pc.share.IncludeChildren, "allow_index": pc.share.AllowIndex}
	if pc.locked {
		return web.OK(w, out)
	}
	root, _ := s.cat.Page(pc.share.PageID)
	tree := []treeItem{{ID: root.ID, Title: root.Title, Icon: root.Icon, Kind: root.Kind, Position: root.Position}}
	if pc.share.IncludeChildren {
		for _, id := range s.cat.Descendants(root.ID) {
			p, ok := s.cat.Page(id)
			if !ok || p.TrashedAt != 0 || p.DatabaseID != "" || p.IsTemplate || id == root.ID {
				continue
			}
			tree = append(tree, treeItem{ID: p.ID, ParentID: p.ParentID, Title: p.Title, Icon: p.Icon, Kind: p.Kind, Position: p.Position})
		}
	}
	out["tree"] = tree
	return web.OK(w, out)
}

type pubPage struct {
	ID         string          `json:"id"`
	SpaceID    string          `json:"space_id"`
	ParentID   *string         `json:"parent_id"`
	DatabaseID *string         `json:"database_id"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Icon       string          `json:"icon"`
	Cover      string          `json:"cover"`
	CreatedAt  int64           `json:"created_at"`
	UpdatedAt  int64           `json:"updated_at"`
	Flags      model.PageFlags `json:"-"`
}

func (s *Service) hPage(w http.ResponseWriter, r *http.Request) error {
	pc := pubOf(r)
	id := chi.URLParam(r, "id")
	if !pc.scope.has(id) {
		return errNoShare
	}
	doc, err := s.pages.Doc(r.Context(), id)
	if err != nil || doc.Page.IsTemplate {
		return errNoShare
	}
	p := doc.Page
	pg := pubPage{ID: p.ID, SpaceID: "", Kind: p.Kind, Title: p.Title, Icon: p.Icon, Cover: p.Cover, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if p.ParentID != nil && pc.scope.has(*p.ParentID) {
		pg.ParentID = p.ParentID
	}
	if p.DatabaseID != nil && pc.scope.has(*p.DatabaseID) {
		pg.DatabaseID = p.DatabaseID
	}
	blocks := make([]map[string]any, 0, len(doc.Blocks))
	for _, b := range doc.Blocks {
		blocks = append(blocks, map[string]any{"id": b.ID, "type": b.Type, "position": b.Position, "version": b.Version,
			"data": pc.scope.sanitizeBlock(b.Data, pc.token)})
	}
	var crumbs []treeItem
	for _, a := range s.cat.Ancestors(id) {
		if pc.scope.has(a.ID) {
			crumbs = append(crumbs, treeItem{ID: a.ID, Title: a.Title, Icon: a.Icon, Kind: a.Kind})
		} else {
			crumbs = crumbs[:0]
		}
	}
	children := []treeItem{}
	for _, c := range s.cat.Children(p.SpaceID, id) {
		if c.TrashedAt == 0 && c.DatabaseID == "" && !c.IsTemplate && pc.scope.has(c.ID) {
			children = append(children, treeItem{ID: c.ID, ParentID: id, Title: c.Title, Icon: c.Icon, Kind: c.Kind, Position: c.Position})
		}
	}
	flags := doc.Flags
	flags.Locked = true
	return web.OK(w, map[string]any{"page": pg, "flags": flags, "blocks": blocks, "role": model.RoleViewer,
		"breadcrumbs": crumbs, "children": children, "users": s.usersIn(p.CreatedBy, p.UpdatedBy)})
}

func (s *Service) usersIn(ids ...string) map[string]catalogUser {
	out := map[string]catalogUser{}
	for _, id := range ids {
		if id == "" || out[id].ID != "" {
			continue
		}
		if u, ok := s.cat.User(id); ok {
			out[id] = catalogUser{ID: id, DisplayName: u.DisplayName, Avatar: u.Avatar}
		}
	}
	return out
}

// ---------- 数据库 ----------

// hiddenProps 返回公开访问时要整列去掉的属性：关联到范围外数据库的关联、
// 通过它们的汇总、以及引用了这些属性的公式（公式可能间接读出对方数据）。
func hiddenProps(sc *dbs.Schema, has func(string) bool) map[string]bool {
	hide := map[string]bool{}
	for _, p := range sc.Properties {
		if p.Type == dbs.TRelation && !has(p.DatabaseID) {
			hide[p.ID] = true
		}
	}
	for _, p := range sc.Properties {
		if p.Type == dbs.TRollup && (hide[p.Relation] || sc.Prop(p.Relation) == nil) {
			hide[p.ID] = true
		}
	}
	// 公式可能层层引用：反复传播直到稳定
	for changed := true; changed; {
		changed = false
		for _, p := range sc.Properties {
			if p.Type != dbs.TFormula || hide[p.ID] {
				continue
			}
			for _, q := range sc.Properties {
				if hide[q.ID] && (strings.Contains(p.Expression, q.Name) || strings.Contains(p.Expression, q.ID)) {
					hide[p.ID] = true
					changed = true
					break
				}
			}
		}
	}
	return hide
}

func (s *Service) dbScope(r *http.Request, id string) (*dbs.Info, map[string]bool, error) {
	pc := pubOf(r)
	if !pc.scope.has(id) {
		return nil, nil, errNoShare
	}
	info, err := s.dbs.Info(r.Context(), id)
	if err != nil {
		return nil, nil, errNoShare
	}
	hide := hiddenProps(info.Schema, pc.scope.has)
	if len(hide) > 0 {
		kept := info.Schema.Properties[:0:0]
		for _, p := range info.Schema.Properties {
			if !hide[p.ID] {
				kept = append(kept, p)
			}
		}
		info.Schema = &dbs.Schema{Properties: kept}
	}
	return info, hide, nil
}

func (s *Service) hDatabase(w http.ResponseWriter, r *http.Request) error {
	info, _, err := s.dbScope(r, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	p := info.Page
	return web.OK(w, map[string]any{"page": pubPage{ID: p.ID, Kind: p.Kind, Title: p.Title, Icon: p.Icon, Cover: p.Cover},
		"schema": info.Schema, "views": info.Views, "role": model.RoleViewer})
}

func (s *Service) hQuery(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	info, hide, err := s.dbScope(r, id)
	if err != nil {
		return err
	}
	var in dbs.QueryInput
	if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	// 匿名访问只按视图查询：不接受临时条件，免得用条件反推被隐藏列的值。
	q := dbs.QueryInput{ViewID: in.ViewID, Search: in.Search, Limit: in.Limit, Offset: in.Offset}
	_, res, err := s.dbs.Query(r.Context(), id, q)
	if err != nil {
		return err
	}
	users := map[string]catalogUser{}
	clean := func(rows []*dbs.Row) {
		for _, row := range rows {
			for k := range hide {
				delete(row.Props, k)
				delete(row.Computed, k)
			}
			for _, u := range s.usersIn(row.CreatedBy, row.UpdatedBy) {
				users[u.ID] = u
			}
			for _, p := range info.Schema.Properties {
				if p.Type == dbs.TPerson {
					for _, uid := range stringList(row.Props[p.ID]) {
						for _, u := range s.usersIn(uid) {
							users[u.ID] = u
						}
					}
				}
			}
		}
	}
	clean(res.Rows)
	for _, g := range res.Groups {
		clean(g.Rows)
	}
	for k := range hide {
		delete(res.Calcs, k)
	}
	return web.OK(w, map[string]any{"rows": res.Rows, "groups": res.Groups, "total": res.Total, "calcs": res.Calcs, "users": users})
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{x}
	}
	return nil
}

func (s *Service) hChart(w http.ResponseWriter, r *http.Request) error {
	var in dbs.ChartSpec
	if err := web.ReadJSONLimit(r, &in, 64<<10); err != nil {
		return err
	}
	_, hide, err := s.dbScope(r, in.Source.DatabaseID)
	if err != nil {
		return err
	}
	bad := hide[in.Source.X] || hide[in.Source.GroupBy]
	for _, y := range in.Source.Y {
		bad = bad || hide[y.Property]
	}
	if bad {
		return errNoShare
	}
	opt, err := s.dbs.ChartOption(r.Context(), in)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"option": opt})
}

// ---------- 附件 ----------

func (s *Service) hFile(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	pc := pubOf(r)
	f, err := s.files.Get(r.Context(), chi.URLParam(r, "fid"))
	if err != nil || f.DeletedAt != nil || f.PageID == nil || !pc.scope.has(*f.PageID) {
		return errNoShare
	}
	return ws.ServeFile(w, r, f)
}
