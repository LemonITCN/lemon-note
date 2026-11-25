package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/netsafe"
)

// 模拟 Notion API。密钥是测试用的假值。
const fakeToken = "secret_test_only_not_real_0000"

const (
	nRootID  = "11111111-1111-1111-1111-111111111111"
	nChildID = "22222222-2222-2222-2222-222222222222"
	nDBID    = "33333333-3333-3333-3333-333333333333"
	nRow1    = "44444444-4444-4444-4444-444444444444"
	nRow2    = "55555555-5555-5555-5555-555555555555"
	nOutside = "66666666-6666-6666-6666-666666666666"
)

func rt(text string, extra ...string) string {
	ann := `{"bold":false,"italic":false,"strikethrough":false,"underline":false,"code":false,"color":"default"}`
	if len(extra) > 0 {
		ann = extra[0]
	}
	b, _ := json.Marshal(text)
	return `{"type":"text","plain_text":` + string(b) + `,"text":{"content":` + string(b) + `},"annotations":` + ann + `}`
}

func block(id, typ, body string, children bool) string {
	return fmt.Sprintf(`{"object":"block","id":"%s","type":"%s","has_children":%v,"%s":%s}`, id, typ, children, typ, body)
}

func list(items ...string) string {
	return `{"object":"list","results":[` + strings.Join(items, ",") + `],"has_more":false,"next_cursor":null}`
}

