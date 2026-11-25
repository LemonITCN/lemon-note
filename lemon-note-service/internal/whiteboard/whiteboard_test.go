package whiteboard

import (
	"strings"
	"testing"
)

func TestMermaidLayout(t *testing.T) {
	g, err := ParseMermaid("flowchart LR\n  A[开始] --> B{是否通过}\n  B -->|是| C(结束)\n  B -- 否 --> A\n  %% 注释\n  style A fill:#f00")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 3 || len(g.Edges) != 3 {
		t.Fatalf("节点 %d 连线 %d", len(g.Nodes), len(g.Edges))
	}
	if g.byID["B"].Shape != "diamond" || g.byID["C"].Shape != "ellipse" || g.byID["A"].Label != "开始" {
		t.Fatalf("形状或文字不对: %+v %+v", g.byID["B"], g.byID["A"])
	}
	if g.Edges[1].Label != "是" || g.Edges[2].Label != "否" {
		t.Fatalf("连线标签不对: %+v", g.Edges)
	}
	s := &Scene{}
	res, err := Apply(s, EditInput{Mermaid: "flowchart LR\n  A[开始] --> B{是否通过}\n  B -->|是| C(结束)\n  B -- 否 --> A"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 6 {
		t.Fatalf("应新增 3 个图形 3 条连线，实际 %d", res.Created)
	}
	ix := index(s)
	// 回边不影响分层：A 在第 0 层，B 第 1 层，C 第 2 层，从左到右
	var ax, bx, cx float64
	for id, e := range ix.byID {
		switch ix.label(id) {
		case "开始":
			ax = num(e, "x")
		case "是否通过":
			if str(e, "type") == "diamond" {
				bx = num(e, "x")
			}
		case "结束":
			cx = num(e, "x")
		}
	}
	if !(ax < bx && bx < cx) {
		t.Fatalf("横向分层不对: %v %v %v", ax, bx, cx)
	}
	sum := Summary(s)
	if !strings.Contains(sum, "共 3 个图形、0 段文字、3 条连线") || !strings.Contains(sum, `"是否通过"`) {
		t.Fatalf("摘要不对:\n%s", sum)
	}
}

func TestEditOps(t *testing.T) {
	s := &Scene{}
	x, y := 10.0, 20.0
	res, err := Apply(s, EditInput{Add: []AddItem{
		{Key: "a", Type: "rectangle", Text: "需求", X: &x, Y: &y},
		{Key: "b", Type: "ellipse", Text: "设计"},
		{Type: "arrow", From: "a", To: "b", Text: "然后"},
		{Type: "text", Text: "备注"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := res.Added["a"], res.Added["b"]
	ix := index(s)
	if num(ix.byID[a], "x") != 10 || ix.label(a) != "需求" || ix.label(b) != "设计" {
		t.Fatal("新增图形不对")
	}
	// 连线双向绑定
	var arrow map[string]any
	for _, e := range s.Elements {
		if str(e, "type") == "arrow" {
			arrow = e
		}
	}
	if binding(arrow, "startBinding") != a || binding(arrow, "endBinding") != b || ix.label(str(arrow, "id")) != "然后" {
		t.Fatal("连线绑定或标签不对")
	}
	nt := "新需求"
	nx := 300.0
	if _, err := Apply(s, EditInput{Update: []UpdateItem{{ID: a, Text: &nt, X: &nx}}}); err != nil {
		t.Fatal(err)
	}
	ix = index(s)
	if ix.label(a) != "新需求" || num(ix.byID[a], "x") != 300 {
		t.Fatal("修改没生效")
	}
	if tx := num(ix.textOf[a], "x"); tx < 300 {
		t.Fatalf("文字应跟着容器移动: %v", tx)
	}
	if _, err := Apply(s, EditInput{Delete: []string{a}}); err != nil {
		t.Fatal(err)
	}
	ix = index(s)
	if ix.byID[a] != nil || ix.textOf[a] != nil || binding(ix.byID[str(arrow, "id")], "startBinding") != "" {
		t.Fatal("删除图形应同时删掉里面的文字并解除连线绑定")
	}
	if _, err := Apply(s, EditInput{Delete: []string{"nope"}}); err == nil {
		t.Fatal("删除不存在的元素应报错")
	}
	if _, err := Apply(s, EditInput{Add: []AddItem{{Type: "star"}}}); err == nil {
		t.Fatal("不支持的类型应报错")
	}
	if _, err := ParseMermaid("sequenceDiagram\nA->>B: hi"); err == nil {
		t.Fatal("非流程图应报错")
	}
}
