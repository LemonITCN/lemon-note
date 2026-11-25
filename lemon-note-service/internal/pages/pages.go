// Package pages 实现页面与块：建页、改元信息、移动、回收站、复制、块级增量写入、历史版本。
// 所有方法都假定调用方已经做过权限检查（见 web 与 tools 层），这里只保证数据一致性。
package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/fracidx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// 限制
const (
	MaxBlockBytes = 4 << 20
	MaxTitleRunes = 500
)

var (
	ErrNotFound = errors.New("页面不存在")
	ErrLocked   = errors.New("页面已锁定，解锁后才能编辑")
	ErrCycle    = errors.New("不能把页面移动到它自己的子页面下")
)

// Service 页面服务。
type Service struct {
	st  *store.Store
	cat *catalog.Catalog
	// OnPurge 页面被彻底删除时回调（附件引用、搜索索引等清理）
	OnPurge func(pageIDs []string)
}

func New(st *store.Store, cat *catalog.Catalog) *Service { return &Service{st: st, cat: cat} }

// BlockDoc 是一个块（对外的 JSON 形式）。
type BlockDoc struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Position string          `json:"position"`
	Data     json.RawMessage `json:"data"`
	Version  int64           `json:"version"`
}

// PageDoc 是页面加正文。
type PageDoc struct {
	Page   *model.Page     `json:"page"`
	Flags  model.PageFlags `json:"flags"`
	Props  json.RawMessage `json:"props"`
	Blocks []BlockDoc      `json:"blocks"`
}

// Get 读页面元信息。
func (s *Service) Get(ctx context.Context, id string) (*model.Page, error) {
	var p model.Page
	if err := s.st.Get(ctx, &p, "SELECT * FROM pages WHERE id = ?", id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// Blocks 读页面全部块（按位置排序）。
func (s *Service) Blocks(ctx context.Context, pageID string) ([]model.Block, error) {
	var bs []model.Block
	if err := s.st.Select(ctx, &bs, "SELECT * FROM blocks WHERE page_id = ?", pageID); err != nil {
		return nil, err
	}
	sortBlocks(bs)
	return bs, nil
}

func sortBlocks(bs []model.Block) {
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].Position != bs[j].Position {
			return bs[i].Position < bs[j].Position
		}
		return bs[i].ID < bs[j].ID
	})
}

// Doc 读页面与正文。
func (s *Service) Doc(ctx context.Context, id string) (*PageDoc, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	bs, err := s.Blocks(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &PageDoc{Page: p, Flags: p.GetFlags(), Props: rawOr(p.Props, "{}"), Blocks: make([]BlockDoc, 0, len(bs))}
	for _, b := range bs {
		d.Blocks = append(d.Blocks, BlockDoc{ID: b.ID, Type: b.Type, Position: b.Position, Data: json.RawMessage(b.Data), Version: b.Version})
	}
	return d, nil
}

func rawOr(s, def string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage(def)
	}
	return json.RawMessage(s)
}

// ---------- 建页 ----------

// CreateInput 新建页面参数。
type CreateInput struct {
	ID         string // 可选，指定 id（导入时用）
	SpaceID    string
	ParentID   string
	DatabaseID string // 非空表示数据库行（ParentID 必须等于它）
	Kind       string
	Title      string
	Icon       string
	Cover      string
	Props      json.RawMessage
	Flags      model.PageFlags
	IsTemplate bool
	AfterID    string // 插在某个兄弟后面；空表示末尾
	First      bool   // 插在最前
	Blocks     []BlockDoc
}

// Create 新建页面（含正文块）。
func (s *Service) Create(ctx context.Context, actor string, in CreateInput) (*model.Page, error) {
	var out *model.Page
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		p, err := s.CreateTx(tx, actor, in)
		out = p
		return err
	})
	return out, err
}

