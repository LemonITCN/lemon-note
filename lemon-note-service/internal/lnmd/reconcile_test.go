package lnmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func withID(n Node, id string) Node {
	attrs := map[string]any{}
	for k, v := range n.Attrs {
		attrs[k] = v
	}
	attrs["id"] = id
	n.Attrs = attrs
	return n
}

// samplePage 构造一页带 id、批注标记、列宽等 Markdown 表达不了的信息的块。
func samplePage() []Node {
	return []Node{
		withID(nd(tHeading, map[string]any{"level": 1}, txt("项目计划")), "b1"),
		withID(para(txt("这段有"), txt("批注", mk("comment", "threadId", "th1")), txt("和颜色。")), "b2"),
		withID(nd(tBulletList, map[string]any{"custom": "x"}, nd(tListItem, map[string]any{"color": "red", "id": "li1"}, para(txt("第一项"))), li(para(txt("第二项")))), "b3"),
		withID(nd(tTable, nil, nd(tTableRow, nil, nd(tTableHeader, map[string]any{"colwidth": []any{120.0}}, para(txt("名"))), nd(tTableHeader, nil, para(txt("值"))))), "b4"),
		withID(nd(tWhiteboard, map[string]any{"title": "架构图", "data": map[string]any{"elements": []any{map[string]any{"id": "e1", "type": "rectangle"}}}}), "wb1"),
		withID(nd(tMindmap, map[string]any{"data": map[string]any{
			"layout": "logicalStructure", "theme": "classic",
			"root": map[string]any{"id": "m0", "text": "中心", "style": map[string]any{"fill": "#fff"}, "children": []any{
				map[string]any{"id": "m1", "text": "分支一", "note": "备注一", "children": []any{map[string]any{"id": "m11", "text": "叶子"}}},
				map[string]any{"id": "m2", "text": "分支二", "children": []any{}},
			}},
		}}), "mm1"),
		withID(para(txt("结尾段落")), "b7"),
		withID(nd(tSyncedBlock, nil, para(txt("同步内容"))), "sync1"),
		withID(para(), "b9"),
		withID(para(), "b10"),
	}
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func ids(ns []Node) string {
	var out []string
	for _, n := range ns {
		out = append(out, nodeID(n))
	}
	return strings.Join(out, ",")
}

func reconcile(t *testing.T, orig []Node, md string) []Node {
	t.Helper()
	out, err := Reconcile(orig, md, testOpts)
	if err != nil {
		t.Fatal(err)
	}
	noPseudo(t, "reconcile", out)
	return out
}

func TestReconcileUnchanged(t *testing.T) {
	orig := samplePage()
	md := Render(orig, testOpts)
	out := reconcile(t, orig, md)
	if jsonOf(out) != jsonOf(orig) {
		t.Fatalf("未改动的页面应原样返回\n原:%s\n新:%s", jsonOf(orig), jsonOf(out))
	}
	// 渲染给智能体看的文本不含普通块 id
	if strings.Contains(md, "b1") || strings.Contains(md, "b2") || strings.Contains(md, "th1") {
		t.Fatalf("Markdown 不应出现块 id 或批注：\n%s", md)
	}
}

func TestReconcileModify(t *testing.T) {
	orig := samplePage()
	md := strings.Replace(Render(orig, testOpts), "结尾段落", "**结尾**段落改过", 1)
	out := reconcile(t, orig, md)
	if ids(out) != ids(orig) {
		t.Fatalf("id 应全部保留：%s", ids(out))
	}
	for i := range orig {
		if i == 6 {
			continue
		}
		if jsonOf(out[i]) != jsonOf(orig[i]) {
			t.Fatalf("第 %d 块应原样保留", i)
		}
	}
	if inlineText(out[6].Content) != "结尾段落改过" || marksOf(out[6].Content[0]) != mBold {
		t.Fatalf("改动的块应用新内容：%s", jsonOf(out[6]))
	}
}

func TestReconcileInsertDelete(t *testing.T) {
	orig := samplePage()
	md := Render(orig, testOpts)
	md = strings.Replace(md, "# 项目计划", "# 项目计划\n\n新插入的段落\n\n- 新列表", 1)
	md = strings.Replace(md, "结尾段落\n\n", "", 1)
	out := reconcile(t, orig, md)
	if ids(out) != "b1,,,b2,b3,b4,wb1,mm1,sync1,b9,b10" {
		t.Fatalf("id 序列不对：%s", ids(out))
	}
	if jsonOf(out[3]) != jsonOf(orig[1]) || jsonOf(out[9]) != jsonOf(orig[8]) {
		t.Fatal("未改动的块应原样保留")
	}
	if out[1].Type != tParagraph || out[2].Type != tBulletList {
		t.Fatal(jsonOf(out))
	}
}

func TestReconcileMove(t *testing.T) {
	orig := samplePage()
	parts := strings.Split(Render(orig, testOpts), "\n\n")
	// 把第 2 块（带批注）挪到最后
	idx := -1
	for i, p := range parts {
		if strings.HasPrefix(p, "这段有") {
			idx = i
		}
	}
	moved := parts[idx]
	parts = append(parts[:idx], parts[idx+1:]...)
	parts = append(parts, moved)
	out := reconcile(t, orig, strings.Join(parts, "\n\n"))
	last := out[len(out)-1]
	if jsonOf(last) != jsonOf(orig[1]) {
		t.Fatalf("移动的块应原样保留 JSON（含 id 与批注）：%s", jsonOf(last))
	}
	if ids(out) != "b1,b3,b4,wb1,mm1,b7,sync1,b9,b10,b2" {
		t.Fatalf("%s", ids(out))
	}
}

func TestReconcileReplaceOneWithTwo(t *testing.T) {
	orig := samplePage()
	md := strings.Replace(Render(orig, testOpts), "结尾段落", "拆成第一段\n\n和第二段", 1)
	out := reconcile(t, orig, md)
	if ids(out) != "b1,b2,b3,b4,wb1,mm1,b7,,sync1,b9,b10" {
		t.Fatalf("%s", ids(out))
	}
}

func TestReconcileWhiteboard(t *testing.T) {
	orig := samplePage()
	md := strings.Replace(Render(orig, testOpts), `title="架构图"`, `title="新架构图"`, 1)
	out := reconcile(t, orig, md)
	wb := out[4]
	if nodeID(wb) != "wb1" || attrStr(wb, "title") != "新架构图" || jsonOf(wb.Attrs["data"]) != jsonOf(orig[4].Attrs["data"]) {
		t.Fatalf("白板应保留原 data：%s", jsonOf(wb))
	}

	// 白板挪进分栏，data 仍然带回
	md2 := strings.Replace(Render(orig, testOpts), `<whiteboard id="wb1" title="架构图"/>`, "<columns>\n<column>\n<whiteboard id=\"wb1\" title=\"架构图\"/>\n</column>\n</columns>", 1)
	out2 := reconcile(t, orig, md2)
	inner := out2[4].Content[0].Content[0]
	if inner.Type != tWhiteboard || nodeID(inner) != "wb1" || jsonOf(inner.Attrs["data"]) != jsonOf(orig[4].Attrs["data"]) {
		t.Fatalf("嵌套白板应带回 data：%s", jsonOf(out2[4]))
	}

	// 复制一份白板：第二份不能沿用同一个 id
	md3 := strings.Replace(Render(orig, testOpts), `<whiteboard id="wb1" title="架构图"/>`, `<whiteboard id="wb1" title="架构图"/>`+"\n\n"+`<whiteboard id="wb1" title="副本"/>`, 1)
	out3 := reconcile(t, orig, md3)
	if nodeID(out3[4]) != "wb1" || nodeID(out3[5]) != "" {
		t.Fatalf("重复的白板 id 应清空：%s", ids(out3))
	}

	// 编造的白板 id 清空交给调用方分配
	md4 := Render(orig, testOpts) + "\n\n<whiteboard id=\"fake\" title=\"新白板\"/>"
	out4 := reconcile(t, orig, md4)
	if nodeID(out4[len(out4)-1]) != "" {
		t.Fatalf("未知白板 id 应清空：%s", jsonOf(out4[len(out4)-1]))
	}
}

func TestReconcileSyncedBlock(t *testing.T) {
	orig := samplePage()
	md := strings.Replace(Render(orig, testOpts), "同步内容", "同步内容改了", 1)
	out := reconcile(t, orig, md)
	if nodeID(out[7]) != "sync1" || inlineText(out[7].Content[0].Content) != "同步内容改了" {
		t.Fatalf("%s", jsonOf(out[7]))
	}
}

func TestReconcileMindmap(t *testing.T) {
	orig := samplePage()
	md := Render(orig, testOpts)
	md = strings.Replace(md, "  - 分支二", "  - 分支二改\n  - 新分支", 1)
	out := reconcile(t, orig, md)
	mm := out[5]
	if nodeID(mm) != "mm1" {
		t.Fatal(jsonOf(mm))
	}
	data := mm.Attrs["data"].(map[string]any)
	if data["layout"] != "logicalStructure" || data["theme"] != "classic" {
		t.Fatalf("布局主题应继承：%s", jsonOf(data))
	}
	root := data["root"].(map[string]any)
	if root["id"] != "m0" || jsonOf(root["style"]) != `{"fill":"#fff"}` {
		t.Fatalf("根节点应继承 id 与样式：%s", jsonOf(root))
	}
	kids := mindmapChildren(root)
	b1 := kids[0].(map[string]any)
	if b1["id"] != "m1" || b1["note"] != "备注一" || mindmapChildren(b1)[0].(map[string]any)["id"] != "m11" {
		t.Fatalf("未改的分支应继承：%s", jsonOf(b1))
	}
	if len(kids) != 3 || kids[1].(map[string]any)["id"] != nil || kids[2].(map[string]any)["text"] != "新分支" {
		t.Fatalf("改名与新增的分支不继承：%s", jsonOf(kids))
	}
	// 思维导图里的非结构字段不出现在 Markdown
	if strings.Contains(md, "logicalStructure") || strings.Contains(md, "备注一") || strings.Contains(md, "m11") {
		t.Fatalf("Markdown 不应包含思维导图的非结构字段：\n%s", md)
	}
}

func TestReconcileRewrite(t *testing.T) {
	orig := []Node{withID(para(txt("甲")), "a"), withID(para(txt("乙")), "b"), withID(para(txt("丙")), "c")}
	out := reconcile(t, orig, "一\n\n二")
	if ids(out) != "a,b" {
		t.Fatalf("整体改写按位置继承：%s", ids(out))
	}
	out = reconcile(t, orig, "")
	if len(out) != 0 {
		t.Fatal(jsonOf(out))
	}
	out = reconcile(t, nil, "新页面\n\n- 列表")
	if ids(out) != "," {
		t.Fatal(jsonOf(out))
	}
}

func TestReconcileDuplicateBlocks(t *testing.T) {
	orig := []Node{withID(para(), "e1"), withID(para(txt("x")), "x"), withID(para(), "e2"), withID(para(), "e3")}
	md := Render(orig, testOpts)
	out := reconcile(t, orig, md)
	if jsonOf(out) != jsonOf(orig) {
		t.Fatal(jsonOf(out))
	}
	// 删掉中间那个空行块
	out = reconcile(t, orig, "<empty-block/>\n\nx\n\n<empty-block/>")
	if len(out) != 3 || nodeID(out[0]) != "e1" || nodeID(out[1]) != "x" {
		t.Fatal(ids(out))
	}
	seen := map[string]bool{}
	for _, n := range out {
		if seen[nodeID(n)] {
			t.Fatalf("id 重复：%s", ids(out))
		}
		seen[nodeID(n)] = true
	}
}

func TestReconcileLLMEdits(t *testing.T) {
	orig := samplePage()
	md := Render(orig, testOpts)
	// 大模型常见的不规范写法：标签与文字同一行、缺空行
	md = strings.Replace(md, "结尾段落", "结尾段落\n<callout icon=\"💡\">新增提示</callout>", 1)
	out := reconcile(t, orig, md)
	if ids(out) != "b1,b2,b3,b4,wb1,mm1,b7,,sync1,b9,b10" || out[7].Type != tCallout {
		t.Fatalf("%s %s", ids(out), types(out))
	}
	if jsonOf(out[6]) != jsonOf(orig[6]) {
		t.Fatal("原段落文本没变应原样保留")
	}
}

// bigPage 生成一个包含多种块的长页面。
func bigPage(n int) []Node {
	var out []Node
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("blk%05d", i)
		var b Node
		switch i % 10 {
		case 0:
			b = nd(tHeading, map[string]any{"level": 2}, txt(fmt.Sprintf("第 %d 节", i)))
		case 1:
			b = para(txt("这是一段比较长的正文，包含 "), txt("粗体", mk(mBold)), txt("、"), txt("斜体", mk(mItalic)), txt("、"),
				txt("代码", mk(mCode)), txt(" 和 "), txt("链接", mk(mLink, "href", "https://example.com")), txt(fmt.Sprintf("，编号 %d。", i)))
		case 2:
			b = nd(tBulletList, nil, li(para(txt(fmt.Sprintf("要点 %d-1", i)))), li(para(txt(fmt.Sprintf("要点 %d-2", i))), nd(tBulletList, nil, li(para(txt("子项"))))))
		case 3:
			b = nd(tCodeBlock, map[string]any{"language": "go"}, txt(fmt.Sprintf("func f%d() int {\n\treturn %d\n}", i, i)))
		case 4:
			b = nd(tCallout, map[string]any{"icon": "💡"}, para(txt(fmt.Sprintf("提示 %d：", i), mk(mBold)), txt("注意这里。")))
		case 5:
			b = nd(tTable, nil, nd(tTableRow, nil, nd(tTableHeader, nil, para(txt("键"))), nd(tTableHeader, nil, para(txt("值")))),
				nd(tTableRow, nil, nd(tTableCell, nil, para(txt(fmt.Sprint("k", i)))), nd(tTableCell, nil, para(txt(fmt.Sprint(i))))))
		case 6:
			b = nd(tTaskList, nil, nd(tTaskItem, map[string]any{"checked": i%2 == 0}, para(txt(fmt.Sprintf("任务 %d", i)))))
		case 7:
			b = nd(tToggle, nil, nd(tToggleTitle, nil, txt(fmt.Sprintf("折叠 %d", i))), nd(tToggleBody, nil, para(txt("内容"))))
		case 8:
			b = para(txt(fmt.Sprintf("公式 %d：", i)), Node{Type: tMathInline, Attrs: map[string]any{"latex": "x^2"}}, txt("，见 "), Node{Type: tMention, Attrs: map[string]any{"kind": "page", "id": "p1"}})
		default:
			b = para()
		}
		out = append(out, withID(b, id))
	}
	return out
}

