package app

import (
	"context"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// pruneHistory 删除超过保留期的历史版本（每个页面至少保留最近 5 个）。
func pruneHistory(ctx context.Context, s *Services, retain time.Duration) error {
	cut := time.Now().Add(-retain).UnixMilli()
	type row struct {
		ID     string `db:"id"`
		PageID string `db:"page_id"`
	}
	var old []row
	if err := s.Inst.Store.Select(ctx, &old, "SELECT id, page_id FROM page_versions WHERE created_at < ? ORDER BY created_at LIMIT 5000", cut); err != nil {
		return err
	}
	for _, r := range old {
		var n int
		if err := s.Inst.Store.QueryRow(ctx, "SELECT count(*) FROM page_versions WHERE page_id = ?", r.PageID).Scan(&n); err != nil || n <= 5 {
			continue
		}
		if _, err := s.Inst.Store.Write(ctx, "system", func(tx *store.Tx) error {
			return tx.DeleteHint("page_versions", r.ID, store.Row{"page_id": r.PageID})
		}); err != nil {
			return err
		}
	}
	return nil
}