// CreateTx 在已有事务里建页。
func (s *Service) CreateTx(tx *store.Tx, actor string, in CreateInput) (*model.Page, error) {
	if in.Kind == "" {
		in.Kind = model.KindPage
	}
	if in.Kind != model.KindPage && in.Kind != model.KindDatabase {
		return nil, fmt.Errorf("未知的页面类型 %s", in.Kind)
	}
	if _, ok := s.cat.Space(in.SpaceID); !ok {
		return nil, errors.New("空间不存在")
	}
	if in.ParentID != "" {
		parent, ok := s.cat.Page(in.ParentID)
		if !ok || parent.SpaceID != in.SpaceID {
			return nil, errors.New("上级页面不存在或不在同一空间")
		}
		if parent.TrashedAt != 0 {
			return nil, errors.New("上级页面在回收站里")
		}
	}
	if in.DatabaseID != "" && in.DatabaseID != in.ParentID {
		return nil, errors.New("数据库行的上级必须是数据库本身")
	}
	pos, err := s.siblingPosition(in.SpaceID, in.ParentID, in.AfterID, in.First)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if id == "" {
		id = ids.New()
	}
	now := model.Now()
	p := &model.Page{ID: id, SpaceID: in.SpaceID, ParentID: model.StrPtr(in.ParentID), DatabaseID: model.StrPtr(in.DatabaseID),
		Kind: in.Kind, Title: clampTitle(in.Title), Icon: in.Icon, Cover: in.Cover, Props: string(rawOr(string(in.Props), "{}")),
		Position: pos, IsTemplate: in.IsTemplate, CreatedAt: now, CreatedBy: actor, UpdatedAt: now, UpdatedBy: actor}
	p.SetFlags(in.Flags)
	if err := tx.Put("pages", p); err != nil {
		return nil, err
	}
	if len(in.Blocks) > 0 {
		if err := s.insertBlocks(tx, actor, id, in.Blocks); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func clampTitle(t string) string {
	t = strings.ReplaceAll(strings.TrimSpace(t), "\n", " ")
	r := []rune(t)
	if len(r) > MaxTitleRunes {
		return string(r[:MaxTitleRunes])
	}
	return t
}

// siblingPosition 计算插入位置。
func (s *Service) siblingPosition(spaceID, parentID, afterID string, first bool) (string, error) {
	sibs := s.cat.Children(spaceID, parentID)
	if len(sibs) == 0 {
		return fracidx.Between("", "")
	}
	if first {
		return fracidx.Between("", sibs[0].Position)
	}
	if afterID == "" {
		return fracidx.Between(sibs[len(sibs)-1].Position, "")
	}
	for i, sb := range sibs {
		if sb.ID == afterID {
			next := ""
			if i+1 < len(sibs) {
				next = sibs[i+1].Position
			}
			if next == sb.Position {
				next = ""
			}
			k, err := fracidx.Between(sb.Position, next)
			if err != nil {
				return fracidx.Between(sibs[len(sibs)-1].Position, "")
			}
			return k, nil
		}
	}
	return fracidx.Between(sibs[len(sibs)-1].Position, "")
}

// insertBlocks 追加块（按给定顺序生成位置，忽略传入的 position）。
func (s *Service) insertBlocks(tx *store.Tx, actor, pageID string, blocks []BlockDoc) error {
	keys, err := fracidx.NBetween("", "", len(blocks))
	if err != nil {
		return err
	}
	now := model.Now()
	for i, b := range blocks {
		bid := b.ID
		if bid == "" {
			bid = ids.New()
		}
		data, typ, err := NormalizeBlockData(bid, b.Type, b.Data)
		if err != nil {
			return err
		}
		if err := tx.Put("blocks", model.Block{ID: bid, PageID: pageID, Position: keys[i], Type: typ, Data: data, Version: 1,
			CreatedAt: now, CreatedBy: actor, UpdatedAt: now, UpdatedBy: actor}); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeBlockData 校验块 JSON：必须是对象，type 与 attrs.id 一致；返回规整后的 JSON 与类型。
func NormalizeBlockData(id, typ string, data json.RawMessage) (string, string, error) {
	if len(data) > MaxBlockBytes {
		return "", "", fmt.Errorf("块太大（超过 %d MB）", MaxBlockBytes>>20)
	}
	var node map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&node); err != nil || node == nil {
		return "", "", errors.New("块内容不是合法的 JSON 对象")
	}
	t, _ := node["type"].(string)
	if t == "" {
		t = typ
	}
	if t == "" || len(t) > 64 {
		return "", "", errors.New("块缺少类型")
	}
	node["type"] = t
	attrs, _ := node["attrs"].(map[string]any)
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["id"] = id
	node["attrs"] = attrs
	b, err := json.Marshal(node)
	if err != nil {
		return "", "", err
	}
	return string(b), t, nil
}

// ---------- 元信息 ----------

// Patch 修改页面元信息（nil 表示不改）。
type Patch struct {
	Title      *string          `json:"title"`
	Icon       *string          `json:"icon"`
	Cover      *string          `json:"cover"`
	Flags      *model.PageFlags `json:"flags"`
	Props      json.RawMessage  `json:"props"`
	IsTemplate *bool            `json:"is_template"`
}

// Update 修改元信息。
func (s *Service) Update(ctx context.Context, actor, id string, patch Patch) (*model.Page, error) {
	var out *model.Page
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		p, err := s.UpdateTx(tx, actor, id, patch)
		out = p
		return err
	})
	return out, err
}

// UpdateTx 在事务里修改元信息。
func (s *Service) UpdateTx(tx *store.Tx, actor, id string, patch Patch) (*model.Page, error) {
	var p model.Page
	if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", id); err != nil {
		return nil, ErrNotFound
	}
	flags := p.GetFlags()
	if flags.Locked && (patch.Flags == nil || patch.Flags.Locked) && (patch.Title != nil || patch.Props != nil || patch.Icon != nil || patch.Cover != nil) {
		return nil, ErrLocked
	}
	if patch.Title != nil {
		p.Title = clampTitle(*patch.Title)
	}
	if patch.Icon != nil {
		p.Icon = *patch.Icon
	}
	if patch.Cover != nil {
		p.Cover = *patch.Cover
	}
	if patch.Flags != nil {
		p.SetFlags(*patch.Flags)
	}
	if patch.Props != nil {
		p.Props = string(patch.Props)
	}
	if patch.IsTemplate != nil {
		p.IsTemplate = *patch.IsTemplate
	}
	p.UpdatedAt = model.Now()
	p.UpdatedBy = actor
	if err := tx.Put("pages", &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ---------- 移动 ----------

// MoveInput 移动参数。
type MoveInput struct {
	SpaceID  string `json:"space_id"`  // 目标空间（空表示不变）
	ParentID string `json:"parent_id"` // 目标上级（空表示空间根）
	AfterID  string `json:"after_id"`
	First    bool   `json:"first"`
}

// Move 移动页面（可跨空间，子孙一起移动）。
func (s *Service) Move(ctx context.Context, actor, id string, in MoveInput) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var p model.Page
		if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", id); err != nil {
			return ErrNotFound
		}
		if p.DatabaseID != nil && in.ParentID != *p.DatabaseID {
			return errors.New("数据库行只能在所属数据库里调整顺序")
		}
		target := in.SpaceID
		if target == "" {
			target = p.SpaceID
		}
		if in.ParentID != "" {
			if in.ParentID == id || s.cat.IsDescendant(in.ParentID, id) {
				return ErrCycle
			}
			parent, ok := s.cat.Page(in.ParentID)
			if !ok || parent.TrashedAt != 0 {
				return errors.New("目标上级页面不存在")
			}
			target = parent.SpaceID
		}
		pos, err := s.siblingPosition(target, in.ParentID, in.AfterID, in.First)
		if err != nil {
			return err
		}
		now := model.Now()
		p.ParentID = model.StrPtr(in.ParentID)
		p.Position = pos
		p.UpdatedAt, p.UpdatedBy = now, actor
		moveSpace := target != p.SpaceID
		p.SpaceID = target
		if err := tx.Put("pages", &p); err != nil {
			return err
		}
		if moveSpace {
			for _, did := range s.cat.Descendants(id) {
				var d model.Page
				if err := tx.Get(&d, "SELECT * FROM pages WHERE id = ?", did); err != nil {
					continue
				}
				d.SpaceID = target
				if err := tx.Put("pages", &d); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return err
}

// ---------- 回收站 ----------

// Trash 把页面及其子孙移入回收站。
func (s *Service) Trash(ctx context.Context, actor, id string) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error { return s.TrashTx(tx, actor, id) })
	return err
}

