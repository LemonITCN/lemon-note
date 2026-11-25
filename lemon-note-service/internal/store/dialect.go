package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Engine 是数据库引擎名。
type Engine string

const (
	SQLite   Engine = "sqlite"
	MySQL    Engine = "mysql"
	Postgres Engine = "postgres"
)

func (e Engine) Valid() bool { return e == SQLite || e == MySQL || e == Postgres }

type dialect interface {
	engine() Engine
	colType(c Column) string
	createTable(t *Table) string
	upsert(t *Table, cols []string) string
	rebind(q string) string
	hasColumn(ctx context.Context, q queryer, table, col string) (bool, error)
	hasIndex(ctx context.Context, q queryer, table, index string) (bool, error)
	createIndex(t *Table, ix Index) string
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func quoteIdent(e Engine, s string) string {
	if e == MySQL {
		return "`" + s + "`"
	}
	return `"` + s + `"`
}

func columnsDDL(d dialect, t *Table) string {
	var parts []string
	for _, c := range t.Columns {
		def := quoteIdent(d.engine(), c.Name) + " " + d.colType(c)
		if c.Name == "id" {
			def += " PRIMARY KEY"
		} else if !c.Nullable {
			def += " NOT NULL" + defaultFor(d.engine(), c)
		}
		parts = append(parts, def)
	}
	return strings.Join(parts, ", ")
}

func defaultFor(e Engine, c Column) string {
	switch c.Type {
	case TInt, TBool, TReal:
		return " DEFAULT 0"
	case TText, TBlob:
		if e == MySQL {
			return "" // MySQL 不允许 TEXT/BLOB 带字面默认值，写入时总是给全列
		}
		if c.Type == TBlob {
			return ""
		}
		return " DEFAULT ''"
	default:
		return " DEFAULT ''"
	}
}

// ---------- SQLite ----------

type sqliteDialect struct{}

func (sqliteDialect) engine() Engine { return SQLite }
func (sqliteDialect) colType(c Column) string {
	switch c.Type {
	case TInt, TBool:
		return "INTEGER"
	case TReal:
		return "REAL"
	case TBlob:
		return "BLOB"
	default:
		return "TEXT"
	}
}
func (d sqliteDialect) createTable(t *Table) string {
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", quoteIdent(SQLite, t.Name), columnsDDL(d, t))
}
func (sqliteDialect) upsert(t *Table, cols []string) string { return upsertOnConflict(SQLite, t, cols) }
func (sqliteDialect) rebind(q string) string                { return q }
func (sqliteDialect) hasColumn(ctx context.Context, q queryer, table, col string) (bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if n == col {
			return true, nil
		}
	}
	return false, rows.Err()
}
func (sqliteDialect) hasIndex(ctx context.Context, q queryer, table, index string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&n)
	return n > 0, err
}
func (sqliteDialect) createIndex(t *Table, ix Index) string { return createIndexIfNotExists(SQLite, t, ix) }

// ---------- MySQL / MariaDB ----------

type mysqlDialect struct{}

