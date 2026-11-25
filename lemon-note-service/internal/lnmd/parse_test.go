package lnmd

import (
	"strings"
	"testing"
)

func types(bs []Node) string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Type)
	}
	return strings.Join(out, ",")
}

// inlineText 把行内内容拼成便于断言的文本：文字原样，换行写 ⏎，提及写 @kind:id，公式写 $latex$。
func inlineText(ns []Node) string {
	var b strings.Builder
	for _, n := range ns {
		switch n.Type {
		case tText:
			b.WriteString(n.Text)
		case tHardBreak:
			b.WriteString("⏎")
		case tMention:
			b.WriteString("@" + attrStr(n, "kind") + ":" + attrStr(n, "id") + attrStr(n, "date"))
		case tMathInline:
			b.WriteString("$" + attrStr(n, "latex") + "$")
		}
	}
	return b.String()
}

func marksOf(n Node) string {
	var out []string
	for _, m := range n.Marks {
		s := m.Type
		if v := markAttrStr(m, "href"); v != "" {
			s += "=" + v
		}
		if v := markAttrStr(m, "color"); v != "" {
			s += "=" + v
		}
		out = append(out, s)
	}
	return strings.Join(out, "+")
}

// noPseudo 断言解析结果里没有内部伪节点。
func noPseudo(t *testing.T, name string, ns []Node) {
	t.Helper()
	for _, n := range ns {
		if strings.HasPrefix(n.Type, "\x00") {
			t.Errorf("%s: 结果里出现内部节点 %q", name, n.Type)
		}
		noPseudo(t, name, n.Content)
	}
}

