// Package store 封装工作数据库：三种引擎、表结构迁移、带事务外箱的写入、快照导出与日志重放。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// Op 是一条行级变更。
type Op struct {
	T   string `json:"t"`
	O   string `json:"o"` // put | del
	ID  string `json:"id"`
	Row Row    `json:"row,omitempty"`
}

// Commit 是一次提交的全部变更。
type Commit struct {
	Seq   int64
	TS    int64
	Actor string
	Ops   []Op
}

// Options 打开工作库的参数。
type Options struct {
	Engine   Engine
	DSN      string // MySQL / PostgreSQL 连接串
	WorkPath string // SQLite 工作库文件路径
}

// Store 是工作数据库。
type Store struct {
	Engine Engine
	d      dialect
	W      *sqlx.DB // 写连接（SQLite 只有一条）
	R      *sqlx.DB // 读连接
	opts   Options

	subMu sync.RWMutex
	subs  []func(Commit)

	notify   chan struct{}
	readOnly atomic.Value // string：非空表示只读原因
}

var ErrReadOnly = errors.New("系统当前处于只读状态")

func sqliteDSN(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(15000)")
	q.Add("_pragma", "foreign_keys(0)")
	q.Add("_pragma", "temp_store(memory)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(FULL)")
		q.Add("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Open 打开工作库并确保表结构是最新的。
func Open(ctx context.Context, o Options) (*Store, error) {
	if !o.Engine.Valid() {
		return nil, fmt.Errorf("未知的数据库引擎 %q", o.Engine)
	}
	s := &Store{Engine: o.Engine, d: dialectFor(o.Engine), opts: o, notify: make(chan struct{}, 1)}
	s.readOnly.Store("")
	var err error
	switch o.Engine {
	case SQLite:
		s.W, err = sqlx.Open("sqlite", sqliteDSN(o.WorkPath, false))
		if err != nil {
			return nil, err
		}
		s.W.SetMaxOpenConns(1)
		s.W.SetMaxIdleConns(1)
		s.W.SetConnMaxLifetime(0)
		// 先用写连接把 WAL 模式落下去，读连接才能并发
		if err := s.W.PingContext(ctx); err != nil {
			s.W.Close()
			return nil, fmt.Errorf("打开工作库失败: %w", err)
		}
		s.R, err = sqlx.Open("sqlite", sqliteDSN(o.WorkPath, true))
		if err != nil {
			s.W.Close()
			return nil, err
		}
		s.R.SetMaxOpenConns(8)
	case MySQL:
		dsn, err := mysqlDSN(o.DSN)
		if err != nil {
			return nil, err
		}
		s.W, err = sqlx.Open("mysql", dsn)
		if err != nil {
			return nil, err
		}
		s.W.SetMaxOpenConns(16)
		s.W.SetConnMaxLifetime(30 * time.Minute)
		s.R = s.W
	case Postgres:
		s.W, err = sqlx.Open("pgx", o.DSN)
		if err != nil {
			return nil, err
		}
		s.W.SetMaxOpenConns(16)
		s.W.SetConnMaxLifetime(30 * time.Minute)
		s.R = s.W
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.W.PingContext(pctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := s.Migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func mysqlDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", errors.New("缺少 MySQL 连接串")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	extra := []string{}
	if !strings.Contains(dsn, "charset=") {
		extra = append(extra, "charset=utf8mb4")
	}
	if !strings.Contains(dsn, "collation=") {
		extra = append(extra, "collation=utf8mb4_bin")
	}
	if !strings.Contains(dsn, "timeout=") {
		extra = append(extra, "timeout=10s")
	}
	if len(extra) == 0 {
		return dsn, nil
	}
	return dsn + sep + strings.Join(extra, "&"), nil
}

// Close 关闭连接。
func (s *Store) Close() error {
	var err error
	if s.R != nil && s.R != s.W {
		err = s.R.Close()
	}
	if s.W != nil {
		if e := s.W.Close(); e != nil {
			err = e
		}
	}
	return err
}

// Migrate 建表、补列、补索引（只做加法）。
func (s *Store) Migrate(ctx context.Context) error {
	for _, t := range Tables {
		if _, err := s.W.ExecContext(ctx, s.d.createTable(t)); err != nil {
			return fmt.Errorf("建表 %s 失败: %w", t.Name, err)
		}
		for _, c := range t.Columns {
			ok, err := s.d.hasColumn(ctx, s.W, t.Name, c.Name)
			if err != nil {
				return err
			}
			if !ok {
				def := quoteIdent(s.Engine, c.Name) + " " + s.d.colType(c)
				if !c.Nullable {
					def += " NOT NULL" + defaultFor(s.Engine, c)
				}
				if _, err := s.W.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", quoteIdent(s.Engine, t.Name), def)); err != nil {
					return fmt.Errorf("给 %s 加列 %s 失败: %w", t.Name, c.Name, err)
				}
			}
		}
		for _, ix := range t.Indexes {
			ok, err := s.d.hasIndex(ctx, s.W, t.Name, ix.Name)
			if err != nil {
				return err
			}
			if !ok {
				if _, err := s.W.ExecContext(ctx, s.d.createIndex(t, ix)); err != nil {
					return fmt.Errorf("建索引 %s 失败: %w", ix.Name, err)
				}
			}
		}
	}
	var n int
	if err := s.W.QueryRowContext(ctx, "SELECT count(*) FROM _seq WHERE id = 1").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.W.ExecContext(ctx, "INSERT INTO _seq (id, seq) VALUES (1, 0)"); err != nil {
			return err
		}
	}
	return nil
}

// Rebind 把 ? 占位符改成引擎需要的形式。
func (s *Store) Rebind(q string) string { return s.d.rebind(q) }

// Get 读一行到结构体，没有时返回 sql.ErrNoRows。
func (s *Store) Get(ctx context.Context, dest any, q string, args ...any) error {
	return s.R.GetContext(ctx, dest, s.d.rebind(q), args...)
}

// Select 读多行。
func (s *Store) Select(ctx context.Context, dest any, q string, args ...any) error {
	return s.R.SelectContext(ctx, dest, s.d.rebind(q), args...)
}

// QueryRow 读单值。
func (s *Store) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.R.QueryRowContext(ctx, s.d.rebind(q), args...)
}

// ExecLocal 直接写本机表（会话、限流等，不进日志）。
func (s *Store) ExecLocal(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.W.ExecContext(ctx, s.d.rebind(q), args...)
}

// PutLocal 写本机表的一整行（upsert）。
func (s *Store) PutLocal(ctx context.Context, table string, v any) error {
	t, ok := TableByName(table)
	if !ok || t.Journaled {
		return fmt.Errorf("store: PutLocal 只能写本机表，%s 不是", table)
	}
	row, err := ToRow(v)
	if err != nil {
		return err
	}
	cols, vals, _, err := normalizeRow(t, row, true)
	if err != nil {
		return err
	}
	_, err = s.W.ExecContext(ctx, s.d.rebind(s.d.upsert(t, cols)), vals...)
	return err
}

// Seq 返回已提交的最后一个变更序号。
func (s *Store) Seq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.W.QueryRowContext(ctx, "SELECT seq FROM _seq WHERE id = 1").Scan(&seq)
	return seq, err
}