func notionServer(t *testing.T) (*httptest.Server, *int32) {
	var limited int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	page := func(id, title, parent string) string {
		return `{"object":"page","id":"` + id + `","parent":` + parent + `,"icon":{"type":"emoji","emoji":"📘"},"archived":false,` +
			`"properties":{"title":{"id":"title","type":"title","title":[` + rt(title) + `]}},"url":"https://www.notion.so/x"}`
	}
	ws := `{"type":"workspace","workspace":true}`
	mux.HandleFunc("/files/pic.png", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("下载文件不应带 Notion 密钥")
		}
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, pngBytes(t))
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeToken || r.Header.Get("Notion-Version") == "" {
			w.WriteHeader(401)
			io.WriteString(w, `{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid."}`)
			return
		}
		p := strings.ReplaceAll(r.URL.Path, "-", "") // Notion 接受带不带横线的 id
		d := func(id string) string { return strings.ReplaceAll(id, "-", "") }
		switch {
		case p == "/v1/search":
			io.WriteString(w, list(page(nRootID, "Root", ws), page(nChildID, "Child", `{"type":"page_id","page_id":"`+nRootID+`"}`)))
		case p == "/v1/pages/"+d(nChildID):
			io.WriteString(w, page(nChildID, "Child", `{"type":"page_id","page_id":"`+nRootID+`"}`))
		case p == "/v1/blocks/"+d(nRootID)+"/children":
			mention := `{"type":"mention","plain_text":"Child","mention":{"type":"page","page":{"id":"` + nChildID + `"}},"annotations":{"color":"default"}}`
			outside := `{"type":"mention","plain_text":"Elsewhere","mention":{"type":"page","page":{"id":"` + nOutside + `"}},"annotations":{"color":"default"}}`
			bold := rt("bold red", `{"bold":true,"italic":false,"strikethrough":false,"underline":false,"code":false,"color":"red"}`)
			io.WriteString(w, list(
				block("b1", "paragraph", `{"rich_text":[`+bold+`,`+rt(" see ")+`,`+mention+`,`+rt(" and ")+`,`+outside+`],"color":"default"}`, false),
				block("b2", "callout", `{"rich_text":[`+rt("Note this")+`],"icon":{"type":"emoji","emoji":"⚠️"},"color":"yellow_background"}`, false),
				block("b3", "to_do", `{"rich_text":[`+rt("task one")+`],"checked":true}`, false),
				block("b4", "to_do", `{"rich_text":[`+rt("task two")+`],"checked":false}`, false),
				block("b5", "toggle", `{"rich_text":[`+rt("More")+`]}`, true),
				block("b6", "column_list", `{}`, true),
				block("b7", "image", `{"type":"file","file":{"url":"`+srv.URL+`/files/pic.png?X-Amz-Expires=3600"},"caption":[`+rt("A picture")+`]}`, false),
				block("b8", "code", `{"rich_text":[`+rt("fmt.Println(1)")+`],"language":"go"}`, false),
				block("b9", "equation", `{"expression":"e=mc^2"}`, false),
				block("b10", "table", `{"table_width":2,"has_column_header":true,"has_row_header":false}`, true),
				block(nChildID, "child_page", `{"title":"Child"}`, true),
				block(nDBID, "child_database", `{"title":"Tasks"}`, false),
				block("b11", "bookmark", `{"url":"https://example.com","caption":[]}`, false),
				block("b12", "heading_2", `{"rich_text":[`+rt("Section")+`],"is_toggleable":false,"color":"blue"}`, false),
				block("b13", "unsupported", `{}`, false),
			))
		case p == "/v1/blocks/b5/children":
			io.WriteString(w, list(block("b5a", "paragraph", `{"rich_text":[`+rt("hidden inside")+`]}`, false)))
		case p == "/v1/blocks/b6/children":
			io.WriteString(w, list(block("c1", "column", `{}`, true), block("c2", "column", `{}`, true)))
		case p == "/v1/blocks/c1/children":
			io.WriteString(w, list(block("c1a", "paragraph", `{"rich_text":[`+rt("left")+`]}`, false)))
		case p == "/v1/blocks/c2/children":
			io.WriteString(w, list(block("c2a", "paragraph", `{"rich_text":[`+rt("right")+`]}`, false)))
		case p == "/v1/blocks/b10/children":
			io.WriteString(w, list(
				block("r1", "table_row", `{"cells":[[`+rt("H1")+`],[`+rt("H2")+`]]}`, false),
				block("r2", "table_row", `{"cells":[[`+rt("a")+`],[`+rt("b")+`]]}`, false)))
		case p == "/v1/blocks/"+d(nChildID)+"/children":
			io.WriteString(w, list(block("d1", "paragraph", `{"rich_text":[`+rt("child body")+`]}`, false)))
		case p == "/v1/databases/"+d(nDBID):
			io.WriteString(w, `{"object":"database","id":"`+nDBID+`","title":[`+rt("Tasks")+`],"is_inline":true,"properties":{`+
				`"Name":{"id":"title","type":"title","title":{}},`+
				`"Status":{"id":"st%3A","type":"select","select":{"options":[{"id":"o1","name":"Todo","color":"red"},{"id":"o2","name":"Done","color":"green"}]}},`+
				`"Tags":{"id":"tg","type":"multi_select","multi_select":{"options":[{"id":"t1","name":"a","color":"blue"},{"id":"t2","name":"b","color":"default"}]}},`+
				`"Score":{"id":"sc","type":"number","number":{"format":"number_with_commas"}},`+
				`"Double":{"id":"db","type":"formula","formula":{"expression":"{{notion:block_property:sc:00000000-0000-0000-0000-000000000000:x}} * 2"}},`+
				`"Weird":{"id":"wd","type":"formula","formula":{"expression":"unknownFn(1"}},`+
				`"Parent":{"id":"pr","type":"relation","relation":{"database_id":"`+nDBID+`","type":"dual_property","dual_property":{"synced_property_name":"Children","synced_property_id":"ch"}}},`+
				`"Children":{"id":"ch","type":"relation","relation":{"database_id":"`+nDBID+`","type":"dual_property","dual_property":{"synced_property_name":"Parent","synced_property_id":"pr"}}},`+
				`"Owner":{"id":"ow","type":"people","people":{}},`+
				`"When":{"id":"wh","type":"date","date":{}},`+
				`"Done":{"id":"dn","type":"checkbox","checkbox":{}}}}`)
		case p == "/v1/databases/"+d(nDBID)+"/query":
			if atomic.AddInt32(&limited, 1) == 1 {
				w.Header().Set("Retry-After", "0.01")
				w.WriteHeader(429)
				io.WriteString(w, `{"object":"error","status":429,"code":"rate_limited","message":"slow down"}`)
				return
			}
			row := func(id, title, extra string) string {
				return `{"object":"page","id":"` + id + `","parent":{"type":"database_id","database_id":"` + nDBID + `"},"properties":{` +
					`"Name":{"id":"title","type":"title","title":[` + rt(title) + `]},` + extra + `}}`
			}
			io.WriteString(w, list(
				row(nRow1, "First", `"Status":{"type":"select","select":{"id":"o1","name":"Todo"}},"Tags":{"type":"multi_select","multi_select":[{"id":"t1","name":"a"},{"id":"t2","name":"b"}]},`+
					`"Score":{"type":"number","number":1234},"Weird":{"type":"formula","formula":{"type":"string","string":"calc"}},`+
					`"Children":{"type":"relation","relation":[{"id":"`+nRow2+`"}],"has_more":false},`+
					`"Owner":{"type":"people","people":[{"name":"Ada"}]},"When":{"type":"date","date":{"start":"2026-11-25","end":null}},"Done":{"type":"checkbox","checkbox":true}`),
				row(nRow2, "Second", `"Status":{"type":"select","select":{"id":"o2","name":"Done"}},"Parent":{"type":"relation","relation":[{"id":"`+nRow1+`"}],"has_more":false},"Done":{"type":"checkbox","checkbox":false}`),
			))
		case p == "/v1/blocks/"+d(nRow1)+"/children":
			io.WriteString(w, list(block("e1", "paragraph", `{"rich_text":[`+rt("row body")+`]}`, false)))
		case p == "/v1/blocks/"+d(nRow2)+"/children":
			io.WriteString(w, list())
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"object":"error","status":404,"code":"object_not_found","message":"not found"}`)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &limited
}

func TestNotionAPI(t *testing.T) {
	old := netsafe.AllowPrivate
	netsafe.AllowPrivate = true // 模拟服务器在本机
	t.Cleanup(func() { netsafe.AllowPrivate = old })
	e := setup(t)
	srv, limited := notionServer(t)
	e.s.NotionBaseURL = srv.URL
	if _, err := e.s.StartNotionAPI(e.ctx, e.user.ID, Target{SpaceID: e.space}, fakeToken, ""); err != nil {
		t.Fatal(err)
	}
	e.s.Wait()
	v := e.lastJob()
	mustDone(t, v)
	if atomic.LoadInt32(limited) < 2 {
		t.Fatal("429 之后应当重试")
	}
	if v.Report.Pages != 2 || v.Report.Databases != 1 || v.Report.Rows != 2 || v.Report.Files != 1 {
		t.Fatalf("计数不对：%+v", v.Report)
	}
	// 密钥不进报告、不进任务表
	raw, _ := json.Marshal(v)
	var stored string
	e.st.QueryRow(e.ctx, "SELECT progress || report FROM import_jobs WHERE id = ?", v.ID).Scan(&stored)
	if strings.Contains(string(raw), fakeToken) || strings.Contains(stored, fakeToken) {
		t.Fatal("报告里不能出现密钥")
	}
	root := e.child("", "Root")
	child := e.child(root.ID, "Child")
	tasks := e.child(root.ID, "Tasks")
	if root.Icon != "📘" || child.Icon != "📘" {
		t.Fatalf("图标应当保留：%q %q", root.Icon, child.Icon)
	}
	ns := e.nodes(root.ID)
	md := e.markdown(root.ID)
	checks := []struct {
		name string
		ok   bool
	}{
		{"粗体红字", find(ns, func(n lnmd.Node) bool {
			return n.Type == "text" && n.Text == "bold red" && len(n.Marks) == 2 && n.Marks[1].Type == "textColor"
		}) != nil},
		{"页面提及", find(ns, func(n lnmd.Node) bool { return n.Type == "mention" && n.Attrs["id"] == child.ID }) != nil},
		{"范围外的提及降级成文字", strings.Contains(md, "Elsewhere") && strings.Count(md, "<mention-page") == 1},
		{"标注框", find(ns, func(n lnmd.Node) bool {
			return n.Type == "callout" && n.Attrs["icon"] == "⚠️" && n.Attrs["color"] == "yellow_background"
		}) != nil},
		{"待办", strings.Contains(md, "- [x] task one") && strings.Contains(md, "- [ ] task two")},
		{"折叠块", find(ns, func(n lnmd.Node) bool { return n.Type == "toggle" }) != nil && strings.Contains(md, "hidden inside")},
		{"分栏", find(ns, func(n lnmd.Node) bool { return n.Type == "columns" && len(n.Content) == 2 }) != nil},
		{"图片下载", find(ns, func(n lnmd.Node) bool {
			return n.Type == "image" && strings.HasPrefix(fmt.Sprint(n.Attrs["src"]), "lemon://file/") && n.Attrs["alt"] == "A picture"
		}) != nil},
		{"代码", strings.Contains(md, "```go\nfmt.Println(1)\n```")},
		{"公式", strings.Contains(md, "$$\ne=mc^2\n$$")},
		{"表格", find(ns, func(n lnmd.Node) bool { return n.Type == "tableHeader" }) != nil && strings.Contains(md, "| a | b |")},
		{"子页面", find(ns, func(n lnmd.Node) bool { return n.Type == "childPage" && n.Attrs["pageId"] == child.ID }) != nil},
		{"子数据库", find(ns, func(n lnmd.Node) bool { return n.Type == "childDatabase" && n.Attrs["pageId"] == tasks.ID }) != nil},
		{"书签", strings.Contains(md, `<bookmark url="https://example.com"/>`)},
		{"标题颜色", strings.Contains(md, `## Section {color="blue"}`)},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s 不对：\n%s", c.name, md)
		}
	}
	if !strings.Contains(e.markdown(child.ID), "child body") {
		t.Fatal("子页面正文不对")
	}
	if !hasWarning(v, "unsupported_block") || !hasWarning(v, "people_as_text") || !hasWarning(v, "formula_as_text") {
		t.Fatalf("降级应当报告：%+v", v.Report.Warnings)
	}
	info, err := e.db.Info(e.ctx, tasks.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Page.GetFlags().Inline {
		t.Fatal("内嵌数据库应当保持内嵌")
	}
	want := map[string]string{"Name": dbs.TTitle, "Status": dbs.TSelect, "Tags": dbs.TMultiSelect, "Score": dbs.TNumber, "Double": dbs.TFormula,
		"Weird": dbs.TText, "Parent": dbs.TRelation, "Children": dbs.TRelation, "Owner": dbs.TText, "When": dbs.TDate, "Done": dbs.TCheckbox}
	for name, typ := range want {
		if p := info.Schema.Find(name); p == nil || p.Type != typ {
			t.Fatalf("属性 %s 应当是 %s：%+v", name, typ, p)
		}
	}
	par, chl := info.Schema.Find("Parent"), info.Schema.Find("Children")
	if !par.TwoWay || par.RelatedProp != chl.ID || chl.RelatedProp != par.ID || par.DatabaseID != tasks.ID {
		t.Fatalf("双向关联应当互相指向：%+v %+v", par, chl)
	}
	if f := info.Schema.Find("Double"); f.Expression != `prop("Score") * 2` {
		t.Fatalf("公式里的属性引用应当转换：%q", f.Expression)
	}
	if info.Schema.Find("Score").Format != "comma" {
		t.Fatal("数字格式应当保留")
	}
	tbl, _ := e.db.Load(e.ctx, tasks.ID)
	var first, second *dbs.Row
	for _, r := range tbl.Rows {
		if r.Title == "First" {
			first = r
		} else if r.Title == "Second" {
			second = r
		}
	}
	if first == nil || second == nil {
		t.Fatal("行没导入")
	}
	text := func(r *dbs.Row, name string) string { return dbs.ToText(tbl.Value(r, info.Schema.Find(name))) }
	got := []string{text(first, "Status"), text(first, "Tags"), text(first, "Score"), text(first, "Double"), text(first, "Weird"),
		text(first, "Children"), text(first, "Owner"), text(first, "When"), text(first, "Done"), text(second, "Parent")}
	exp := []string{"Todo", "a, b", "1234", "2468", "calc", "Second", "Ada", "2026-11-25", "true", "First"}
	for i := range got {
		if got[i] != exp[i] {
			t.Fatalf("行属性不对：%v，应当 %v", got, exp)
		}
	}
	if !strings.Contains(e.markdown(first.ID), "row body") {
		t.Fatal("行正文不对")
	}
}