func (mysqlDialect) engine() Engine { return MySQL }
func (mysqlDialect) colType(c Column) string {
	switch c.Type {
	case TID:
		return "VARCHAR(26)"
	case TKey:
		return "VARCHAR(191)"
	case TKeyBin:
		return "VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin"
	case TText:
		return "LONGTEXT"
	case TInt:
		return "BIGINT"
	case TBool:
		return "SMALLINT"
	case TReal:
		return "DOUBLE"
	case TBlob:
		return "LONGBLOB"
	}
	return "LONGTEXT"
}
func (d mysqlDialect) createTable(t *Table) string {
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
		quoteIdent(MySQL, t.Name), columnsDDL(d, t))
}
func (mysqlDialect) upsert(t *Table, cols []string) string {
	var names, marks, sets []string
	for _, c := range cols {
		q := quoteIdent(MySQL, c)
		names = append(names, q)
		marks = append(marks, "?")
		if c != "id" {
			sets = append(sets, q+"=VALUES("+q+")")
		}
	}
	if len(sets) == 0 {
		sets = append(sets, "`id`=`id`")
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s",
		quoteIdent(MySQL, t.Name), strings.Join(names, ","), strings.Join(marks, ","), strings.Join(sets, ","))
}
func (mysqlDialect) rebind(q string) string { return q }
func (mysqlDialect) hasColumn(ctx context.Context, q queryer, table, col string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?", table, col).Scan(&n)
	return n > 0, err
}
func (mysqlDialect) hasIndex(ctx context.Context, q queryer, table, index string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?", table, index).Scan(&n)
	return n > 0, err
}
func (mysqlDialect) createIndex(t *Table, ix Index) string {
	var cs []string
	for _, c := range ix.Columns {
		cs = append(cs, quoteIdent(MySQL, c))
	}
	u := ""
	if ix.Unique {
		u = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", u, quoteIdent(MySQL, ix.Name), quoteIdent(MySQL, t.Name), strings.Join(cs, ","))
}

// ---------- PostgreSQL ----------

type pgDialect struct{}

func (pgDialect) engine() Engine { return Postgres }
func (pgDialect) colType(c Column) string {
	switch c.Type {
	case TID:
		return `VARCHAR(26) COLLATE "C"`
	case TKey:
		return `VARCHAR(191) COLLATE "C"`
	case TKeyBin:
		return `VARCHAR(191) COLLATE "C"`
	case TText:
		return `TEXT COLLATE "C"`
	case TInt:
		return "BIGINT"
	case TBool:
		return "SMALLINT"
	case TReal:
		return "DOUBLE PRECISION"
	case TBlob:
		return "BYTEA"
	}
	return "TEXT"
}
func (d pgDialect) createTable(t *Table) string {
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", quoteIdent(Postgres, t.Name), columnsDDL(d, t))
}
func (pgDialect) upsert(t *Table, cols []string) string { return upsertOnConflict(Postgres, t, cols) }
func (pgDialect) rebind(q string) string {
	var b strings.Builder
	n := 0
	inStr := false
	for i := 0; i < len(q); i++ {
		ch := q[i]
		if ch == '\'' {
			inStr = !inStr
		}
		if ch == '?' && !inStr {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
func (pgDialect) hasColumn(ctx context.Context, q queryer, table, col string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2", table, col).Scan(&n)
	return n > 0, err
}
func (pgDialect) hasIndex(ctx context.Context, q queryer, table, index string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1", index).Scan(&n)
	return n > 0, err
}
func (pgDialect) createIndex(t *Table, ix Index) string { return createIndexIfNotExists(Postgres, t, ix) }

func upsertOnConflict(e Engine, t *Table, cols []string) string {
	var names, marks, sets []string
	for _, c := range cols {
		q := quoteIdent(e, c)
		names = append(names, q)
		marks = append(marks, "?")
		if c != "id" {
			sets = append(sets, q+"=excluded."+q)
		}
	}
	action := "DO NOTHING"
	if len(sets) > 0 {
		action = "DO UPDATE SET " + strings.Join(sets, ",")
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) %s",
		quoteIdent(e, t.Name), strings.Join(names, ","), strings.Join(marks, ","), quoteIdent(e, "id"), action)
}

func createIndexIfNotExists(e Engine, t *Table, ix Index) string {
	var cs []string
	for _, c := range ix.Columns {
		cs = append(cs, quoteIdent(e, c))
	}
	u := ""
	if ix.Unique {
		u = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)", u, quoteIdent(e, ix.Name), quoteIdent(e, t.Name), strings.Join(cs, ","))
}

func dialectFor(e Engine) dialect {
	switch e {
	case MySQL:
		return mysqlDialect{}
	case Postgres:
		return pgDialect{}
	default:
		return sqliteDialect{}
	}
}
