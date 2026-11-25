package dbs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func setup(t *testing.T) (*Service, *store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: filepath.Join(t.TempDir(), "w.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cat, err := catalog.Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	acc := accounts.New(st, cat)
	u, err := acc.CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "password-123", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	sp := cat.VisibleSpaces(u.ID)[0]
	return New(st, cat, pages.New(st, cat)), st, u.ID, sp.ID
}

func TestDatabaseEndToEnd(t *testing.T) {
	ctx := context.Background()
	s, st, uid, spaceID := setup(t)
	sc := &Schema{Properties: []Property{
		{ID: "title", Name: "任务", Type: TTitle},
		{Name: "状态", Type: TStatus, Options: DefaultStatusOptions(true)},
		{Name: "工时", Type: TNumber},
		{Name: "标签", Type: TMultiSelect},
		{Name: "截止", Type: TDate},
		{Name: "加倍", Type: TFormula, Expression: `if(prop("工时") > 3, prop("工时") * 2, 0)`},
	}}
	var tasksDB string
	_, err := st.Write(ctx, uid, func(tx *store.Tx) error {
		p, _, err := s.CreateTx(tx, uid, CreateInput{SpaceID: spaceID, Title: "任务库", Schema: sc, Zh: true})
		tasksDB = p.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{
		{"状态": "进行中", "工时": 5.0, "标签": []any{"前端", "急"}, "截止": "2026-12-01"},
		{"状态": "已完成", "工时": "2", "标签": "后端", "截止": "2026-11-20"},
		{"状态": "未开始", "工时": 8.0},
	}
	var ids []string
	for i, props := range rows {
		_, err := st.Write(ctx, uid, func(tx *store.Tx) error {
			p, err := s.CreateRowTx(tx, uid, tasksDB, []string{"写页面", "写接口", "写测试"}[i], props, "", nil, false)
			if p != nil {
				ids = append(ids, p.ID)
			}
			return err
		})
		if err != nil {
			t.Fatal(i, err)
		}
	}
	// 不存在的状态选项要报错（状态不自动建）
	_, err = st.Write(ctx, uid, func(tx *store.Tx) error { return s.SetPropsTx(tx, uid, ids[0], map[string]any{"状态": "不存在"}) })
	if err == nil {
		t.Fatal("不存在的状态应报错")
	}
	// 筛选 + 排序 + 公式
	_, res, err := s.Query(ctx, tasksDB, QueryInput{Filter: &Filter{Prop: "工时", Op: ">", Value: 3.0}, Sorts: []Sort{{Prop: "工时", Dir: "desc"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || res.Rows[0].Title != "写测试" {
		t.Fatalf("筛选排序错误：%v", titles(res.Rows))
	}
	tbl, _ := s.Load(ctx, tasksDB)
	f := tbl.Schema.Find("加倍")
	if v := tbl.Value(tbl.Row(ids[0]), f); v != 10.0 {
		t.Fatalf("公式结果 %v", v)
	}
	// 多选 contains
	_, res, _ = s.Query(ctx, tasksDB, QueryInput{Filter: &Filter{Prop: "标签", Op: "contains", Value: "急"}})
	if len(res.Rows) != 1 {
		t.Fatalf("多选筛选 %v", titles(res.Rows))
	}
	// 日期筛选
	_, res, _ = s.Query(ctx, tasksDB, QueryInput{Filter: &Filter{Prop: "截止", Op: "before", Value: "2026-11-25"}})
	if len(res.Rows) != 1 || res.Rows[0].Title != "写接口" {
		t.Fatalf("日期筛选 %v", titles(res.Rows))
	}
	// 分组
	_, res, _ = s.Query(ctx, tasksDB, QueryInput{GroupBy: &GroupBy{Prop: "状态"}})
	if len(res.Groups) != 3 || res.Groups[0].Label != "未开始" {
		t.Fatalf("分组 %+v", res.Groups)
	}

	// 双向关联 + 汇总
	var projDB string
	_, err = st.Write(ctx, uid, func(tx *store.Tx) error {
		p, _, err := s.CreateTx(tx, uid, CreateInput{SpaceID: spaceID, Title: "项目", Zh: true})
		projDB = p.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := s.Info(ctx, projDB)
	info.Schema.Properties = append(info.Schema.Properties, Property{Name: "任务", Type: TRelation, DatabaseID: tasksDB, TwoWay: true})
	if _, err := s.UpdateSchema(ctx, uid, projDB, info.Schema); err != nil {
		t.Fatal(err)
	}
	info, _ = s.Info(ctx, projDB)
	rel := info.Schema.Find("任务")
	info.Schema.Properties = append(info.Schema.Properties, Property{Name: "总工时", Type: TRollup, Relation: rel.ID, Target: tbl.Schema.Find("工时").ID, Function: "sum"})
	if _, err := s.UpdateSchema(ctx, uid, projDB, info.Schema); err != nil {
		t.Fatal(err)
	}
	var proj string
	_, err = st.Write(ctx, uid, func(tx *store.Tx) error {
		p, err := s.CreateRowTx(tx, uid, projDB, "笔记系统", map[string]any{"任务": []any{"写页面", ids[1]}}, "", nil, false)
		if p != nil {
			proj = p.ID
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	pt, _ := s.Load(ctx, projDB)
	if v := pt.Value(pt.Row(proj), pt.Schema.Find("总工时")); v != 7.0 {
		t.Fatalf("汇总结果 %v", v)
	}
	// 反向属性已写到任务行
	tt, _ := s.Load(ctx, tasksDB)
	back := tt.Schema.Find("项目")
	if back == nil {
		t.Fatal("对方数据库没有反向属性")
	}
	if l, _ := tt.Value(tt.Row(ids[0]), back).([]Value); len(l) != 1 || l[0] != "笔记系统" {
		t.Fatalf("反向关联 %v", tt.Value(tt.Row(ids[0]), back))
	}
	// 类型转换：数字 → 文本
	info, _ = s.Info(ctx, tasksDB)
	info.Schema.Find("工时").Type = TText
	if _, err := s.UpdateSchema(ctx, uid, tasksDB, info.Schema); err != nil {
		t.Fatal(err)
	}
	tt, _ = s.Load(ctx, tasksDB)
	if v := tt.Value(tt.Row(ids[0]), tt.Schema.Find("工时")); v != "5" {
		t.Fatalf("类型转换 %v", v)
	}
}

func titles(rs []*Row) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Title)
	}
	return out
}

func TestFormulaFunctions(t *testing.T) {
	cases := map[string]any{
		`1 + 2 * 3`: 7.0, `(1 + 2) * 3`: 9.0, `2 ^ 3 ^ 2`: 512.0, `"a" + 1`: "a1", `if(1 > 2, "x", "y")`: "y",
		`length("你好")`: 2.0, `contains("hello", "ell")`: true, `round(3.14159, 2)`: 3.14, `max(1, 5, 3)`: 5.0,
		`dateBetween(parseDate("2026-12-01"), parseDate("2026-11-25"), "days")`: 6.0, `upper("ab")`: "AB", `not true`: false,
		`[1,2,3].length()`: 3.0, `sum([1,2,3])`: 6.0, `1 > 0 && 2 > 1`: true, `formatDate(parseDate("2026-11-25"), "YYYY/MM/DD")`: "2026/11/25",
		`ifs(false, 1, true, 2, 3)`: 2.0, `true ? "是" : "否"`: "是",
	}
	for src, want := range cases {
		f, err := ParseFormula(src)
		if err != nil {
			t.Fatalf("%s 解析失败：%v", src, err)
		}
		got, err := f.Eval(&EvalEnv{})
		if err != nil {
			t.Fatalf("%s 求值失败：%v", src, err)
		}
		if got != want {
			t.Fatalf("%s = %v，期望 %v", src, got, want)
		}
	}
	if _, err := ParseFormula(`unknownFn(1)`); err == nil {
		t.Fatal("未知函数应报错")
	}
}