func TestParseLLMStyle(t *testing.T) {
	cases := []struct {
		name  string
		md    string
		check func(t *testing.T, bs []Node)
	}{
		{"标注框写在一行", `<callout icon="💡">一句话</callout>`, func(t *testing.T, bs []Node) {
			if types(bs) != tCallout || attrStr(bs[0], "icon") != "💡" || inlineText(bs[0].Content[0].Content) != "一句话" {
				t.Fatal(js(bs))
			}
		}},
		{"标签前后缺空行", "前文\n<callout color=\"blue\">\n内容\n</callout>\n后文", func(t *testing.T, bs []Node) {
			if types(bs) != "paragraph,callout,paragraph" || attrStr(bs[1], "color") != "blue" {
				t.Fatal(js(bs))
			}
		}},
		{"段落中间夹标签", "前文 <callout>框</callout> 后文", func(t *testing.T, bs []Node) {
			if types(bs) != "paragraph,callout,paragraph" || inlineText(bs[2].Content) != "后文" {
				t.Fatal(js(bs))
			}
		}},
		{"折叠写在一行", `<details><summary>标题</summary>内容</details>`, func(t *testing.T, bs []Node) {
			if types(bs) != tToggle || inlineText(bs[0].Content[0].Content) != "标题" || types(bs[0].Content[1].Content) != tParagraph {
				t.Fatal(js(bs))
			}
		}},
		{"折叠缩进排版", "<details open>\n  <summary>\n    标题\n  </summary>\n  - a\n  - b\n</details>", func(t *testing.T, bs []Node) {
			if types(bs) != tToggle || !attrBool(bs[0], "open") || inlineText(bs[0].Content[0].Content) != "标题" ||
				types(bs[0].Content[1].Content) != tBulletList {
				t.Fatal(js(bs))
			}
		}},
		{"折叠标题", `<details heading="1"><summary>**粗**标题</summary></details>`, func(t *testing.T, bs []Node) {
			title := bs[0].Content[0].Content
			if attrInt(bs[0], "level", 0) != 1 || inlineText(title) != "粗标题" || marksOf(title[0]) != mBold {
				t.Fatal(js(bs))
			}
		}},
		{"分栏缩进排版", "<columns>\n  <column width=\"0.3\">\n    左边\n\n    - 列表\n  </column>\n  <column>\n    右边\n  </column>\n</columns>", func(t *testing.T, bs []Node) {
			if types(bs) != tColumns || len(bs[0].Content) != 2 || types(bs[0].Content[0].Content) != "paragraph,bulletList" {
				t.Fatal(js(bs))
			}
			if w, _ := attrNum(bs[0].Content[0], "width"); w != 0.3 {
				t.Fatal(js(bs))
			}
		}},
		{"分栏一行", "<columns><column>左</column><column>右</column></columns>", func(t *testing.T, bs []Node) {
			if types(bs) != tColumns || len(bs[0].Content) != 2 || inlineText(bs[0].Content[1].Content[0].Content) != "右" {
				t.Fatal(js(bs))
			}
		}},
		{"分栏缺列标签", "<columns>\n甲\n\n乙\n</columns>", func(t *testing.T, bs []Node) {
			if types(bs) != tColumns || len(bs[0].Content) != 1 || len(bs[0].Content[0].Content) != 2 {
				t.Fatal(js(bs))
			}
		}},
		{"孤立列", "<column>a</column><column>b</column>", func(t *testing.T, bs []Node) {
			if types(bs) != tColumns || len(bs[0].Content) != 2 {
				t.Fatal(js(bs))
			}
		}},
		{"中文标点粗体", "**注意：**内容很重要，说明**“重点”**。（**括号**）", func(t *testing.T, bs []Node) {
			c := bs[0].Content
			if len(c) != 6 || c[0].Text != "注意：" || marksOf(c[0]) != mBold || c[2].Text != "“重点”" || marksOf(c[2]) != mBold || c[4].Text != "括号" {
				t.Fatal(js(bs))
			}
		}},
		{"标签不带斜杠", "<toc>\n<breadcrumb></breadcrumb>\n<empty-block>", func(t *testing.T, bs []Node) {
			if types(bs) != "tableOfContents,breadcrumb,paragraph" || len(bs[2].Content) != 0 {
				t.Fatal(js(bs))
			}
		}},
		{"成对的媒体标签", `<video src="a.mp4"></video><video src="b.mp4">演示</video><audio src="c.mp3"/>`, func(t *testing.T, bs []Node) {
			if types(bs) != "video,video,audio" || attrStr(bs[1], "title") != "演示" || attrStr(bs[0], "src") != "a.mp4" {
				t.Fatal(js(bs))
			}
		}},
		{"页面类标签", `<page id="p">标题</page><page>新页面</page><link-to-page id="x"/><link-to-page id="y">T</link-to-page><database id="d" view="v">表</database>`, func(t *testing.T, bs []Node) {
			if types(bs) != "childPage,childPage,linkToPage,linkToPage,childDatabase" {
				t.Fatal(js(bs))
			}
			if attrStr(bs[0], "pageId") != "p" || attrStr(bs[0], "title") != "" || attrStr(bs[1], "title") != "新页面" ||
				attrStr(bs[3], "pageId") != "y" || attrStr(bs[4], "viewId") != "v" {
				t.Fatal(js(bs))
			}
		}},
		{"同步块", "<synced-block id=\"s1\">\n共享\n</synced-block>\n<synced-block source=\"s1\"/>\n<synced-block source=\"s2\">", func(t *testing.T, bs []Node) {
			if types(bs) != "syncedBlock,syncedBlock,syncedBlock" || nodeID(bs[0]) != "s1" || attrStr(bs[1], "sourceId") != "s1" ||
				attrStr(bs[2], "sourceId") != "s2" || len(bs[0].Content) != 1 {
				t.Fatal(js(bs))
			}
		}},
		{"白板", `<whiteboard id="w1" title="架构"/>`, func(t *testing.T, bs []Node) {
			if types(bs) != tWhiteboard || nodeID(bs[0]) != "w1" || attrStr(bs[0], "title") != "架构" {
				t.Fatal(js(bs))
			}
		}},
		{"附件书签嵌入", `<file src="lemon://file/1" name="a.pdf" size="1024" mime="application/pdf"/>` + "\n" +
			`<bookmark url="https://a.com" title="A"/><embed url="https://b.com" height="400"/>`, func(t *testing.T, bs []Node) {
			if types(bs) != "fileBlock,bookmark,embed" {
				t.Fatal(js(bs))
			}
			if f, _ := attrNum(bs[0], "size"); f != 1024 {
				t.Fatal(js(bs))
			}
			if h, _ := attrNum(bs[2], "height"); h != 400 {
				t.Fatal(js(bs))
			}
		}},
		{"大写标签与中文引号", `<CALLOUT ICON=“💡” Color='red'>x</CALLOUT>`, func(t *testing.T, bs []Node) {
			if types(bs) != tCallout || attrStr(bs[0], "icon") != "💡" || attrStr(bs[0], "color") != "red" {
				t.Fatal(js(bs))
			}
		}},
		{"缺闭合标签", "<callout>\n内容\n\n更多", func(t *testing.T, bs []Node) {
			if types(bs) != tCallout || len(bs[0].Content) != 2 {
				t.Fatal(js(bs))
			}
		}},
		{"多余闭合标签", "内容\n</callout>\n后文", func(t *testing.T, bs []Node) {
			if types(bs) != "paragraph,paragraph" {
				t.Fatal(js(bs))
			}
		}},
		{"孤立 summary", "<summary>标题</summary>", func(t *testing.T, bs []Node) {
			if types(bs) != tParagraph || inlineText(bs[0].Content) != "标题" {
				t.Fatal(js(bs))
			}
		}},
		{"嵌套容器一行", `<columns><column><details><summary>T</summary><callout>深</callout></details></column></columns>`, func(t *testing.T, bs []Node) {
			tg := bs[0].Content[0].Content[0]
			if tg.Type != tToggle || tg.Content[1].Content[0].Type != tCallout {
				t.Fatal(js(bs))
			}
		}},
		{"列表项里的标签", "- 第一项\n- <callout>框</callout>\n- 第三项", func(t *testing.T, bs []Node) {
			if types(bs) != tBulletList || len(bs[0].Content) != 3 || types(bs[0].Content[1].Content) != "paragraph,callout" {
				t.Fatal(js(bs))
			}
		}},
		{"列表项续行标签", "- 项\n  <callout>\n  框\n  </callout>\n- 下一项", func(t *testing.T, bs []Node) {
			if types(bs) != tBulletList || len(bs[0].Content) != 2 || types(bs[0].Content[0].Content) != "paragraph,callout" {
				t.Fatal(js(bs))
			}
		}},
		{"引用里的标签", "> <callout>框</callout>\n> 后文", func(t *testing.T, bs []Node) {
			if types(bs) != tBlockquote || types(bs[0].Content) != "callout,paragraph" {
				t.Fatal(js(bs))
			}
		}},
		{"转义的标签", `\<callout>不是标签\</callout>`, func(t *testing.T, bs []Node) {
			if types(bs) != tParagraph || inlineText(bs[0].Content) != "<callout>不是标签</callout>" {
				t.Fatal(js(bs))
			}
		}},
		{"代码里的标签", "```html\n<callout>\n<details>\n```\n\n`<callout>x</callout>`", func(t *testing.T, bs []Node) {
			if types(bs) != "codeBlock,paragraph" || codeText(bs[0]) != "<callout>\n<details>" || bs[1].Content[0].Text != "<callout>x</callout>" {
				t.Fatal(js(bs))
			}
		}},
		{"不认识的 HTML 保留原文", "<div>hi</div>\n\n按 <kbd>Ctrl</kbd>+C <!-- 注释 --> <img src=\"x\">", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "<div>hi</div>" || inlineText(bs[1].Content) != `按 <kbd>Ctrl</kbd>+C <!-- 注释 --> <img src="x">` {
				t.Fatal(js(bs))
			}
		}},
		{"行内 HTML 标记", `<u>下</u><ins>划</ins><s>删</s><del>除</del><b>粗</b><strong>体</strong><i>斜</i><em>体</em><code>码</code>H<sub>2</sub>x<sup>2</sup><mark>亮</mark><mark color="red">红亮</mark><span color="blue">蓝</span><span style="color: green">绿</span><font color="gray">灰</font><a href="https://a.com">链</a><span>无色</span>`, func(t *testing.T, bs []Node) {
			// 相邻同标记的文字会合并：「下」「划」合成一段
			want := []string{"underline", "strike", "bold", "italic", "code", "", "subscript", "", "superscript",
				"highlight", "highlight=red", "textColor=blue", "textColor=green", "textColor=gray", "link=https://a.com", ""}
			c := bs[0].Content
			if len(c) != len(want) {
				t.Fatalf("%d %s", len(c), js(bs))
			}
			for i, w := range want {
				if marksOf(c[i]) != w {
					t.Fatalf("第 %d 段 %q 期望 %q 得到 %q", i, c[i].Text, w, marksOf(c[i]))
				}
			}
		}},
		{"交错的 HTML 标记", `<u>a<b>b</u>c</b>`, func(t *testing.T, bs []Node) {
			c := bs[0].Content
			if len(c) != 3 || marksOf(c[0]) != "underline" || marksOf(c[1]) != "bold+underline" || marksOf(c[2]) != "bold" {
				t.Fatal(js(bs))
			}
		}},
		{"换行写法", "一<br>二<br/>三<BR />四\n五  \n六\\\n七", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "一⏎二⏎三⏎四⏎五⏎六⏎七" {
				t.Fatal(inlineText(bs[0].Content))
			}
		}},
		{"提及", `见 <mention-page id="p1">旧标题</mention-page>，找 <mention-user id="u1">名</mention-user>，<mention-date date="2026-11-25" end="2026-11-26"/>，<mention-page id="p2"/>`, func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "见 @page:p1，找 @user:u1，@date:2026-11-25，@page:p2" {
				t.Fatal(inlineText(bs[0].Content))
			}
			for _, n := range bs[0].Content {
				if n.Type == tMention && attrStr(n, "kind") == "date" && attrStr(n, "endDate") != "2026-11-26" {
					t.Fatal(js(n))
				}
			}
		}},
		{"金额不是公式", "价格 $5 到 $10。", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "价格 $5 到 $10。" {
				t.Fatal(js(bs))
			}
		}},
		{"金额与公式混排", "从 $5 到 $10，公式 $x^2$ 与 $$E=mc^2$$", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "从 $5 到 $10，公式 $x^2$ 与 $E=mc^2$" {
				t.Fatal(inlineText(bs[0].Content))
			}
		}},
		{"公式规则", "$ x$ 与 $x $ 与 $x$5 与 $a\\$b$", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != `$ x$ 与 $x $ 与 $x$5 与 $a\$b$` {
				t.Fatal(js(bs))
			}
			last := bs[0].Content[len(bs[0].Content)-1]
			if last.Type != tMathInline || attrStr(last, "latex") != `a\$b` {
				t.Fatal(js(bs))
			}
		}},
		{"公式块写法", "$$E=mc^2$$\n\n$$\n\\int x\n$$\n\n```math\na\n```\n\n$$ a +\nb $$", func(t *testing.T, bs []Node) {
			if types(bs) != "mathBlock,mathBlock,mathBlock,mathBlock" || attrStr(bs[0], "latex") != "E=mc^2" ||
				attrStr(bs[1], "latex") != `\int x` || attrStr(bs[3], "latex") != "a +\nb" {
				t.Fatal(js(bs))
			}
		}},
		{"块颜色尾注", "段落 {color=\"gray_background\"}\n\n# 标题 {color=\"red\" align=\"center\"}\n\n- 项 {color=red}\n\n> 引 {color=\"blue\"}\n\n多行段落\n{color=\"green\"}", func(t *testing.T, bs []Node) {
			if attrStr(bs[0], "color") != "gray_background" || inlineText(bs[0].Content) != "段落" {
				t.Fatal(js(bs[0]))
			}
			if attrStr(bs[1], "color") != "red" || attrStr(bs[1], "textAlign") != "center" || inlineText(bs[1].Content) != "标题" {
				t.Fatal(js(bs[1]))
			}
			if attrStr(bs[2].Content[0].Content[0], "color") != "red" {
				t.Fatal(js(bs[2]))
			}
			if attrStr(bs[3].Content[0], "color") != "blue" {
				t.Fatal(js(bs[3]))
			}
			if attrStr(bs[4], "color") != "green" || inlineText(bs[4].Content) != "多行段落" {
				t.Fatal(js(bs[4]))
			}
		}},
		{"不是尾注", "{.class}\n\n{color=\"red\"}\n\n代码 `{color=\"red\"}`", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "{.class}" || inlineText(bs[1].Content) != `{color="red"}` || len(bs[1].Attrs) != 0 {
				t.Fatal(js(bs))
			}
		}},
		{"图片尺寸", `![说明](lemon://file/abc "标题"){width=480 align="center"}`, func(t *testing.T, bs []Node) {
			w, _ := attrNum(bs[0], "width")
			if types(bs) != tImage || w != 480 || attrStr(bs[0], "align") != "center" || attrStr(bs[0], "title") != "标题" || attrStr(bs[0], "alt") != "说明" {
				t.Fatal(js(bs))
			}
		}},
		{"段落里的图片拆开", "前 ![i](x.png) 后", func(t *testing.T, bs []Node) {
			if types(bs) != "paragraph,image,paragraph" {
				t.Fatal(js(bs))
			}
		}},
		{"任务与普通列表混排", "- a\n- [ ] b\n- [X] c\n- d", func(t *testing.T, bs []Node) {
			if types(bs) != "bulletList,taskList,bulletList" || len(bs[1].Content) != 2 || !attrBool(bs[1].Content[1], "checked") {
				t.Fatal(js(bs))
			}
		}},
		{"松散有序列表", "1. a\n\n2. b\n\n7. c", func(t *testing.T, bs []Node) {
			if types(bs) != "orderedList,orderedList,orderedList" || attrInt(bs[1], "start", 0) != 2 || attrInt(bs[2], "start", 0) != 7 {
				t.Fatal(js(bs))
			}
		}},
		{"有序列表起始与括号", "5) a\n6) b", func(t *testing.T, bs []Node) {
			if types(bs) != tOrderedList || attrInt(bs[0], "start", 0) != 5 || len(bs[0].Content) != 2 {
				t.Fatal(js(bs))
			}
		}},
		{"星号加号列表", "* a\n* b\n\n+ c", func(t *testing.T, bs []Node) {
			if types(bs) != "bulletList,bulletList" {
				t.Fatal(js(bs))
			}
		}},
		{"链接开头的列表项不是任务", "- [x](https://a.com)\n- [ ]不是任务", func(t *testing.T, bs []Node) {
			if types(bs) != tBulletList || marksOf(bs[0].Content[0].Content[0].Content[0]) != "link=https://a.com" {
				t.Fatal(js(bs))
			}
		}},
		{"软换行", "一\n二", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "一⏎二" {
				t.Fatal(js(bs))
			}
		}},
		{"标题层级", "标题\n===\n\n副标题\n---\n\n#### 四级\n\n###### 六级", func(t *testing.T, bs []Node) {
			if types(bs) != "heading,heading,heading,heading" || attrInt(bs[0], "level", 0) != 1 || attrInt(bs[1], "level", 0) != 2 ||
				attrInt(bs[2], "level", 0) != 3 || attrInt(bs[3], "level", 0) != 3 {
				t.Fatal(js(bs))
			}
		}},
		{"图表 JSON 错误降级", "```chart\n{not json\n```\n\n```chart\n{\"type\":\"line\"}\n```", func(t *testing.T, bs []Node) {
			if types(bs) != "codeBlock,chart" || attrStr(bs[0], "language") != "chart" || codeText(bs[0]) != "{not json" {
				t.Fatal(js(bs))
			}
		}},
		{"思维导图同级根", "```mindmap\n中心\n- 甲\n  - 甲一\n- 乙\n```", func(t *testing.T, bs []Node) {
			root := mindmapRoot(bs[0].Attrs["data"])
			if mindmapText(root) != "中心" || len(mindmapChildren(root)) != 2 || len(mindmapChildren(mindmapChildren(root)[0])) != 1 {
				t.Fatal(js(bs))
			}
		}},
		{"Graphviz 引擎", "```dot engine=neato\ngraph{}\n```\n\n```graphviz\ndigraph{}\n```", func(t *testing.T, bs []Node) {
			if attrStr(bs[0], "engine") != "neato" || attrStr(bs[1], "engine") != "dot" || attrStr(bs[0], "code") != "graph{}" {
				t.Fatal(js(bs))
			}
		}},
		{"表格", "| 名 | 值 |\n|:--|--:|\n| a\\|b | 1<br>2 |\n| `x\\|y` | <span color=\"red\">红</span> |", func(t *testing.T, bs []Node) {
			rows := bs[0].Content
			if len(rows) != 3 || rows[0].Content[0].Type != tTableHeader || rows[1].Content[0].Type != tTableCell {
				t.Fatal(js(bs))
			}
			if inlineText(rows[1].Content[0].Content[0].Content) != "a|b" || inlineText(rows[1].Content[1].Content[0].Content) != "1⏎2" ||
				inlineText(rows[2].Content[0].Content[0].Content) != "x|y" {
				t.Fatal(js(bs))
			}
		}},
		{"CRLF 换行", "# 标题\r\n\r\n段落\r\n<callout>\r\n内容\r\n</callout>\r\n", func(t *testing.T, bs []Node) {
			if types(bs) != "heading,paragraph,callout" {
				t.Fatal(js(bs))
			}
		}},
		{"大模型缩进代码块", "<callout>\n    缩进的正文\n</callout>", func(t *testing.T, bs []Node) {
			if types(bs[0].Content) != tParagraph {
				t.Fatal(js(bs))
			}
		}},
		{"空列表项", "-\n- [ ]\n1.", func(t *testing.T, bs []Node) {
			if types(bs) != "bulletList,taskList,orderedList" {
				t.Fatal(js(bs))
			}
		}},
		{"私用区字符原样保留", "ab `cd`", func(t *testing.T, bs []Node) {
			if bs[0].Content[0].Text != "ab " || bs[0].Content[1].Text != "cd" {
				t.Fatal(js(bs))
			}
		}},
		{"无效 UTF-8", "a\xffb", func(t *testing.T, bs []Node) {
			if inlineText(bs[0].Content) != "a�b" {
				t.Fatal(js(bs))
			}
		}},
		{"链接引用定义按原文保留", "[a]: https://x.com\n\n[a]", func(t *testing.T, bs []Node) {
			if types(bs) != "paragraph,paragraph" || inlineText(bs[0].Content) != "[a]: https://x.com" {
				t.Fatal(js(bs))
			}
		}},
		{"自动链接", "<https://a.com> 与 <me@a.com> 与 https://b.com", func(t *testing.T, bs []Node) {
			c := bs[0].Content
			if marksOf(c[0]) != "link=https://a.com" || marksOf(c[2]) != "link=mailto:me@a.com" || c[3].Text != " 与 https://b.com" {
				t.Fatal(js(bs))
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bs := mustParse(t, c.md)
			noPseudo(t, c.name, bs)
			c.check(t, bs)
			// 大模型写法解析后的结果也必须能稳定往返
			for i, b := range bs {
				checkStable(t, c.name+"/"+b.Type+"/"+string(rune('0'+i%10)), b, RenderOptions{})
			}
		})
	}
}

func TestPlainText(t *testing.T) {
	bs := mustParse(t, "# 标题\n\n正文 **粗** $x^2$ <mention-date date=\"2026-11-25\"/>\n\n- a\n- b\n\n| h1 | h2 |\n| --- | --- |\n| c | d |\n\n```go\ncode\n```\n\n```mindmap\n- 根\n  - 叶\n```")
	got := make([]string, len(bs))
	for i, b := range bs {
		got[i] = PlainText(b)
	}
	want := []string{"标题", "正文 粗 x^2 2026-11-25", "a\nb", "h1\th2\nc\td", "code", "根\n叶"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("得到 %q", got)
	}
	wb := Node{Type: tWhiteboard, Attrs: map[string]any{"title": "图", "data": map[string]any{"elements": []any{map[string]any{"text": "方框"}}}}}
	if PlainText(wb) != "图\n方框" {
		t.Fatalf("%q", PlainText(wb))
	}
}
