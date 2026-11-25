package catalog

import (
	"sort"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
)

// SpaceRole 返回用户在空间里的角色。个人空间只有所有者；系统管理员没有特权。
func (c *Catalog) SpaceRole(userID, spaceID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.spaceRoleLocked(userID, spaceID)
}

func (c *Catalog) spaceRoleLocked(userID, spaceID string) string {
	s := c.spaces[spaceID]
	if s == nil || s.DeletedAt != nil || userID == "" {
		return model.RoleNone
	}
	if u := c.users[userID]; u == nil || u.Status != model.UserActive {
		return model.RoleNone
	}
	if s.OwnerID == userID {
		return model.RoleOwner
	}
	if s.Kind == model.SpacePersonal {
		return model.RoleNone
	}
	return c.members[spaceID][userID]
}

func shareActive(s *model.PageShare, now int64) bool {
	return s.RevokedAt == nil && (s.ExpiresAt == nil || *s.ExpiresAt > now)
}

// PageRole 返回用户对页面的角色 = max(空间角色, 自己及祖先页面上分享给该用户的角色)。
func (c *Catalog) PageRole(userID, pageID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p := c.pages[pageID]
	if p == nil || userID == "" {
		return model.RoleNone
	}
	if u := c.users[userID]; u == nil || u.Status != model.UserActive {
		return model.RoleNone
	}
	role := c.spaceRoleLocked(userID, p.SpaceID)
	if role == model.RoleOwner {
		return role
	}
	now := time.Now().UnixMilli()
	seen := map[string]bool{}
	for cur := p; cur != nil && !seen[cur.ID]; cur = c.pages[cur.ParentID] {
		seen[cur.ID] = true
		for sid := range c.byPage[cur.ID] {
			s := c.shares[sid]
			if s != nil && s.Kind == model.ShareUser && model.Deref(s.UserID) == userID && shareActive(s, now) {
				role = model.MaxRole(role, s.Role)
			}
		}
		if cur.ParentID == "" {
			break
		}
	}
	return role
}

// CanRead / CanComment / CanEdit 便捷判断。
func (c *Catalog) CanRead(userID, pageID string) bool {
	return model.RoleAtLeast(c.PageRole(userID, pageID), model.RoleViewer)
}
func (c *Catalog) CanComment(userID, pageID string) bool {
	return model.RoleAtLeast(c.PageRole(userID, pageID), model.RoleCommenter)
}
func (c *Catalog) CanEdit(userID, pageID string) bool {
	return model.RoleAtLeast(c.PageRole(userID, pageID), model.RoleEditor)
}

// VisibleSpaces 返回用户有角色的空间（个人空间在前）。
func (c *Catalog) VisibleSpaces(userID string) []model.Space {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []model.Space
	for _, s := range c.spaces {
		if c.spaceRoleLocked(userID, s.ID) != model.RoleNone {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == model.SpacePersonal
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// SharedWithUser 返回直接分享给该用户、且用户在其空间里没有角色的页面（「与我共享」）。
func (c *Catalog) SharedWithUser(userID string) []PageMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now().UnixMilli()
	seen := map[string]bool{}
	out := []PageMeta{}
	for _, s := range c.shares {
		if s.Kind != model.ShareUser || model.Deref(s.UserID) != userID || !shareActive(s, now) || seen[s.PageID] {
			continue
		}
		p := c.pages[s.PageID]
		if p == nil || p.TrashedAt != 0 || c.spaceRoleLocked(userID, p.SpaceID) != model.RoleNone {
			continue
		}
		seen[s.PageID] = true
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// PublicCanView 判断公开分享能否看到某页面。
func (c *Catalog) PublicCanView(s model.PageShare, pageID string) bool {
	if s.Kind != model.SharePublic || !shareActive(&s, time.Now().UnixMilli()) {
		return false
	}
	p, ok := c.Page(pageID)
	if !ok || p.TrashedAt != 0 {
		return false
	}
	if pageID == s.PageID {
		return true
	}
	return s.IncludeChildren && c.IsDescendant(pageID, s.PageID)
}
