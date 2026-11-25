package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/jmoiron/sqlx"
)

// localTables 快照里不保留内容的本机表。
var localTables = []string{"sessions", "login_attempts", "oauth_clients", "oauth_codes", "oauth_tokens", "recent_pages", "import_jobs", "_outbox"}

// ExportSnapshot 把当前数据一致地导出成一个 SQLite 文件 dst（不能已存在），返回快照包含到的序号。
func (s *Store) ExportSnapshot(ctx context.Context, dst string) (int64, error) {
	if _, err := os.Stat(dst); err == nil {
		return 0, fmt.Errorf("快照目标已存在: %s", dst)
	}
	if s.Engine == SQLite {
		return s.exportSQLite(ctx, dst)
	}
	return s.exportGeneric(ctx, dst)
}

func (s *Store) exportSQLite(ctx context.Context, dst string) (int64, error) {
	// 独立连接做 VACUUM INTO：读的是一致性快照，不阻塞写入
	db, err := sql.Open("sqlite", sqliteDSN(s.opts.WorkPath, false))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		os.Remove(dst)
		return 0, fmt.Errorf("导出快照失败: %w", err)
	}
	db.Close()
	return finalizeSnapshotFile(ctx, dst)
}

// finalizeSnapshotFile 清空快照里的本机表、改为非 WAL 单文件，并返回其中的序号。
func finalizeSnapshotFile(ctx context.Context, path string) (int64, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(DELETE)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, t := range localTables {
		if _, err := db.ExecContext(ctx, "DELETE FROM "+quoteIdent(SQLite, t)); err != nil {
			return 0, err
		}
	}
	var seq int64
	if err := db.QueryRowContext(ctx, "SELECT seq FROM _seq WHERE id = 1").Scan(&seq); err != nil {
		return 0, fmt.Errorf("快照缺少序号: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return 0, err
	}
	if err := db.Close(); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return seq, f.Sync()
}

// snapshotTables 是外部引擎导出快照时要复制的表。
func snapshotTables() []*Table {
	out := JournaledTables()
	for _, n := range []string{"meta", "_seq"} {
		t, _ := TableByName(n)
		out = append(out, t)
	}
	return out
}

func createSQLiteSchema(ctx context.Context, db *sql.DB) error {
	d := sqliteDialect{}
	for _, t := range Tables {
		if _, err := db.ExecContext(ctx, d.createTable(t)); err != nil {
			return err
		}
		for _, ix := range t.Indexes {
			if _, err := db.ExecContext(ctx, d.createIndex(t, ix)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) exportGeneric(ctx context.Context, dst string) (int64, error) {
	out, err := sql.Open("sqlite", "file:"+dst+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(dst)
		}
	}()
	out.SetMaxOpenConns(1)
	if err := createSQLiteSchema(ctx, out); err != nil {
		return 0, err
	}
	conn, err := s.W.Connx(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	switch s.Engine {
	case MySQL:
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
			return 0, err
		}
		if _, err := conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
			return 0, err
		}
	case Postgres:
		if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
			return 0, err
		}
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var seq int64
	if err := conn.QueryRowContext(ctx, "SELECT seq FROM _seq WHERE id = 1").Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := out.ExecContext(ctx, "DELETE FROM _seq"); err != nil {
		return 0, err
	}
	for _, t := range snapshotTables() {
		if err := copyTable(ctx, conn, s.Engine, out, SQLite, t); err != nil {
			return 0, fmt.Errorf("导出表 %s 失败: %w", t.Name, err)
		}
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	n, err := finalizeSnapshotFile(ctx, dst)
	if err != nil {
		return 0, err
	}
	if n != seq {
		return 0, fmt.Errorf("快照序号不一致 %d != %d", n, seq)
	}
	ok = true
	return seq, nil
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// copyTable 把 src 的整张表复制进 dst（目标表需已存在）。
func copyTable(ctx context.Context, src rowsQuerier, srcEngine Engine, dst execer, dstEngine Engine, t *Table) error {
	var names []string
	for _, c := range t.Columns {
		names = append(names, quoteIdent(srcEngine, c.Name))
	}
	rows, err := src.QueryContext(ctx, "SELECT "+strings.Join(names, ",")+" FROM "+quoteIdent(srcEngine, t.Name))
	if err != nil {
		return err
	}
	defer rows.Close()
	var colNames []string
	for _, c := range t.Columns {
		colNames = append(colNames, c.Name)
	}
	ins := dialectFor(dstEngine).rebind(dialectFor(dstEngine).upsert(t, colNames))
	for rows.Next() {
		vals := make([]any, len(t.Columns))
		ptrs := make([]any, len(t.Columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, c := range t.Columns {
			vals[i] = coerceScanned(c, vals[i])
		}
		if _, err := dst.ExecContext(ctx, ins, vals...); err != nil {
			return err
		}
	}
	return rows.Err()
}

// coerceScanned 把驱动扫描出来的值规整成目标列能接受的类型。
func coerceScanned(c Column, v any) any {
	if v == nil {
		return nil
	}
	switch c.Type {
	case TBlob:
		switch x := v.(type) {
		case string:
			return []byte(x)
		}
		return v
	case TInt, TBool:
		switch x := v.(type) {
		case []byte:
			var n int64
			fmt.Sscan(string(x), &n)
			return n
		case int32:
			return int64(x)
		case int16:
			return int64(x)
		}
		return v
	case TReal:
		if b, ok := v.([]byte); ok {
			var f float64
			fmt.Sscan(string(b), &f)
			return f
		}
		return v
	default:
		if b, ok := v.([]byte); ok {
			return string(b)
		}
		return v
	}
}

// ImportSnapshot 清空当前库的业务表，再从 SQLite 快照文件导入（用于 MySQL / PostgreSQL 的重建）。
func (s *Store) ImportSnapshot(ctx context.Context, snapshotPath string) (int64, error) {
	src, err := sqlx.Open("sqlite", "file:"+snapshotPath+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return 0, err
	}
	defer src.Close()
	tx, err := s.W.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, t := range Tables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+quoteIdent(s.Engine, t.Name)); err != nil {
			return 0, err
		}
	}
	for _, t := range snapshotTables() {
		if err := copyTable(ctx, src, SQLite, tx, s.Engine, t); err != nil {
			return 0, fmt.Errorf("导入表 %s 失败: %w", t.Name, err)
		}
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, "SELECT seq FROM _seq WHERE id = 1").Scan(&seq); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// SnapshotSeq 读快照文件里的序号与元数据。
func SnapshotInfo(ctx context.Context, path string) (seq int64, meta map[string]string, err error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	if err := db.QueryRowContext(ctx, "SELECT seq FROM _seq WHERE id = 1").Scan(&seq); err != nil {
		return 0, nil, err
	}
	meta = map[string]string{}
	rows, err := db.QueryContext(ctx, "SELECT id, value FROM meta")
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return 0, nil, err
		}
		meta[k] = v
	}
	return seq, meta, rows.Err()
}
