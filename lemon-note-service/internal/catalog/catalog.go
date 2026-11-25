// Package catalog 在内存里维护页面树、空间、成员、分享与用户的轻量索引，
// 随每次提交增量更新。权限判断、侧栏、面包屑、搜索过滤都基于它。
package catalog

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// PageMeta 是页面的轻量信息。
type PageMeta struct {
	ID         string `json:"id"`
	SpaceID    string `json:"space_id"`
	ParentID   string `json:"parent_id,omitempty"`
	DatabaseID string `json:"database_id,omitempty"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Icon       string `json:"icon,omitempty"`
	Position   string `json:"position"`
	Inline     bool   `json:"inline,omitempty"`
	IsTemplate bool   `json:"is_template,omitempty"`
	TrashedAt  int64  `json:"trashed_at,omitempty"`
	TrashRoot  string `json:"trash_root,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	CreatedBy  string `json:"created_by"`
	UpdatedAt  int64  `json:"updated_at"`
	UpdatedBy  string `json:"updated_by"`
}

// UserMeta 是用户的公开信息。
type UserMeta struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar,omitempty"`
	Role        string `json:"-"`
	Status      string `json:"-"`
}

// Catalog 是内存索引。
type Catalog struct {
	mu       sync.RWMutex
	pages    map[string]*PageMeta
	children map[string]map[string]struct{} // parentKey → 子页面 id
	spaces   map[string]*model.Space
	members  map[string]map[string]string // spaceID → userID → role
	memberRecs map[string]model.SpaceMember // 成员记录 id → 记录
	shares   map[string]*model.PageShare  // shareID → share
	byPage   map[string]map[string]struct{}
	users    map[string]*UserMeta
}

func parentKey(spaceID, parentID string) string {
	if parentID == "" {
		return "space:" + spaceID
	}
	return parentID
}

// Load 从数据库全量加载并订阅后续提交。
func Load(ctx context.Context, st *store.Store) (*Catalog, error) {
	c := &Catalog{}
	if err := c.reload(ctx, st); err != nil {
		return nil, err
	}
	st.Subscribe(c.apply)
	return c, nil
}

func (c *Catalog) reload(ctx context.Context, st *store.Store) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pages = map[string]*PageMeta{}
	c.children = map[string]map[string]struct{}{}
	c.spaces = map[string]*model.Space{}
	c.members = map[string]map[string]string{}
	c.memberRecs = map[string]model.SpaceMember{}
	c.shares = map[string]*model.PageShare{}
	c.byPage = map[string]map[string]struct{}{}
	c.users = map[string]*UserMeta{}

	var pages []model.Page
	if err := st.Select(ctx, &pages, "SELECT id, space_id, parent_id, database_id, kind, title, icon, '' AS cover, '' AS props, position, flags, is_template, content_rev, created_at, created_by, updated_at, updated_by, trashed_at, trashed_by, trash_root FROM pages"); err != nil {
		return err
	}
	for i := range pages {
		c.putPage(metaFromPage(&pages[i]))
	}
	var spaces []model.Space
	if err := st.Select(ctx, &spaces, "SELECT * FROM spaces"); err != nil {
		return err
	}
	for i := range spaces {
		s := spaces[i]
		c.spaces[s.ID] = &s
	}
	var members []model.SpaceMember
	if err := st.Select(ctx, &members, "SELECT * FROM space_members"); err != nil {
		return err
	}
	for _, m := range members {
		c.putMember(m)
	}
	var shares []model.PageShare
	if err := st.Select(ctx, &shares, "SELECT * FROM page_shares WHERE revoked_at IS NULL"); err != nil {
		return err
	}
	for i := range shares {
		s := shares[i]
		c.putShare(&s)
	}
	var users []model.User
	if err := st.Select(ctx, &users, "SELECT * FROM users"); err != nil {
		return err
	}
	for i := range users {
		u := users[i]
		c.users[u.ID] = &UserMeta{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Avatar: u.Avatar, Role: u.Role, Status: u.Status}
	}
	return nil
}

