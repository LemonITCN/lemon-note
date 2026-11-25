package persist

// 外部数据库（MySQL / PostgreSQL）集成测试。设置了下面的环境变量才跑，否则跳过；
// CI 里由 MySQL 8 与 PostgreSQL 16 服务容器提供数据库。测试会清空目标库里的全部表，只能指向专用的测试库。
//
//	LEMON_NOTE_TEST_MYSQL_DSN  例如 root:test@tcp(127.0.0.1:3306)/lemon_note_test
//	LEMON_NOTE_TEST_PG_DSN     例如 postgres://postgres:test@127.0.0.1:5432/lemon_note_test?sslmode=disable

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

type externalDB struct {
	name   string
	engine store.Engine
	driver string
	dsn    string
}

func externalDBs() []externalDB {
	var out []externalDB
	if dsn := os.Getenv("LEMON_NOTE_TEST_MYSQL_DSN"); dsn != "" {
		out = append(out, externalDB{name: "mysql", engine: store.MySQL, driver: "mysql", dsn: dsn})
	}
	if dsn := os.Getenv("LEMON_NOTE_TEST_PG_DSN"); dsn != "" {
		out = append(out, externalDB{name: "postgres", engine: store.Postgres, driver: "pgx", dsn: dsn})
	}
	return out
}

// wipe 删掉测试库里的全部表，模拟一个全新的空库。
func (e externalDB) wipe(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(e.driver, e.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("连接 %s 失败: %v", e.name, err)
	}
	defer conn.Close()
	var q, drop string
	switch e.engine {
	case store.MySQL:
		q = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()"
		drop = "DROP TABLE IF EXISTS `%s`"
		if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
			t.Fatal(err)
		}
	default:
		q = "SELECT tablename FROM pg_tables WHERE schemaname = current_schema()"
		drop = `DROP TABLE IF EXISTS "%s" CASCADE`
	}
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(drop, n)); err != nil {
			t.Fatalf("删除表 %s 失败: %v", n, err)
		}
	}
}

func TestExternalDatabases(t *testing.T) {
	dbs := externalDBs()
	if len(dbs) == 0 {
		t.Skip("未设置 LEMON_NOTE_TEST_MYSQL_DSN / LEMON_NOTE_TEST_PG_DSN，跳过外部数据库测试")
	}
	for _, e := range dbs {
		t.Run(e.name, func(t *testing.T) { testExternalLifecycle(t, e) })
	}
}

func testExternalLifecycle(t *testing.T, e externalDB) {
	ctx := context.Background()
	e.wipe(t)
	t.Cleanup(func() { e.wipe(t) })

	root := t.TempDir()
	data, cache := filepath.Join(root, "data"), filepath.Join(root, "cache")
	cfg := testCfg(t, data, cache, "host-a")
	cfg.DSN = e.dsn
	if err := Initialize(ctx, cfg, e.engine); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}

	// 中文、emoji 与超过 64 KB 的长文本：检查字符集与列类型
	unicode := "柠檬笔记 Lemon Note \U0001F34B 数据库测试"
	long := strings.Repeat("长文本 long text 0123456789 ", 8000)

	in, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if in.Info.Engine != string(e.engine) {
		t.Fatalf("引擎应为 %s，实际 %s", e.engine, in.Info.Engine)
	}
	for i := 0; i < 30; i++ {
		put(t, in, fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
	}
	put(t, in, "k1", "changed")
	put(t, in, "unicode", unicode)
	put(t, in, "long", long)
	if err := in.SealPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 重开：库还在，直接接着用
	in, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	if get(t, in, "k1") != "changed" || get(t, in, "unicode") != unicode || get(t, in, "long") != long {
		t.Fatal("重开后数据不一致")
	}
	put(t, in, "after-reopen", "1")
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 换一个空库、删掉缓存目录（换机器）：从数据目录的快照与日志重建
	e.wipe(t)
	os.RemoveAll(cache)
	in, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("空库重建失败: %v", err)
	}
	if get(t, in, "k1") != "changed" || get(t, in, "k29") != "v29" || get(t, in, "after-reopen") != "1" ||
		get(t, in, "unicode") != unicode || get(t, in, "long") != long {
		t.Fatal("重建后数据不一致")
	}
	st := in.Status(ctx)
	if st.SealedSeq != st.Seq {
		t.Fatalf("重建后应全部封存 sealed=%d seq=%d", st.SealedSeq, st.Seq)
	}

	// 写快照后再写一条，再换空库重建：快照导出与导入都走外部库
	if err := in.Snapshot(ctx); err != nil {
		t.Fatalf("写快照失败: %v", err)
	}
	put(t, in, "after-snap", "2")
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	e.wipe(t)
	in, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("快照后重建失败: %v", err)
	}
	defer in.Close(ctx)
	if get(t, in, "after-snap") != "2" || get(t, in, "k3") != "v3" || get(t, in, "long") != long {
		t.Fatal("快照后重建数据不一致")
	}
	var n int
	if err := in.Store.QueryRow(ctx, "SELECT count(*) FROM _outbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("外箱应为空，实际 %d", n)
	}
}