// Meta 读本机元数据。
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.W.QueryRowContext(ctx, s.d.rebind("SELECT value FROM meta WHERE id = ?"), key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta 写本机元数据。
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.PutLocal(ctx, "meta", Row{"id": key, "value": value})
}

// Subscribe 订阅提交事件（同步回调，必须很快返回）。
func (s *Store) Subscribe(fn func(Commit)) {
	s.subMu.Lock()
	s.subs = append(s.subs, fn)
	s.subMu.Unlock()
}

// Notify 返回「有新的外箱变更」通知通道。
func (s *Store) Notify() <-chan struct{} { return s.notify }

// SetReadOnly 设置只读原因（空串恢复可写）。
func (s *Store) SetReadOnly(reason string) { s.readOnly.Store(reason) }

// ReadOnlyReason 返回只读原因。
func (s *Store) ReadOnlyReason() string { return s.readOnly.Load().(string) }

// Tx 是一次写事务。
type Tx struct {
	s     *Store
	tx    *sqlx.Tx
	ctx   context.Context
	ops   []Op
	pos   map[string]int
	after []func()
}

// Write 执行一次写事务；业务表的改动经外箱进日志。返回本次分配的序号（无改动为 0）。
func (s *Store) Write(ctx context.Context, actor string, fn func(tx *Tx) error) (int64, error) {
	if r := s.ReadOnlyReason(); r != "" {
		return 0, fmt.Errorf("%w：%s", ErrReadOnly, r)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		seq, err := s.writeOnce(ctx, actor, fn)
		if err == nil || !isRetryable(err) {
			return seq, err
		}
		lastErr = err
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	return 0, lastErr
}

func isRetryable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Deadlock found") || strings.Contains(msg, "deadlock detected") ||
		strings.Contains(msg, "could not serialize") || strings.Contains(msg, "SQLITE_BUSY")
}