func TestReconcileLargePagePerformance(t *testing.T) {
	const n = 3000
	orig := bigPage(n)
	start := time.Now()
	md := Render(orig, testOpts)
	renderCost := time.Since(start)

	edited := strings.Replace(md, "第 1500 节", "第 1500 节（已修改）", 1)
	edited = strings.Replace(edited, "要点 12-1", "要点 12-1 改", 1)
	edited += "\n\n文末新增段落"
	start = time.Now()
	out, err := Reconcile(orig, edited, testOpts)
	cost := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d 块：渲染 %v，Reconcile %v", n, renderCost, cost)
	if cost > time.Second {
		t.Fatalf("Reconcile 太慢：%v", cost)
	}
	if len(out) != n+1 {
		t.Fatalf("块数 %d", len(out))
	}
	changed := 0
	for i := 0; i < n; i++ {
		if nodeID(out[i]) != nodeID(orig[i]) {
			t.Fatalf("第 %d 块 id 变了：%s", i, nodeID(out[i]))
		}
		if jsonOf(out[i]) != jsonOf(orig[i]) {
			changed++
		}
	}
	if changed != 2 || nodeID(out[n]) != "" {
		t.Fatalf("应只有 2 块变化，实际 %d", changed)
	}

	// 整页往返也必须逐块一致
	checkPage(t, "长页面", orig, testOpts)

	// 整页改写（最坏情况）也要在时限内
	start = time.Now()
	rewritten := strings.ReplaceAll(md, "。", "！")
	if _, err := Reconcile(orig, rewritten, testOpts); err != nil {
		t.Fatal(err)
	}
	if c := time.Since(start); c > 2*time.Second {
		t.Fatalf("整页改写 Reconcile 太慢：%v", c)
	} else {
		t.Logf("整页改写 Reconcile %v", c)
	}
}

