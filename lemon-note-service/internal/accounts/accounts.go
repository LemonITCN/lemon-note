// Package accounts 管理用户与空间：建用户（同时建个人空间）、改资料、重置口令、共享空间与成员。
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/auth"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

type Service struct {
	st  *store.Store
	cat *catalog.Catalog
}

func New(st *store.Store, cat *catalog.Catalog) *Service { return &Service{st: st, cat: cat} }

// NewUser 新建用户参数。
type NewUser struct {
	Username    string
	DisplayName string
	Password    string
	Role        string
	Locale      string
}

var ErrUsernameTaken = errors.New("用户名已被使用")

// CreateUser 建用户与其个人空间。
func (s *Service) CreateUser(ctx context.Context, actor string, in NewUser) (*model.User, error) {
	u, err := BuildUser(in)
	if err != nil {
		return nil, err
	}
	if actor == "" {
		actor = u.ID
	}
	_, err = s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM users WHERE username = ?", u.Username).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrUsernameTaken
		}
		if err := tx.Put("users", u); err != nil {
			return err
		}
		return PutPersonalSpace(tx, u)
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// BuildUser 校验并构造用户（不写库）。
func BuildUser(in NewUser) (*model.User, error) {
	name := auth.NormalizeUsername(in.Username)
	if !auth.ValidUsername(name) {
		return nil, errors.New("用户名需为 3-32 位小写字母、数字、下划线、点或减号")
	}
	if err := auth.CheckPasswordPolicy(in.Password); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	role := in.Role
	if role != model.RoleAdmin {
		role = model.RoleMember
	}
	display := strings.TrimSpace(in.DisplayName)
	if display == "" {
		display = name
	}
	if utf8.RuneCountInString(display) > 64 {
		return nil, errors.New("显示名称太长")
	}
	locale := in.Locale
	if locale != "en" {
		locale = "zh-CN"
	}
	now := model.Now()
	return &model.User{ID: ids.New(), Username: name, DisplayName: display, PasswordHash: hash, Role: role, Status: model.UserActive,
		Locale: locale, Prefs: "{}", RecoveryCodes: "[]", PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}, nil
}

// PutPersonalSpace 为用户建个人空间。
func PutPersonalSpace(tx *store.Tx, u *model.User) error {
	now := model.Now()
	name := u.DisplayName
	if u.Locale == "en" {
		name += "'s space"
	} else {
		name += " 的空间"
	}
	return tx.Put("spaces", model.Space{ID: ids.New(), Kind: model.SpacePersonal, Name: name, Icon: "", OwnerID: u.ID,
		Settings: "{}", CreatedAt: now, UpdatedAt: now})
}

// UpdateUserTx 在事务里读改写一个用户。
func UpdateUserTx(tx *store.Tx, id string, fn func(u *model.User) error) (*model.User, error) {
	var u model.User
	if err := tx.Get(&u, "SELECT * FROM users WHERE id = ?", id); err != nil {
		return nil, errors.New("用户不存在")
	}
	if err := fn(&u); err != nil {
		return nil, err
	}
	u.UpdatedAt = model.Now()
	return &u, tx.Put("users", &u)
}

// UpdateUser 读改写用户。
func (s *Service) UpdateUser(ctx context.Context, actor, id string, fn func(u *model.User) error) (*model.User, error) {
	var out *model.User
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		u, err := UpdateUserTx(tx, id, fn)
		out = u
		return err
	})
	return out, err
}

// SetPassword 设置新口令（让该用户所有旧会话失效）。
func (s *Service) SetPassword(ctx context.Context, actor, id, pw string) error {
	if err := auth.CheckPasswordPolicy(pw); err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = s.UpdateUser(ctx, actor, id, func(u *model.User) error {
		u.PasswordHash = hash
		u.PasswordChangedAt = model.Now()
		return nil
	})
	return err
}

// CountAdmins 返回启用中的管理员数量。
func (s *Service) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.st.QueryRow(ctx, "SELECT count(*) FROM users WHERE role = ? AND status = ?", model.RoleAdmin, model.UserActive).Scan(&n)
	return n, err
}

// ---------- 空间 ----------

