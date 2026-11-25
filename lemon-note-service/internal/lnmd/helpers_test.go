package lnmd

import (
	"encoding/json"
	"testing"
)

func txt(s string, marks ...Mark) Node { return Node{Type: tText, Text: s, Marks: marks} }

func mk(t string, kv ...string) Mark {
	m := Mark{Type: t}
	for i := 0; i+1 < len(kv); i += 2 {
		if m.Attrs == nil {
			m.Attrs = map[string]any{}
		}
		m.Attrs[kv[i]] = kv[i+1]
	}
	return m
}

func para(content ...Node) Node { return Node{Type: tParagraph, Content: content} }

func nd(t string, attrs map[string]any, content ...Node) Node {
	return Node{Type: t, Attrs: attrs, Content: content}
}

func li(content ...Node) Node { return Node{Type: tListItem, Content: content} }

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var testOpts = RenderOptions{
	PageTitle: func(id string) (string, bool) {
		switch id {
		case "p1":
			return "子页面 *标题*", true
		case "p2":
			return "Weird <title> | [x]", true
		}
		return "", false
	},
	UserName: func(id string) (string, bool) {
		if id == "u1" {
			return "张三", true
		}
		return "", false
	},
}

// checkStable 断言 RenderBlock 稳定：渲染 → 解析得到恰好一块 → 再渲染文本不变，且再来一轮仍不变。
func checkStable(t *testing.T, name string, n Node, o RenderOptions) string {
	t.Helper()
	md1 := RenderBlock(n, o)
	bs, err := Parse(md1)
	if err != nil {
		t.Errorf("%s: 解析出错 %v", name, err)
		return md1
	}
	if len(bs) != 1 {
		t.Errorf("%s: 期望解析出 1 块，得到 %d 块\n--- Markdown:\n%s\n--- 块:\n%s", name, len(bs), md1, js(bs))
		return md1
	}
	md2 := RenderBlock(bs[0], o)
	if md1 != md2 {
		t.Errorf("%s: 渲染不稳定\n--- 第一次:\n%s\n--- 第二次:\n%s\n--- 原节点:\n%s\n--- 解析:\n%s", name, md1, md2, js(n), js(bs[0]))
		return md1
	}
	bs2, _ := Parse(md2)
	if len(bs2) != 1 || RenderBlock(bs2[0], o) != md2 {
		t.Errorf("%s: 第三轮不稳定\n%s", name, md2)
	}
	return md1
}

// checkPage 断言整页往返：块数一致且逐块渲染文本一致。
func checkPage(t *testing.T, name string, blocks []Node, o RenderOptions) {
	t.Helper()
	md := Render(blocks, o)
	bs, err := Parse(md)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(bs) != len(blocks) {
		t.Errorf("%s: 块数 %d → %d\n--- Markdown:\n%s\n--- 块:\n%s", name, len(blocks), len(bs), md, js(bs))
		return
	}
	for i := range blocks {
		a, b := RenderBlock(blocks[i], o), RenderBlock(bs[i], o)
		if a != b {
			t.Errorf("%s: 第 %d 块不一致\n--- 原:\n%s\n--- 新:\n%s", name, i, a, b)
		}
	}
}

func mustParse(t *testing.T, md string) []Node {
	t.Helper()
	bs, err := Parse(md)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}