func metaFromPage(p *model.Page) *PageMeta {
	f := p.GetFlags()
	m := &PageMeta{ID: p.ID, SpaceID: p.SpaceID, ParentID: model.Deref(p.ParentID), DatabaseID: model.Deref(p.DatabaseID),
		Kind: p.Kind, Title: p.Title, Icon: p.Icon, Position: p.Position, Inline: f.Inline, IsTemplate: p.IsTemplate,
		CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy, UpdatedAt: p.UpdatedAt, UpdatedBy: p.UpdatedBy, TrashRoot: model.Deref(p.TrashRoot)}
	if p.TrashedAt != nil {
		m.TrashedAt = *p.TrashedAt
	}
	return m
}

func (c *Catalog) putPage(m *PageMeta) {
	if old, ok := c.pages[m.ID]; ok {
		if ch := c.children[parentKey(old.SpaceID, old.ParentID)]; ch != nil {
			delete(ch, m.ID)
		}
	}
	c.pages[m.ID] = m
	k := parentKey(m.SpaceID, m.ParentID)
	if c.children[k] == nil {
		c.children[k] = map[string]struct{}{}
	}
	c.children[k][m.ID] = struct{}{}
}

func (c *Catalog) delPage(id string) {
	if old, ok := c.pages[id]; ok {
		if ch := c.children[parentKey(old.SpaceID, old.ParentID)]; ch != nil {
			delete(ch, id)
		}
		delete(c.pages, id)
	}
}

func (c *Catalog) putMember(m model.SpaceMember) {
	if old, ok := c.memberRecs[m.ID]; ok {
		if mm := c.members[old.SpaceID]; mm != nil {
			delete(mm, old.UserID)
		}
	}
	c.memberRecs[m.ID] = m
	if c.members[m.SpaceID] == nil {
		c.members[m.SpaceID] = map[string]string{}
	}
	c.members[m.SpaceID][m.UserID] = m.Role
}

func (c *Catalog) putShare(s *model.PageShare) {
	if old, ok := c.shares[s.ID]; ok {
		delete(c.byPage[old.PageID], s.ID)
	}
	if s.RevokedAt != nil {
		delete(c.shares, s.ID)
		return
	}
	c.shares[s.ID] = s
	if c.byPage[s.PageID] == nil {
		c.byPage[s.PageID] = map[string]struct{}{}
	}
	c.byPage[s.PageID][s.ID] = struct{}{}
}

// apply 处理一次提交。
func (c *Catalog) apply(cm store.Commit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, op := range cm.Ops {
		switch op.T {
		case "pages":
			if op.O == "del" {
				c.delPage(op.ID)
				continue
			}
			var p model.Page
			if rowInto(op.Row, &p) == nil {
				c.putPage(metaFromPage(&p))
			}
		case "spaces":
			if op.O == "del" {
				delete(c.spaces, op.ID)
				continue
			}
			var s model.Space
			if rowInto(op.Row, &s) == nil {
				c.spaces[s.ID] = &s
			}
		case "space_members":
			if op.O == "del" {
				if old, ok := c.memberRecs[op.ID]; ok {
					if mm := c.members[old.SpaceID]; mm != nil {
						delete(mm, old.UserID)
					}
					delete(c.memberRecs, op.ID)
				}
				continue
			}
			var m model.SpaceMember
			if rowInto(op.Row, &m) == nil {
				c.putMember(m)
			}
		case "page_shares":
			if op.O == "del" {
				if old, ok := c.shares[op.ID]; ok {
					delete(c.byPage[old.PageID], op.ID)
					delete(c.shares, op.ID)
				}
				continue
			}
			var s model.PageShare
			if rowInto(op.Row, &s) == nil {
				c.putShare(&s)
			}
		case "users":
			if op.O == "del" {
				delete(c.users, op.ID)
				continue
			}
			var u model.User
			if rowInto(op.Row, &u) == nil {
				c.users[u.ID] = &UserMeta{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Avatar: u.Avatar, Role: u.Role, Status: u.Status}
			}
		}
	}
}

