package search

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func TestSearchChineseAndShort(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: filepath.Join(dir, "w.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ix, err := Open(ctx, dir, st, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	now := model.Now()
	_, err = st.Write(ctx, "u", func(tx *store.Tx) error {
		if err := tx.Put("pages", model.Page{ID: "p1", SpaceID: "s1", Kind: "page", Title: "读书笔记", Props: "{}", Position: "a0", Flags: "{}", CreatedAt: now, CreatedBy: "u", UpdatedAt: now, UpdatedBy: "u"}); err != nil {
			return err
		}
		return tx.Put("blocks", model.Block{ID: "b1", PageID: "p1", Position: "a0", Type: "paragraph", Data: `{"type":"paragraph","content":[{"type":"text","text":"三体是刘慈欣写的科幻小说 <script>"}]}`, Version: 1, CreatedAt: now, CreatedBy: "u", UpdatedAt: now, UpdatedBy: "u"})
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	ix.flush(ctx)
	for _, q := range []string{"刘慈欣", "三体", "科幻小说", "读书"} {
		hits, err := ix.Search(ctx, q, []string{"s1"}, 10)
		if err != nil {
			t.Fatal(q, err)
		}
		if len(hits) != 1 || hits[0].PageID != "p1" {
			t.Fatalf("%s 应命中 p1，得到 %v", q, hits)
		}
	}
	hits, _ := ix.Search(ctx, "刘慈欣", []string{"other"}, 10)
	if len(hits) != 0 {
		t.Fatal("空间过滤失效")
	}
	hits, _ = ix.Search(ctx, "科幻小说", nil, 10)
	if len(hits) == 0 || contains(hits[0].Snippet, "<script>") {
		t.Fatalf("片段没有转义: %v", hits)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