// TrashTx 事务内移入回收站。
func (s *Service) TrashTx(tx *store.Tx, actor, id string) error {
	now := model.Now()
	all := append([]string{id}, s.cat.Descendants(id)...)
	for _, pid := range all {
		var p model.Page
		if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", pid); err != nil {
			if pid == id {
				return ErrNotFound
			}
			continue
		}
		if p.TrashedAt != nil {
			continue // 之前单独删过的子页面保留原来的回收站归属
		}
		p.TrashedAt = &now
		p.TrashedBy = &actor
		p.TrashRoot = &id
		if err := tx.Put("pages", &p); err != nil {
			return err
		}
	}
	return nil
}

// Restore 从回收站恢复（同一次删除的子孙一起恢复）；原上级已不存在时恢复到空间根。
func (s *Service) Restore(ctx context.Context, actor, id string) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		var root model.Page
		if err := tx.Get(&root, "SELECT * FROM pages WHERE id = ?", id); err != nil {
			return ErrNotFound
		}
		if root.TrashedAt == nil {
			return nil
		}
		if root.ParentID != nil {
			parent, ok := s.cat.Page(*root.ParentID)
			if !ok || parent.TrashedAt != 0 {
				if root.DatabaseID != nil {
					return errors.New("所属数据库在回收站里，请先恢复数据库")
				}
				root.ParentID = nil
				pos, err := s.siblingPosition(root.SpaceID, "", "", false)
				if err != nil {
					return err
				}
				root.Position = pos
			}
		}
		var members []model.Page
		if err := tx.Select(&members, "SELECT * FROM pages WHERE trash_root = ?", id); err != nil {
			return err
		}
		members = append(members, root)
		for i := range members {
			m := members[i]
			if m.ID == id {
				m = root
			}
			m.TrashedAt, m.TrashedBy, m.TrashRoot = nil, nil, nil
			m.UpdatedAt, m.UpdatedBy = model.Now(), actor
			if err := tx.Put("pages", &m); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// Purge 彻底删除页面及其子孙（含块、版本、评论、分享、收藏）。不可恢复。
func (s *Service) Purge(ctx context.Context, actor, id string) error {
	all := append([]string{id}, s.cat.Descendants(id)...)
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		for _, pid := range all {
			if err := s.purgeOne(tx, pid); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && s.OnPurge != nil {
		s.OnPurge(all)
	}
	return err
}

func (s *Service) purgeOne(tx *store.Tx, pid string) error {
	type idRow struct {
		ID string `db:"id"`
	}
	del := func(table, where string) error {
		var rows []idRow
		if err := tx.Select(&rows, "SELECT id FROM "+table+" WHERE "+where, pid); err != nil {
			return err
		}
		for _, r := range rows {
			if err := tx.DeleteHint(table, r.ID, store.Row{"page_id": pid}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range [][2]string{
		{"blocks", "page_id = ?"}, {"page_versions", "page_id = ?"}, {"comments", "page_id = ?"},
		{"comment_threads", "page_id = ?"}, {"page_shares", "page_id = ?"}, {"favorites", "page_id = ?"},
		{"db_views", "database_id = ?"},
	} {
		if err := del(d[0], d[1]); err != nil {
			return err
		}
	}
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM databases WHERE id = ?", pid).Scan(&n); err == nil && n > 0 {
		if err := tx.Delete("databases", pid); err != nil {
			return err
		}
	}
	return tx.Delete("pages", pid)
}

// PurgeExpiredTrash 清理超过保留期的回收站页面。
func (s *Service) PurgeExpiredTrash(ctx context.Context, retain time.Duration) (int, error) {
	cut := time.Now().Add(-retain).UnixMilli()
	var roots []model.Page
	if err := s.st.Select(ctx, &roots, "SELECT * FROM pages WHERE trashed_at IS NOT NULL AND trashed_at < ? AND trash_root = id", cut); err != nil {
		return 0, err
	}
	for _, r := range roots {
		if err := s.Purge(ctx, "system", r.ID); err != nil {
			return 0, err
		}
	}
	return len(roots), nil
}

// ---------- 复制 ----------

// Duplicate 复制页面（可含子孙），返回新页面 id。数据库的结构与视图一并复制。
func (s *Service) Duplicate(ctx context.Context, actor, id string, withChildren bool, titleSuffix string) (string, error) {
	var newRoot string
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		mapping := map[string]string{}
		var copyPage func(srcID, parentID string, afterID string, isRoot bool) error
		copyPage = func(srcID, parentID string, afterID string, isRoot bool) error {
			var p model.Page
			if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", srcID); err != nil {
				return ErrNotFound
			}
			if p.TrashedAt != nil && !isRoot {
				return nil
			}
			nid := ids.New()
			mapping[srcID] = nid
			np := p
			np.ID = nid
			np.ParentID = model.StrPtr(parentID)
			if p.DatabaseID != nil {
				np.DatabaseID = model.StrPtr(parentID)
			}
			if isRoot {
				np.Title = clampTitle(p.Title + titleSuffix)
				pos, err := s.siblingPosition(p.SpaceID, parentID, afterID, false)
				if err != nil {
					return err
				}
				np.Position = pos
			}
			now := model.Now()
			np.CreatedAt, np.CreatedBy, np.UpdatedAt, np.UpdatedBy = now, actor, now, actor
			np.TrashedAt, np.TrashedBy, np.TrashRoot = nil, nil, nil
			np.ContentRev = 0
			if err := tx.Put("pages", &np); err != nil {
				return err
			}
			var bs []model.Block
			if err := tx.Select(&bs, "SELECT * FROM blocks WHERE page_id = ?", srcID); err != nil {
				return err
			}
			for _, b := range bs {
				nb := b
				nb.ID = ids.New()
				nb.PageID = nid
				nb.Version = 1
				nb.CreatedAt, nb.CreatedBy, nb.UpdatedAt, nb.UpdatedBy = now, actor, now, actor
				data, _, err := NormalizeBlockData(nb.ID, nb.Type, json.RawMessage(b.Data))
				if err != nil {
					return err
				}
				nb.Data = data
				if err := tx.Put("blocks", nb); err != nil {
					return err
				}
			}
			if p.Kind == model.KindDatabase {
				var d model.Database
				if err := tx.Get(&d, "SELECT * FROM databases WHERE id = ?", srcID); err == nil {
					d.ID = nid
					d.CreatedAt, d.UpdatedAt = now, now
					if err := tx.Put("databases", d); err != nil {
						return err
					}
				}
				var vs []model.DBView
				if err := tx.Select(&vs, "SELECT * FROM db_views WHERE database_id = ?", srcID); err != nil {
					return err
				}
				for _, v := range vs {
					v.ID = ids.New()
					v.DatabaseID = nid
					v.CreatedAt, v.UpdatedAt, v.CreatedBy = now, now, actor
					if err := tx.Put("db_views", v); err != nil {
						return err
					}
				}
			}
			if withChildren || p.Kind == model.KindDatabase {
				for _, ch := range s.cat.Children(p.SpaceID, srcID) {
					if err := copyPage(ch.ID, nid, "", false); err != nil {
						return err
					}
				}
			}
			return nil
		}
		src, ok := s.cat.Page(id)
		if !ok {
			return ErrNotFound
		}
		if err := copyPage(id, src.ParentID, id, true); err != nil {
			return err
		}
		newRoot = mapping[id]
		return nil
	})
	return newRoot, err
}
