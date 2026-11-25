// Package search 维护全文索引：独立的 SQLite FTS5（trigram 分词，中英文都能搜）放在缓存目录，可随时删除重建。
package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

const indexVersion = "2"

// Index 是全文索引。
type Index struct {
	db    *sql.DB
	st    *store.Store
	log   *slog.Logger
	mu    sync.Mutex
	dirty map[string]bool
	stop  chan struct{}
	wg    sync.WaitGroup
	// Text 把块 JSON 提取成纯文本（由增强 Markdown 模块提供更好的实现，默认按 JSON 遍历）
	Text func(data string) string
}

// Open 打开（必要时重建）索引。
func Open(ctx context.Context, cacheDir string, st *store.Store, log *slog.Logger) (*Index, error) {
	path := filepath.Join(cacheDir, "search.db")
	ix := &Index{st: st, log: log, dirty: map[string]bool{}, stop: make(chan struct{}), Text: PlainFromJSON}
	open := func() error {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)")
		if err != nil {
			return err
		}
		db.SetMaxOpenConns(1)
		ix.db = db
		_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT);
			CREATE VIRTUAL TABLE IF NOT EXISTS docs USING fts5(page_id UNINDEXED, space_id UNINDEXED, title, body, tokenize='trigram');`)
		return err
	}
	if err := open(); err != nil {
		os.Remove(path)
		if err := open(); err != nil {
			return nil, err
		}
	}
	var v string
	_ = ix.db.QueryRowContext(ctx, "SELECT v FROM meta WHERE k='version'").Scan(&v)
	if v != indexVersion {
		ix.db.Close()
		for _, s := range []string{"", "-wal", "-shm"} {
			os.Remove(path + s)
		}
		if err := open(); err != nil {
			return nil, err
		}
		ix.db.ExecContext(ctx, "INSERT OR REPLACE INTO meta (k, v) VALUES ('version', ?)", indexVersion)
		ix.markAll(ctx)
	}
	st.Subscribe(ix.onCommit)
	ix.wg.Add(1)
	go ix.loop()
	return ix, nil
}

func (ix *Index) markAll(ctx context.Context) {
	var ids []string
	_ = ix.st.Select(ctx, &ids, "SELECT id FROM pages")
	ix.mu.Lock()
	for _, id := range ids {
		ix.dirty[id] = true
	}
	ix.mu.Unlock()
}

func (ix *Index) onCommit(c store.Commit) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, op := range c.Ops {
		switch op.T {
		case "pages":
			ix.dirty[op.ID] = true
		case "blocks":
			if pid, _ := op.Row["page_id"].(string); pid != "" {
				ix.dirty[pid] = true
			}
		}
	}
}

func (ix *Index) loop() {
	defer ix.wg.Done()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ix.stop:
			return
		case <-t.C:
			ix.flush(context.Background())
		}
	}
}

func (ix *Index) flush(ctx context.Context) {
	ix.mu.Lock()
	if len(ix.dirty) == 0 {
		ix.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(ix.dirty))
	for id := range ix.dirty {
		ids = append(ids, id)
		if len(ids) >= 500 {
			break
		}
	}
	for _, id := range ids {
		delete(ix.dirty, id)
	}
	ix.mu.Unlock()
	for _, id := range ids {
		if err := ix.reindex(ctx, id); err != nil {
			ix.log.Warn("更新全文索引失败", "page", id, "err", err)
		}
	}
}

func (ix *Index) reindex(ctx context.Context, pageID string) error {
	var p model.Page
	err := ix.st.Get(ctx, &p, "SELECT * FROM pages WHERE id = ?", pageID)
	if _, derr := ix.db.ExecContext(ctx, "DELETE FROM docs WHERE page_id = ?", pageID); derr != nil {
		return derr
	}
	if err != nil || p.TrashedAt != nil {
		return nil
	}
	var bs []model.Block
	if err := ix.st.Select(ctx, &bs, "SELECT * FROM blocks WHERE page_id = ?", pageID); err != nil {
		return err
	}
	var b strings.Builder
	for _, bl := range bs {
		b.WriteString(ix.Text(bl.Data))
		b.WriteByte('\n')
	}
	if p.Props != "" && p.Props != "{}" {
		b.WriteString(PlainFromJSON(p.Props))
	}
	_, err = ix.db.ExecContext(ctx, "INSERT INTO docs (page_id, space_id, title, body) VALUES (?, ?, ?, ?)", p.ID, p.SpaceID, p.Title, b.String())
	return err
}

// Hit 是一个搜索结果。
type Hit struct {
	PageID  string `json:"page_id"`
	Snippet string `json:"snippet"`
}

// Search 全文搜索，返回按相关度排序的页面与片段（片段已做 HTML 转义，命中部分包在 <mark> 里）。
func (ix *Index) Search(ctx context.Context, q string, spaceIDs []string, limit int) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	args := []any{}
	spaceFilter := ""
	if len(spaceIDs) > 0 {
		spaceFilter = " AND space_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(spaceIDs)), ",") + ")"
	}
	if len([]rune(q)) >= 3 {
		args = append(args, ftsQuote(q))
		for _, s := range spaceIDs {
			args = append(args, s)
		}
		args = append(args, limit)
		rows, err = ix.db.QueryContext(ctx, "SELECT page_id, snippet(docs, 3, char(2), char(3), '…', 24), title FROM docs WHERE docs MATCH ?"+spaceFilter+" ORDER BY bm25(docs, 0, 0, 10, 1) LIMIT ?", args...)
	} else {
		like := "%" + escapeLike(q) + "%"
		args = append(args, q, like, like)
		for _, s := range spaceIDs {
			args = append(args, s)
		}
		args = append(args, limit)
		rows, err = ix.db.QueryContext(ctx, "SELECT page_id, substr(body, max(1, instr(body, ?) - 20), 80), title FROM docs WHERE (title LIKE ? ESCAPE '\\' OR body LIKE ? ESCAPE '\\')"+spaceFilter+" LIMIT ?", args...)
	}
	if err != nil {
		return nil, fmt.Errorf("搜索失败: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var snip, title string
		if err := rows.Scan(&h.PageID, &snip, &title); err != nil {
			return nil, err
		}
		h.Snippet = renderSnippet(snip, q)
		out = append(out, h)
	}
	return out, rows.Err()
}

func ftsQuote(q string) string { return `"` + strings.ReplaceAll(q, `"`, `""`) + `"` }

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func renderSnippet(s, q string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if strings.ContainsRune(s, '\x02') {
		s = html.EscapeString(s)
		s = strings.ReplaceAll(s, "\x02", "<mark>")
		return strings.ReplaceAll(s, "\x03", "</mark>")
	}
	esc := html.EscapeString(s)
	qe := html.EscapeString(q)
	return strings.ReplaceAll(esc, qe, "<mark>"+qe+"</mark>")
}

// Close 停止索引。
func (ix *Index) Close() {
	close(ix.stop)
	ix.wg.Wait()
	ix.db.Close()
}

// PlainFromJSON 粗略提取 JSON 里的文字（text、code、latex、title 等字段）。
func PlainFromJSON(data string) string {
	var v any
	if json.Unmarshal([]byte(data), &v) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				switch k {
				case "text", "code", "latex", "title", "label", "description", "name", "alt":
					if s, ok := val.(string); ok {
						b.WriteString(s)
						b.WriteByte(' ')
						continue
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case string:
			if len(x) < 400 && !strings.HasPrefix(x, "lemon://") && !strings.HasPrefix(x, "http") {
				b.WriteString(x)
				b.WriteByte(' ')
			}
		}
	}
	walk(v)
	return b.String()
}