// CreateSharedSpace 建共享空间，创建者为所有者。
func (s *Service) CreateSharedSpace(ctx context.Context, actor, name, icon string) (*model.Space, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return nil, errors.New("空间名称不能为空且不超过 80 字")
	}
	now := model.Now()
	sp := &model.Space{ID: ids.New(), Kind: model.SpaceShared, Name: name, Icon: icon, OwnerID: actor, Settings: "{}", CreatedAt: now, UpdatedAt: now}
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error { return tx.Put("spaces", sp) })
	return sp, err
}

// UpdateSpace 改名称、图标、设置。
func (s *Service) UpdateSpace(ctx context.Context, actor, id string, name, icon *string, settings json.RawMessage) (*model.Space, error) {
	var out model.Space
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		if err := tx.Get(&out, "SELECT * FROM spaces WHERE id = ?", id); err != nil {
			return errors.New("空间不存在")
		}
		if name != nil {
			n := strings.TrimSpace(*name)
			if n == "" || utf8.RuneCountInString(n) > 80 {
				return errors.New("空间名称不能为空且不超过 80 字")
			}
			out.Name = n
		}
		if icon != nil {
			out.Icon = *icon
		}
		if settings != nil {
			out.Settings = string(settings)
		}
		out.UpdatedAt = model.Now()
		return tx.Put("spaces", &out)
	})
	return &out, err
}

// DeleteSpace 删除共享空间（软删除，数据保留在数据目录里）。
func (s *Service) DeleteSpace(ctx context.Context, actor, id string) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var sp model.Space
		if err := tx.Get(&sp, "SELECT * FROM spaces WHERE id = ?", id); err != nil {
			return errors.New("空间不存在")
		}
		if sp.Kind == model.SpacePersonal {
			return errors.New("个人空间不能删除")
		}
		now := model.Now()
		sp.DeletedAt = &now
		return tx.Put("spaces", &sp)
	})
	return err
}

// SetMember 添加或修改成员角色。
func (s *Service) SetMember(ctx context.Context, actor, spaceID, userID, role string) error {
	if !model.ValidMemberRole(role) {
		return errors.New("无效的角色")
	}
	if _, ok := s.cat.User(userID); !ok {
		return errors.New("用户不存在")
	}
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var sp model.Space
		if err := tx.Get(&sp, "SELECT * FROM spaces WHERE id = ?", spaceID); err != nil || sp.Kind != model.SpaceShared {
			return errors.New("只能给共享空间添加成员")
		}
		if sp.OwnerID == userID {
			return errors.New("不能修改所有者的角色")
		}
		var m model.SpaceMember
		now := model.Now()
		if err := tx.Get(&m, "SELECT * FROM space_members WHERE space_id = ? AND user_id = ?", spaceID, userID); err != nil {
			m = model.SpaceMember{ID: ids.New(), SpaceID: spaceID, UserID: userID, CreatedAt: now}
		}
		m.Role = role
		m.UpdatedAt = now
		return tx.Put("space_members", m)
	})
	return err
}

// RemoveMember 移除成员。
func (s *Service) RemoveMember(ctx context.Context, actor, spaceID, userID string) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var m model.SpaceMember
		if err := tx.Get(&m, "SELECT * FROM space_members WHERE space_id = ? AND user_id = ?", spaceID, userID); err != nil {
			return nil
		}
		return tx.Delete("space_members", m.ID)
	})
	return err
}

// TransferOwnership 转让共享空间所有权（原所有者降为管理员）。
func (s *Service) TransferOwnership(ctx context.Context, actor, spaceID, newOwner string) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var sp model.Space
		if err := tx.Get(&sp, "SELECT * FROM spaces WHERE id = ?", spaceID); err != nil || sp.Kind != model.SpaceShared {
			return errors.New("只能转让共享空间")
		}
		old := sp.OwnerID
		sp.OwnerID = newOwner
		sp.UpdatedAt = model.Now()
		if err := tx.Put("spaces", &sp); err != nil {
			return err
		}
		var m model.SpaceMember
		if err := tx.Get(&m, "SELECT * FROM space_members WHERE space_id = ? AND user_id = ?", spaceID, newOwner); err == nil {
			if err := tx.Delete("space_members", m.ID); err != nil {
				return err
			}
		}
		now := model.Now()
		return tx.Put("space_members", model.SpaceMember{ID: ids.New(), SpaceID: spaceID, UserID: old, Role: model.RoleManager, CreatedAt: now, UpdatedAt: now})
	})
	return err
}