func TestNotionAPIBadToken(t *testing.T) {
	old := netsafe.AllowPrivate
	netsafe.AllowPrivate = true
	t.Cleanup(func() { netsafe.AllowPrivate = old })
	e := setup(t)
	srv, _ := notionServer(t)
	e.s.NotionBaseURL = srv.URL
	if _, err := e.s.StartNotionAPI(e.ctx, e.user.ID, Target{SpaceID: e.space}, "secret_wrong", nRootID); err != nil {
		t.Fatal(err)
	}
	e.s.Wait()
	v := e.lastJob()
	if v.Status != StatusFailed || !strings.Contains(v.Report.Error, "密钥无效") {
		t.Fatalf("错误的密钥应当失败：%+v", v.Report)
	}
	if len(e.cat.SpacePages(e.space)) != 0 {
		t.Fatal("不应建页")
	}
}

func TestNotionAPIBlocksPrivateNetwork(t *testing.T) {
	e := setup(t)
	srv, _ := notionServer(t)
	e.s.NotionBaseURL = srv.URL // 本机地址：默认被防 SSRF 拦截
	if _, err := e.s.StartNotionAPI(e.ctx, e.user.ID, Target{SpaceID: e.space}, fakeToken, ""); err != nil {
		t.Fatal(err)
	}
	e.s.Wait()
	if v := e.lastJob(); v.Status != StatusFailed || !strings.Contains(v.Report.Error, "拦截") {
		t.Fatalf("内网地址应当被拦截：%+v", v.Report)
	}
}
