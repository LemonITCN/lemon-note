package pages

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/klauspost/compress/zstd"
)

var (
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	zdec, _ = zstd.NewReader(nil)
)

type versionBlock struct {
	ID       string          `json:"id"`
	Position string          `json:"position"`
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
}

type versionData struct {
	Title  string          `json:"title"`
	Icon   string          `json:"icon"`
	Cover  string          `json:"cover"`
	Props  json.RawMessage `json:"props"`
	Flags  json.RawMessage `json:"flags"`
	Blocks []versionBlock  `json:"blocks"`
}

// SaveVersionTx 把页面当前状态存成历史版本。
func (s *Service) SaveVersionTx(tx *store.Tx, actor, pageID, reason string, meta map[string]any) (string, error) {
	var p model.Page
	if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", pageID); err != nil {
		return "", ErrNotFound
	}
	var bs []model.Block
	if err := tx.Select(&bs, "SELECT * FROM blocks WHERE page_id = ?", pageID); err != nil {
		return "", err
	}
	sortBlocks(bs)
	vd := versionData{Title: p.Title, Icon: p.Icon, Cover: p.Cover, Props: rawOr(p.Props, "{}"), Flags: rawOr(p.Flags, "{}")}
	for _, b := range bs {
		vd.Blocks = append(vd.Blocks, versionBlock{ID: b.ID, Position: b.Position, Type: b.Type, Data: json.RawMessage(b.Data)})
	}
	raw, err := json.Marshal(vd)
	if err != nil {
		return "", err
	}
	mb, _ := json.Marshal(meta)
	if meta == nil {
		mb = []byte("{}")
	}
	v := model.PageVersion{ID: ids.New(), PageID: pageID, Reason: reason, Title: p.Title, Data: zenc.EncodeAll(raw, nil),
		Size: int64(len(raw)), Meta: string(mb), CreatedAt: model.Now(), CreatedBy: actor}
	return v.ID, tx.Put("page_versions", v)
}

// SaveVersion 独立事务存版本。
func (s *Service) SaveVersion(ctx context.Context, actor, pageID, reason string, meta map[string]any) (string, error) {
	var id string
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		v, err := s.SaveVersionTx(tx, actor, pageID, reason, meta)
		id = v
		return err
	})
	return id, err
}

// VersionInfo 历史版本列表项。
type VersionInfo struct {
	ID        string `db:"id" json:"id"`
	Reason    string `db:"reason" json:"reason"`
	Title     string `db:"title" json:"title"`
	Size      int64  `db:"size" json:"size"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	CreatedBy string `db:"created_by" json:"created_by"`
}

// Versions 列出页面历史版本（新的在前）。
func (s *Service) Versions(ctx context.Context, pageID string, limit int) ([]VersionInfo, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var out []VersionInfo
	err := s.st.Select(ctx, &out, "SELECT id, reason, title, size, created_at, created_by FROM page_versions WHERE page_id = ? ORDER BY created_at DESC LIMIT ?", pageID, limit)
	return out, err
}

// VersionDoc 读取历史版本内容。
func (s *Service) VersionDoc(ctx context.Context, pageID, versionID string) (*PageDoc, error) {
	var v model.PageVersion
	if err := s.st.Get(ctx, &v, "SELECT * FROM page_versions WHERE id = ? AND page_id = ?", versionID, pageID); err != nil {
		return nil, errors.New("历史版本不存在")
	}
	vd, err := decodeVersion(v.Data)
	if err != nil {
		return nil, err
	}
	p := &model.Page{ID: pageID, Title: vd.Title, Icon: vd.Icon, Cover: vd.Cover, Props: string(vd.Props), Flags: string(vd.Flags)}
	d := &PageDoc{Page: p, Flags: p.GetFlags(), Props: vd.Props}
	for _, b := range vd.Blocks {
		d.Blocks = append(d.Blocks, BlockDoc{ID: b.ID, Type: b.Type, Position: b.Position, Data: b.Data})
	}
	return d, nil
}

// VersionJSON 返回历史版本解压后的原始 JSON（附件回收扫描引用用）。
func VersionJSON(data []byte) ([]byte, error) { return zdec.DecodeAll(data, nil) }

func decodeVersion(data []byte) (*versionData, error) {
	raw, err := zdec.DecodeAll(data, nil)
	if err != nil {
		return nil, errors.New("历史版本数据损坏")
	}
	var vd versionData
	if err := json.Unmarshal(raw, &vd); err != nil {
		return nil, err
	}
	return &vd, nil
}

// RestoreVersion 把页面恢复到某个历史版本（恢复前先把当前状态存一个版本）。
func (s *Service) RestoreVersion(ctx context.Context, actor, pageID, versionID string) error {
	_, err := s.st.Write(ctx, actor, func(tx *store.Tx) error {
		return s.RestoreVersionTx(tx, actor, pageID, versionID)
	})
	return err
}

// RestoreVersionTx 事务内恢复历史版本。
func (s *Service) RestoreVersionTx(tx *store.Tx, actor, pageID, versionID string) error {
	var v model.PageVersion
	if err := tx.Get(&v, "SELECT * FROM page_versions WHERE id = ? AND page_id = ?", versionID, pageID); err != nil {
		return errors.New("历史版本不存在")
	}
	vd, err := decodeVersion(v.Data)
	if err != nil {
		return err
	}
	if _, err := s.SaveVersionTx(tx, actor, pageID, "before_restore", map[string]any{"restore_to": versionID}); err != nil {
		return err
	}
	var p model.Page
	if err := tx.Get(&p, "SELECT * FROM pages WHERE id = ?", pageID); err != nil {
		return ErrNotFound
	}
	locked := p.GetFlags().Locked
	p.Title, p.Icon, p.Cover, p.Props = vd.Title, vd.Icon, vd.Cover, string(vd.Props)
	if !locked {
		p.Flags = string(vd.Flags)
	}
	p.UpdatedAt, p.UpdatedBy = model.Now(), actor
	if err := tx.Put("pages", &p); err != nil {
		return err
	}
	desired := make([]BlockDoc, 0, len(vd.Blocks))
	for _, b := range vd.Blocks {
		desired = append(desired, BlockDoc{ID: b.ID, Type: b.Type, Data: b.Data})
	}
	_, err = s.ReplaceBlocksTx(tx, actor, pageID, desired)
	return err
}
