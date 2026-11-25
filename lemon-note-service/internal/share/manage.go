package share

import (
	"net/http"
	"sort"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/realtime"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// 可以分享给用户的角色（admin = 完全访问，可以继续分享）。
var userRoles = map[string]bool{model.RoleViewer: true, model.RoleCommenter: true, model.RoleEditor: true, model.RoleManager: true}

// Mount 挂载需要登录的分享管理接口。
func (s *Service) Mount(ws *web.Server, r chi.Router) {
	h := ws.H
	r.Get("/api/pages/{id}/sharing", h(func(w http.ResponseWriter, r *http.Request) error { return s.hGet(ws, w, r) }))
	r.Put("/api/pages/{id}/sharing/users/{uid}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSetUser(ws, w, r) }))
	r.Delete("/api/pages/{id}/sharing/users/{uid}", h(func(w http.ResponseWriter, r *http.Request) error { return s.hRemoveUser(ws, w, r) }))
	r.Put("/api/pages/{id}/sharing/public", h(func(w http.ResponseWriter, r *http.Request) error { return s.hSetPublic(ws, w, r) }))
	r.Delete("/api/pages/{id}/sharing/public", h(func(w http.ResponseWriter, r *http.Request) error { return s.hRemovePublic(ws, w, r) }))
	r.Post("/api/pages/{id}/sharing/public/rotate", h(func(w http.ResponseWriter, r *http.Request) error { return s.hRotatePublic(ws, w, r) }))
}

type userShareView struct {
	ShareID   string      `json:"share_id"`
	User      catalogUser `json:"user"`
	Role      string      `json:"role"`
	PageID    string      `json:"page_id"`
	PageTitle string      `json:"page_title"`
	Inherited bool        `json:"inherited"`
	CreatedAt int64       `json:"created_at"`
}

type catalogUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar,omitempty"`
}

type publicView struct {
	ID              string `json:"id"`
	Token           string `json:"token"`
	Path            string `json:"path"`
	PageID          string `json:"page_id"`
	PageTitle       string `json:"page_title"`
	IncludeChildren bool   `json:"include_children"`
	AllowIndex      bool   `json:"allow_index"`
	HasPassword     bool   `json:"has_password"`
	ExpiresAt       *int64 `json:"expires_at"`
	Expired         bool   `json:"expired"`
	CreatedAt       int64  `json:"created_at"`
	CreatedBy       string `json:"created_by"`
}

func (s *Service) userOf(id string) catalogUser {
	u, _ := s.cat.User(id)
	return catalogUser{ID: id, Username: u.Username, DisplayName: u.DisplayName, Avatar: u.Avatar}
}

func (s *Service) viewPublic(sh model.PageShare) publicView {
	title := ""
	if p, ok := s.cat.Page(sh.PageID); ok {
		title = p.Title
	}
	tok := model.Deref(sh.Token)
	return publicView{ID: sh.ID, Token: tok, Path: "/share/" + tok, PageID: sh.PageID, PageTitle: title, IncludeChildren: sh.IncludeChildren,
		AllowIndex: sh.AllowIndex, HasPassword: sh.PasswordHash != "", ExpiresAt: sh.ExpiresAt,
		Expired: sh.ExpiresAt != nil && *sh.ExpiresAt <= time.Now().UnixMilli(), CreatedAt: sh.CreatedAt, CreatedBy: sh.CreatedBy}
}

func active(sh *model.PageShare) bool { return sh.RevokedAt == nil }

// hGet 返回页面的分享情况：自己的角色、空间成员、分享给用户的（含从上级页面继承的）、公开链接。
func (s *Service) hGet(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	meta, err := ws.NeedPage(r, id, model.RoleViewer)
	if err != nil {
		return err
	}
	role := ws.PageRole(r, id)
	users := []userShareView{}
	var public *publicView
	inheritedPublic := []publicView{}
	chain := append([]string{id}, ancestorsOf(s, id)...)
	for i, pid := range chain {
		pm, _ := s.cat.Page(pid)
		for _, sh := range s.cat.SharesOf(pid) {
			if !active(&sh) {
				continue
			}
			switch sh.Kind {
			case model.ShareUser:
				users = append(users, userShareView{ShareID: sh.ID, User: s.userOf(model.Deref(sh.UserID)), Role: sh.Role, PageID: pid,
					PageTitle: pm.Title, Inherited: i > 0, CreatedAt: sh.CreatedAt})
			case model.SharePublic:
				v := s.viewPublic(sh)
				if i == 0 {
					public = &v
				} else if sh.IncludeChildren {
					inheritedPublic = append(inheritedPublic, v)
				}
			}
		}
	}
	sort.SliceStable(users, func(a, b int) bool {
		if users[a].Inherited != users[b].Inherited {
			return !users[a].Inherited
		}
		return users[a].CreatedAt < users[b].CreatedAt
	})
	space, _ := s.cat.Space(meta.SpaceID)
	members := s.cat.Members(meta.SpaceID)
	canManage := model.RoleAtLeast(role, model.RoleManager)
	if !canManage && public != nil {
		public.Token, public.Path = "", "" // 不能管理分享的人看不到令牌
	}
	if !canManage {
		for i := range inheritedPublic {
			inheritedPublic[i].Token, inheritedPublic[i].Path = "", ""
		}
	}
	return web.OK(w, map[string]any{
		"role": role, "can_manage": canManage, "public_enabled": s.publicEnabled(r.Context()),
		"space": map[string]any{"id": space.ID, "name": space.Name, "kind": space.Kind, "members": len(members)},
		"users": users, "public": public, "inherited_public": inheritedPublic,
	})
}

func ancestorsOf(s *Service, id string) []string {
	var out []string
	for _, a := range s.cat.Ancestors(id) {
		if a.ID != id {
			out = append(out, a.ID)
		}
	}
	// Ancestors 从根到父；这里要从近到远
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *Service) needManage(ws *web.Server, r *http.Request, id string) (string, error) {
	meta, err := ws.NeedPage(r, id, model.RoleManager)
	if err != nil {
		return "", err
	}
	if meta.TrashedAt != 0 {
		return "", web.Bad("页面在回收站里")
	}
	return meta.SpaceID, nil
}

func (s *Service) hSetUser(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	id, uid := chi.URLParam(r, "id"), chi.URLParam(r, "uid")
	spaceID, err := s.needManage(ws, r, id)
	if err != nil {
		return err
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := web.ReadJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if !userRoles[in.Role] {
		return web.Bad("角色不正确")
	}
	me := web.UserOf(r).ID
	if uid == me {
		return web.Bad("不能分享给自己")
	}
	if u, ok := s.cat.User(uid); !ok || u.Status != model.UserActive {
		return web.NotFound("用户")
	}
	isNew := false
	_, err = s.st.Write(r.Context(), me, func(tx *store.Tx) error {
		var cur []model.PageShare
		if err := tx.Select(&cur, "SELECT * FROM page_shares WHERE page_id = ? AND kind = ? AND user_id = ? AND revoked_at IS NULL", id, model.ShareUser, uid); err != nil {
			return err
		}
		if len(cur) > 0 {
			sh := cur[0]
			sh.Role = in.Role
			for _, extra := range cur[1:] { // 理论上只有一条；多了就收拾掉
				now := model.Now()
				extra.RevokedAt = &now
				if err := tx.Put("page_shares", extra); err != nil {
					return err
				}
			}
			return tx.Put("page_shares", sh)
		}
		isNew = true
		sh := model.PageShare{ID: ids.New(), PageID: id, SpaceID: spaceID, Kind: model.ShareUser, UserID: model.StrPtr(uid), Role: in.Role,
			Settings: "{}", CreatedAt: model.Now(), CreatedBy: me}
		if err := tx.Put("page_shares", sh); err != nil {
			return err
		}
		return tx.Put("notifications", model.Notification{ID: ids.New(), UserID: uid, Kind: "share", PageID: model.StrPtr(id),
			ActorID: model.StrPtr(me), Data: `{"role":"` + in.Role + `"}`, CreatedAt: model.Now()})
	})
	if err != nil {
		return err
	}
	ws.Audit(r, me, "share_user_set", id, map[string]any{"user_id": uid, "role": in.Role, "new": isNew})
	s.hub.SendToUser(uid, realtime.Message{Type: "shares"})
	return s.hGet(ws, w, r)
}

func (s *Service) hRemoveUser(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	id, uid := chi.URLParam(r, "id"), chi.URLParam(r, "uid")
	me := web.UserOf(r).ID
	if uid != me { // 自己可以退出别人分享给自己的页面
		if _, err := s.needManage(ws, r, id); err != nil {
			return err
		}
	} else if _, err := ws.NeedPage(r, id, model.RoleViewer); err != nil {
		return err
	}
	n := 0
	_, err := s.st.Write(r.Context(), me, func(tx *store.Tx) error {
		var cur []model.PageShare
		if err := tx.Select(&cur, "SELECT * FROM page_shares WHERE page_id = ? AND kind = ? AND user_id = ? AND revoked_at IS NULL", id, model.ShareUser, uid); err != nil {
			return err
		}
		now := model.Now()
		for _, sh := range cur {
			sh.RevokedAt = &now
			if err := tx.Put("page_shares", sh); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if n > 0 {
		ws.Audit(r, me, "share_user_removed", id, map[string]any{"user_id": uid})
		s.hub.SendToUser(uid, realtime.Message{Type: "shares"})
	}
	if uid == me && ws.PageRole(r, id) == model.RoleNone {
		return web.OK(w, map[string]any{"left": true}) // 自己退出后已经看不到这个页面了
	}
	return s.hGet(ws, w, r)
}

type publicInput struct {
	IncludeChildren *bool   `json:"include_children"`
	AllowIndex      *bool   `json:"allow_index"`
	Password        *string `json:"password"`   // nil 不改；"" 去掉口令
	ExpiresAt       *int64  `json:"expires_at"` // 配合 SetExpiry 使用
	SetExpiry       bool    `json:"set_expiry"` // true 时按 ExpiresAt 设置（nil = 永久）
}

func (s *Service) activePublic(tx *store.Tx, pageID string) ([]model.PageShare, error) {
	var cur []model.PageShare
	err := tx.Select(&cur, "SELECT * FROM page_shares WHERE page_id = ? AND kind = ? AND revoked_at IS NULL ORDER BY created_at", pageID, model.SharePublic)
	return cur, err
}

func (s *Service) hSetPublic(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	spaceID, err := s.needManage(ws, r, id)
	if err != nil {
		return err
	}
	if !s.publicEnabled(r.Context()) {
		return &web.APIError{Status: 403, Code: "public_sharing_disabled", Message: "管理员已关闭公开链接"}
	}
	var in publicInput
	if err := web.ReadJSONLimit(r, &in, 8192); err != nil {
		return err
	}
	pwHash := ""
	if in.Password != nil && *in.Password != "" {
		if len([]rune(*in.Password)) < 4 || len(*in.Password) > 128 {
			return web.Bad("口令长度需在 4 到 128 个字符之间")
		}
		if pwHash, err = auth.HashPassword(*in.Password); err != nil {
			return err
		}
	}
	if in.SetExpiry && in.ExpiresAt != nil && *in.ExpiresAt <= time.Now().UnixMilli() {
		return web.Bad("有效期必须晚于现在")
	}
	me := web.UserOf(r).ID
	created := false
	_, err = s.st.Write(r.Context(), me, func(tx *store.Tx) error {
		cur, err := s.activePublic(tx, id)
		if err != nil {
			return err
		}
		var sh model.PageShare
		if len(cur) > 0 {
			sh = cur[0]
			now := model.Now()
			for _, extra := range cur[1:] {
				extra.RevokedAt = &now
				if err := tx.Put("page_shares", extra); err != nil {
					return err
				}
			}
		} else {
			created = true
			tok := newToken()
			sh = model.PageShare{ID: ids.New(), PageID: id, SpaceID: spaceID, Kind: model.SharePublic, Role: model.RoleViewer, Token: &tok,
				Settings: "{}", CreatedAt: model.Now(), CreatedBy: me}
		}
		if in.IncludeChildren != nil {
			sh.IncludeChildren = *in.IncludeChildren
		}
		if in.AllowIndex != nil {
			sh.AllowIndex = *in.AllowIndex
		}
		if in.Password != nil {
			sh.PasswordHash = pwHash
		}
		if in.SetExpiry {
			sh.ExpiresAt = in.ExpiresAt
		}
		return tx.Put("page_shares", sh)
	})
	if err != nil {
		return err
	}
	detail := map[string]any{"created": created}
	if in.Password != nil {
		detail["password"] = *in.Password != ""
	}
	if in.IncludeChildren != nil {
		detail["include_children"] = *in.IncludeChildren
	}
	ws.Audit(r, me, "share_public_set", id, detail)
	return s.hGet(ws, w, r)
}

func (s *Service) hRemovePublic(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.needManage(ws, r, id); err != nil {
		return err
	}
	me := web.UserOf(r).ID
	_, err := s.st.Write(r.Context(), me, func(tx *store.Tx) error {
		cur, err := s.activePublic(tx, id)
		if err != nil {
			return err
		}
		now := model.Now()
		for _, sh := range cur {
			sh.RevokedAt = &now
			if err := tx.Put("page_shares", sh); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	ws.Audit(r, me, "share_public_removed", id, nil)
	return s.hGet(ws, w, r)
}

// hRotatePublic 换一个新令牌（旧链接立即失效），其他设置不变。
func (s *Service) hRotatePublic(ws *web.Server, w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := s.needManage(ws, r, id); err != nil {
		return err
	}
	me := web.UserOf(r).ID
	_, err := s.st.Write(r.Context(), me, func(tx *store.Tx) error {
		cur, err := s.activePublic(tx, id)
		if err != nil {
			return err
		}
		if len(cur) == 0 {
			return web.NotFound("公开链接")
		}
		now := model.Now()
		old := cur[0]
		for _, sh := range cur {
			sh.RevokedAt = &now
			if err := tx.Put("page_shares", sh); err != nil {
				return err
			}
		}
		tok := newToken()
		nw := old
		nw.ID, nw.Token, nw.CreatedAt, nw.CreatedBy, nw.RevokedAt = ids.New(), &tok, now, me, nil
		return tx.Put("page_shares", nw)
	})
	if err != nil {
		return err
	}
	ws.Audit(r, me, "share_public_rotated", id, nil)
	return s.hGet(ws, w, r)
}