// rowInto 把日志行转成结构体（按 db 标签对应）。
func rowInto(row store.Row, dst any) error { return store.FromRow(row, dst) }

// ---------- 查询 ----------

// Page 返回页面信息副本。
func (c *Catalog) Page(id string) (PageMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.pages[id]
	if !ok {
		return PageMeta{}, false
	}
	return *p, true
}

// Children 返回直接子页面（按位置排序）。parentID 为空表示空间根。
func (c *Catalog) Children(spaceID, parentID string) []PageMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.childrenLocked(spaceID, parentID)
}

func (c *Catalog) childrenLocked(spaceID, parentID string) []PageMeta {
	var out []PageMeta
	for id := range c.children[parentKey(spaceID, parentID)] {
		if p := c.pages[id]; p != nil {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Ancestors 返回从根到父页面的祖先链（不含自己）。
func (c *Catalog) Ancestors(id string) []PageMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var chain []PageMeta
	seen := map[string]bool{}
	p := c.pages[id]
	for p != nil && p.ParentID != "" && !seen[p.ParentID] {
		seen[p.ParentID] = true
		parent := c.pages[p.ParentID]
		if parent == nil {
			break
		}
		chain = append(chain, *parent)
		p = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// IsDescendant 判断 id 是否是 ancestor 的子孙（或自己）。
func (c *Catalog) IsDescendant(id, ancestor string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isDescendantLocked(id, ancestor)
}

func (c *Catalog) isDescendantLocked(id, ancestor string) bool {
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		if cur == ancestor {
			return true
		}
		seen[cur] = true
		p := c.pages[cur]
		if p == nil {
			return false
		}
		cur = p.ParentID
	}
	return false
}

// Descendants 返回所有子孙页面 id（不含自己），广度优先。
func (c *Catalog) Descendants(id string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	queue := []string{id}
	seen := map[string]bool{id: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for ch := range c.children[cur] {
			if !seen[ch] {
				seen[ch] = true
				out = append(out, ch)
				queue = append(queue, ch)
			}
		}
	}
	return out
}

// SpacePages 返回空间里的全部页面。
func (c *Catalog) SpacePages(spaceID string) []PageMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []PageMeta
	for _, p := range c.pages {
		if p.SpaceID == spaceID {
			out = append(out, *p)
		}
	}
	return out
}

// Space 返回空间。
func (c *Catalog) Space(id string) (model.Space, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.spaces[id]
	if !ok {
		return model.Space{}, false
	}
	return *s, true
}

// User 返回用户公开信息。
func (c *Catalog) User(id string) (UserMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	u, ok := c.users[id]
	if !ok {
		return UserMeta{}, false
	}
	return *u, true
}

// Users 返回全部用户。
func (c *Catalog) Users() []UserMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]UserMeta, 0, len(c.users))
	for _, u := range c.users {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// Members 返回空间成员（userID → 角色）。
func (c *Catalog) Members(spaceID string) map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]string{}
	for k, v := range c.members[spaceID] {
		out[k] = v
	}
	return out
}

// SharesOf 返回页面上的有效分享。
func (c *Catalog) SharesOf(pageID string) []model.PageShare {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []model.PageShare
	for id := range c.byPage[pageID] {
		if s := c.shares[id]; s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// FindPublicShare 按令牌找公开分享。
func (c *Catalog) FindPublicShare(token string) (model.PageShare, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, s := range c.shares {
		if s.Kind == model.SharePublic && s.Token != nil && *s.Token == token {
			return *s, true
		}
	}
	return model.PageShare{}, false
}

// SearchTitles 按标题子串查页面（只给权限过滤之前的候选）。
func (c *Catalog) SearchTitles(q string, limit int) []PageMeta {
	q = strings.ToLower(q)
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []PageMeta
	for _, p := range c.pages {
		if p.TrashedAt == 0 && strings.Contains(strings.ToLower(p.Title), q) {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