func (s *Store) writeOnce(ctx context.Context, actor string, fn func(tx *Tx) error) (int64, error) {
	sqltx, err := s.W.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	tx := &Tx{s: s, tx: sqltx, ctx: ctx, pos: map[string]int{}}
	done := false
	defer func() {
		if !done {
			_ = sqltx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return 0, err
	}
	var seq int64
	ts := time.Now().UnixMilli()
	if len(tx.ops) > 0 {
		if _, err := sqltx.ExecContext(ctx, "UPDATE _seq SET seq = seq + 1 WHERE id = 1"); err != nil {
			return 0, err
		}
		if err := sqltx.QueryRowContext(ctx, "SELECT seq FROM _seq WHERE id = 1").Scan(&seq); err != nil {
			return 0, err
		}
		opsJSON, err := json.Marshal(tx.ops)
		if err != nil {
			return 0, err
		}
		if _, err := sqltx.ExecContext(ctx, s.d.rebind("INSERT INTO _outbox (id, ts, actor, ops) VALUES (?, ?, ?, ?)"), seq, ts, actor, string(opsJSON)); err != nil {
			return 0, err
		}
	}
	if err := sqltx.Commit(); err != nil {
		return 0, err
	}
	done = true
	if seq > 0 {
		c := Commit{Seq: seq, TS: ts, Actor: actor, Ops: tx.ops}
		s.subMu.RLock()
		subs := append([]func(Commit){}, s.subs...)
		s.subMu.RUnlock()
		for _, fn := range subs {
			fn(c)
		}
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
	for _, fn := range tx.after {
		fn()
	}
	return seq, nil
}

// Context 返回事务的上下文。
func (tx *Tx) Context() context.Context { return tx.ctx }

// Get 在事务内读一行。
func (tx *Tx) Get(dest any, q string, args ...any) error {
	return tx.tx.GetContext(tx.ctx, dest, tx.s.d.rebind(q), args...)
}

// Select 在事务内读多行。
func (tx *Tx) Select(dest any, q string, args ...any) error {
	return tx.tx.SelectContext(tx.ctx, dest, tx.s.d.rebind(q), args...)
}

// QueryRow 在事务内读单值。
func (tx *Tx) QueryRow(q string, args ...any) *sql.Row {
	return tx.tx.QueryRowContext(tx.ctx, tx.s.d.rebind(q), args...)
}

// ExecLocal 在事务内写本机表。
func (tx *Tx) ExecLocal(q string, args ...any) (sql.Result, error) {
	return tx.tx.ExecContext(tx.ctx, tx.s.d.rebind(q), args...)
}

// After 注册提交成功后执行的回调。
func (tx *Tx) After(fn func()) { tx.after = append(tx.after, fn) }

func (tx *Tx) record(op Op) {
	k := op.T + "\x00" + op.ID
	if i, ok := tx.pos[k]; ok {
		tx.ops[i] = op
		return
	}
	tx.pos[k] = len(tx.ops)
	tx.ops = append(tx.ops, op)
}

// Put 写入一整行（新增或覆盖）。必须提供表的全部列。
func (tx *Tx) Put(table string, v any) error {
	t, ok := TableByName(table)
	if !ok {
		return fmt.Errorf("store: 未知的表 %s", table)
	}
	row, err := ToRow(v)
	if err != nil {
		return err
	}
	cols, vals, clean, err := normalizeRow(t, row, true)
	if err != nil {
		return err
	}
	if _, err := tx.tx.ExecContext(tx.ctx, tx.s.d.rebind(tx.s.d.upsert(t, cols)), vals...); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", table, err)
	}
	if t.Journaled {
		tx.record(Op{T: table, O: "put", ID: clean["id"].(string), Row: clean})
	}
	return nil
}

// Delete 删除一行。
func (tx *Tx) Delete(table, id string) error {
	t, ok := TableByName(table)
	if !ok {
		return fmt.Errorf("store: 未知的表 %s", table)
	}
	if _, err := tx.tx.ExecContext(tx.ctx, tx.s.d.rebind("DELETE FROM "+quoteIdent(tx.s.Engine, table)+" WHERE id = ?"), id); err != nil {
		return err
	}
	if t.Journaled {
		tx.record(Op{T: table, O: "del", ID: id})
	}
	return nil
}

// HasChanges 表示事务里是否有进日志的改动。
func (tx *Tx) HasChanges() bool { return len(tx.ops) > 0 }