func TestAlignBlocks(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"abcde", "abcde", 5},
		{"abcde", "axcye", 3},
		{"abc", "", 0},
		{"", "abc", 0},
		{"abcabc", "cbacba", 3},
		{"aaaa", "aa", 2},
	}
	for _, c := range cases {
		a, b := strings.Split(c.a, ""), strings.Split(c.b, "")
		if c.a == "" {
			a = nil
		}
		if c.b == "" {
			b = nil
		}
		pairs := alignBlocks(a, b)
		if len(pairs) != c.want {
			t.Errorf("%s/%s: 期望 %d 对，得到 %v", c.a, c.b, c.want, pairs)
		}
		for k, p := range pairs {
			if a[p[0]] != b[p[1]] || (k > 0 && (p[0] <= pairs[k-1][0] || p[1] <= pairs[k-1][1])) {
				t.Errorf("%s/%s: 对齐不合法 %v", c.a, c.b, pairs)
			}
		}
	}
	// 大规模走 patience 分支
	var a, b []string
	for i := 0; i < 3000; i++ {
		a = append(a, fmt.Sprint("x", i))
		if i%7 != 0 {
			b = append(b, fmt.Sprint("x", i))
		}
		if i%11 == 0 {
			b = append(b, fmt.Sprint("new", i))
		}
	}
	b = append([]string{"head"}, b...)
	a = append(a, "tail-a")
	pairs := alignBlocks(a, b)
	want := 0
	for i := 0; i < 3000; i++ {
		if i%7 != 0 {
			want++
		}
	}
	if len(pairs) != want {
		t.Fatalf("大规模对齐期望 %d 对，得到 %d", want, len(pairs))
	}
}
