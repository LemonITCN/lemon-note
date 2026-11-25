package lnmd

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// blockCases 覆盖节点目录里的每一种节点。
func blockCases() []struct {
	name string
	n    Node
} {
	p := para(txt("正文"))
	return []struct {
		name string
		n    Node
	}{
		{"段落", para(txt("你好，世界"))},
		{"空段落", para()},
		{"空白段落", para(txt("   "))},
		{"段落颜色", nd(tParagraph, map[string]any{"color": "gray_background"}, txt("灰底"))},
		{"段落颜色对齐", nd(tParagraph, map[string]any{"color": "red", "textAlign": "center"}, txt("居中"))},
		{"空段落颜色", nd(tParagraph, map[string]any{"color": "blue"})},
		{"段落左对齐默认", nd(tParagraph, map[string]any{"textAlign": "left"}, txt("x"))},
		{"标题1", nd(tHeading, map[string]any{"level": 1}, txt("一级"))},
		{"标题2", nd(tHeading, map[string]any{"level": 2.0}, txt("二级"))},
		{"标题3颜色", nd(tHeading, map[string]any{"level": 3, "color": "blue"}, txt("三级"))},
		{"标题越界", nd(tHeading, map[string]any{"level": 6}, txt("六级"))},
		{"空标题", nd(tHeading, map[string]any{"level": 2})},
		{"标题含井号", nd(tHeading, map[string]any{"level": 1}, txt("C# 与 F# ##"))},
		{"无序列表", nd(tBulletList, nil, li(para(txt("a"))), li(para(txt("b"))))},
		{"嵌套列表", nd(tBulletList, nil, li(para(txt("a")), nd(tBulletList, nil, li(para(txt("a1"))), li(para(txt("a2"))))), li(para(txt("b"))))},
		{"有序列表", nd(tOrderedList, map[string]any{"start": 1}, li(para(txt("一"))), li(para(txt("二"))))},
		{"有序列表起始", nd(tOrderedList, map[string]any{"start": 9.0}, li(para(txt("九"))), li(para(txt("十")), nd(tBulletList, nil, li(para(txt("子"))))), li(para(txt("十一"))))},
		{"有序列表嵌套有序非1", nd(tBulletList, nil, li(para(txt("a")), nd(tOrderedList, map[string]any{"start": 3}, li(para(txt("c"))))))},
		{"列表项多段", nd(tBulletList, nil, li(para(txt("第一段")), para(txt("第二段"))), li(para(txt("下一项"))))},
		{"列表项代码块", nd(tBulletList, nil, li(para(txt("看代码")), nd(tCodeBlock, map[string]any{"language": "go"}, txt("func main() {\n\n\tprintln(1)\n}"))))},
		{"空列表项", nd(tBulletList, nil, li(para()), li(para(txt("x"))))},
		{"空列表项带子块", nd(tBulletList, nil, li(para(), nd(tBulletList, nil, li(para(txt("子"))))))},
		{"列表项首块非段落", nd(tBulletList, nil, li(nd(tCallout, nil, p)))},
		{"列表项颜色", nd(tBulletList, nil, li(nd(tParagraph, map[string]any{"color": "red"}, txt("红"))))},
		{"空列表", nd(tBulletList, nil)},
		{"两个相邻子列表", nd(tBulletList, nil, li(para(txt("a")), nd(tBulletList, nil, li(para(txt("x")))), nd(tBulletList, nil, li(para(txt("y"))))))},
		{"任务列表", nd(tTaskList, nil, nd(tTaskItem, map[string]any{"checked": false}, para(txt("待办"))), nd(tTaskItem, map[string]any{"checked": true}, para(txt("完成"))))},
		{"空任务项", nd(tTaskList, nil, nd(tTaskItem, map[string]any{"checked": true}, para()))},
		{"任务项带子块", nd(tTaskList, nil, nd(tTaskItem, nil, para(txt("父")), nd(tTaskList, nil, nd(tTaskItem, map[string]any{"checked": true}, para(txt("子"))))))},
		{"任务项文字像任务框", nd(tTaskList, nil, nd(tTaskItem, nil, para(txt("[x] 不是勾"))))},
		{"列表项文字像任务框", nd(tBulletList, nil, li(para(txt("[ ] 普通"))))},
		{"折叠", nd(tToggle, nil, nd(tToggleTitle, nil, txt("标题")), nd(tToggleBody, nil, para(txt("内容"))))},
		{"折叠展开标题", nd(tToggle, map[string]any{"open": true, "level": 2}, nd(tToggleTitle, nil, txt("二级"), txt("粗", mk(mBold))), nd(tToggleBody, nil, para(txt("内容")), nd(tBulletList, nil, li(para(txt("x"))))))},
		{"空折叠", nd(tToggle, nil, nd(tToggleTitle, nil), nd(tToggleBody, nil))},
		{"折叠颜色", nd(tToggle, map[string]any{"color": "red_background"}, nd(tToggleTitle, nil, txt("- 像列表的标题")), nd(tToggleBody, nil))},
		{"折叠嵌套", nd(tToggle, nil, nd(tToggleTitle, nil, txt("外")), nd(tToggleBody, nil, nd(tToggle, nil, nd(tToggleTitle, nil, txt("内")), nd(tToggleBody, nil, para(txt("深"))))))},
		{"引用", nd(tBlockquote, nil, para(txt("引文")))},
		{"引用多段", nd(tBlockquote, nil, para(txt("一")), para(txt("二")), nd(tBulletList, nil, li(para(txt("三")))))},
		{"引用嵌套", nd(tBlockquote, nil, nd(tBlockquote, nil, para(txt("深"))))},
		{"引用含代码", nd(tBlockquote, nil, nd(tCodeBlock, nil, txt("  缩进\n\n空行后")))},
		{"空引用", nd(tBlockquote, nil)},
		{"标注框", nd(tCallout, map[string]any{"icon": "💡", "color": "blue_background"}, para(txt("提示")))},
		{"标注框无属性", nd(tCallout, nil, para(txt("a")), para(txt("b")))},
		{"空标注框", nd(tCallout, map[string]any{"icon": "lightbulb"})},
		{"标注框属性含引号", nd(tCallout, map[string]any{"icon": `a"b<c>&d`}, p)},
		{"标注框嵌套列表", nd(tCallout, nil, nd(tBulletList, nil, li(para(txt("a"))), li(para(txt("b")))))},
		{"代码块", nd(tCodeBlock, map[string]any{"language": "python"}, txt("print('hi')\n"))},
		{"代码块含围栏", nd(tCodeBlock, map[string]any{"language": "markdown"}, txt("```go\nx\n```\n````"))},
		{"代码块含标签", nd(tCodeBlock, nil, txt("<callout>\n</callout>\n$$\nx\n$$\n<details>"))},
		{"空代码块", nd(tCodeBlock, nil)},
		{"代码块只有换行", nd(tCodeBlock, nil, txt("\n"))},
		{"代码块首尾空行", nd(tCodeBlock, nil, txt("\n\nx\n\n"))},
		{"代码块语言含空格", nd(tCodeBlock, map[string]any{"language": "js title=a.js"}, txt("x"))},
		{"代码块含私用区字符", nd(tCodeBlock, nil, txt("\uE000 0\uE001"))},
		{"分隔线", nd(tHorizontalRule, nil)},
		{"图片", nd(tImage, map[string]any{"src": "lemon://file/abc", "alt": "说明", "title": "标题"})},
		{"图片尺寸", nd(tImage, map[string]any{"src": "lemon://file/abc", "width": 480.0, "align": "center"})},
		{"图片特殊地址", nd(tImage, map[string]any{"src": "https://x.com/a b(1).png?x=1&amp;y", "alt": "a]b*c"})},
		{"图片空地址", nd(tImage, map[string]any{"alt": "无"})},
		{"图片空地址带标题", nd(tImage, map[string]any{"title": "t \"q\""})},
		{"图片宽度字符串", nd(tImage, map[string]any{"src": "x", "width": "50%"})},
		{"附件", nd(tFileBlock, map[string]any{"src": "lemon://file/1", "name": "报告.pdf", "size": 12345.0, "mime": "application/pdf"})},
		{"视频", nd(tVideo, map[string]any{"src": "lemon://file/2", "title": "演示"})},
		{"音频", nd(tAudio, map[string]any{"src": "lemon://file/3"})},
		{"书签", nd(tBookmark, map[string]any{"url": "https://example.com/?a=1&b=2", "title": "例子", "description": "描述\n两行", "image": "https://example.com/i.png"})},
		{"嵌入", nd(tEmbed, map[string]any{"url": "https://www.youtube.com/embed/x", "height": 400})},
		{"表格", nd(tTable, nil,
			nd(tTableRow, nil, nd(tTableHeader, nil, para(txt("名"))), nd(tTableHeader, nil, para(txt("值")))),
			nd(tTableRow, nil, nd(tTableCell, nil, para(txt("a|b"))), nd(tTableCell, nil, para(txt("1"), Node{Type: tHardBreak}, txt("2")))),
			nd(tTableRow, nil, nd(tTableCell, nil, para(txt("`x|y`", mk(mCode)))), nd(tTableCell, nil, para(Node{Type: tMathInline, Attrs: map[string]any{"latex": `|x| + \|y\|`}}))),
		)},
		{"表格无表头行", nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(txt("a"))), nd(tTableCell, nil, para(txt("b")))))},
		{"表格合并单元格", nd(tTable, nil,
			nd(tTableRow, nil, nd(tTableHeader, map[string]any{"colspan": 2}, para(txt("合并")))),
			nd(tTableRow, nil, nd(tTableCell, map[string]any{"rowspan": 2}, para(txt("竖"))), nd(tTableCell, nil, para(txt("b")))),
			nd(tTableRow, nil, nd(tTableCell, nil, para(txt("c")))),
		)},
		{"表格单元格多段", nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(txt("一")), para(txt("二")), para())))},
		{"表格空单元格", nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil), nd(tTableCell, nil, para())))},
		{"空表格", nd(tTable, nil)},
		{"表格单元格含标记", nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(txt("粗", mk(mBold)), txt("链", mk(mLink, "href", "http://a.b/c|d", "title", "t|t")), Node{Type: tMention, Attrs: map[string]any{"kind": "page", "id": "p2"}}))))},
		{"分栏", nd(tColumns, nil, nd(tColumn, map[string]any{"width": 0.5}, para(txt("左"))), nd(tColumn, map[string]any{"width": 0.5}, para(txt("右")), nd(tCodeBlock, nil, txt("x"))))},
		{"分栏无宽度", nd(tColumns, nil, nd(tColumn, nil, para(txt("左"))), nd(tColumn, nil))},
		{"空分栏", nd(tColumns, nil)},
		{"孤立分栏列", nd(tColumn, nil, p)},
		{"分栏宽度字符串", nd(tColumns, nil, nd(tColumn, map[string]any{"width": "0.25"}, p))},
		{"公式块", nd(tMathBlock, map[string]any{"latex": `\int_0^1 x^2 \, dx = \frac{1}{3}`})},
		{"公式块多行", nd(tMathBlock, map[string]any{"latex": "a &= b \\\\\nc &= d"})},
		{"公式块含空行", nd(tMathBlock, map[string]any{"latex": "a\n\nb"})},
		{"公式块含双美元", nd(tMathBlock, map[string]any{"latex": "x $$ y"})},
		{"空公式块", nd(tMathBlock, nil)},
		{"公式块结尾换行", nd(tMathBlock, map[string]any{"latex": "x\n"})},
		{"Mermaid", nd(tMermaid, map[string]any{"code": "graph TD\n  A-->B"})},
		{"空Mermaid", nd(tMermaid, nil)},
		{"Graphviz", nd(tGraphviz, map[string]any{"code": "digraph { a -> b }", "engine": "dot"})},
		{"Graphviz引擎", nd(tGraphviz, map[string]any{"code": "graph { a -- b }", "engine": "neato"})},
		{"思维导图", nd(tMindmap, map[string]any{"data": map[string]any{
			"layout": "logicalStructure", "theme": "default",
			"root": map[string]any{"id": "r", "text": "中心", "style": map[string]any{"color": "red"}, "children": []any{
				map[string]any{"id": "a", "text": "- 分支一", "note": "备注", "children": []any{map[string]any{"text": "叶子"}}},
				map[string]any{"text": "分支\n二"},
				map[string]any{"text": ""},
			}},
		}})},
		{"空思维导图", nd(tMindmap, nil)},
		{"白板", nd(tWhiteboard, map[string]any{"id": "wb1", "title": "架构图", "data": map[string]any{"elements": []any{}}})},
		{"图表", nd(tChart, map[string]any{"data": map[string]any{"type": "bar", "title": "<销量> & 利润", "source": map[string]any{"kind": "static", "option": map[string]any{"x": []any{1.0, 2.5, 1e21}}}}})},
		{"空图表", nd(tChart, nil)},
		{"图表含反引号", nd(tChart, map[string]any{"data": map[string]any{"t": "```"}})},
		{"子页面", nd(tChildPage, map[string]any{"pageId": "p1"})},
		{"子页面无标题", nd(tChildPage, map[string]any{"pageId": "zz"})},
		{"子页面怪标题", nd(tChildPage, map[string]any{"pageId": "p2"})},
		{"新子页面", nd(tChildPage, map[string]any{"title": "新建页"})},
		{"内嵌数据库", nd(tChildDatabase, map[string]any{"pageId": "p1", "viewId": "v1"})},
		{"页面链接", nd(tLinkToPage, map[string]any{"pageId": "p2"})},
		{"同步块原件", nd(tSyncedBlock, map[string]any{"id": "s1"}, para(txt("共享内容")), nd(tBulletList, nil, li(para(txt("x")))))},
		{"空同步块原件", nd(tSyncedBlock, map[string]any{"id": "s2"})},
		{"同步块引用", nd(tSyncedBlock, map[string]any{"sourceId": "s1"})},
		{"目录", nd(tTOC, nil)},
		{"面包屑", nd(tBreadcrumb, nil)},
		{"未知块", nd("someFutureBlock", map[string]any{"x": 1}, txt("文字"))},
		{"未知空块", nd("someFutureBlock", nil)},
		{"顶层列表项", li(para(txt("孤儿项")))},
		{"深层组合", nd(tColumns, nil,
			nd(tColumn, nil, nd(tCallout, map[string]any{"icon": "⚠️"}, nd(tToggle, nil, nd(tToggleTitle, nil, txt("折叠")), nd(tToggleBody, nil, nd(tBulletList, nil, li(para(txt("项")), nd(tCodeBlock, nil, txt("code")))))))),
			nd(tColumn, nil, nd(tBlockquote, nil, nd(tCallout, nil, nd(tTable, nil, nd(tTableRow, nil, nd(tTableHeader, nil, para(txt("h"))))))), nd(tSyncedBlock, map[string]any{"sourceId": "x"})),
		)},
		{"列表里的各种块", nd(tOrderedList, map[string]any{"start": 98}, li(para(txt("九十八")),
			nd(tCallout, nil, para(txt("框"))),
			nd(tToggle, nil, nd(tToggleTitle, nil, txt("t")), nd(tToggleBody, nil, para(txt("b")))),
			nd(tMathBlock, map[string]any{"latex": "x"}),
			nd(tImage, map[string]any{"src": "a.png"}),
			nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(txt("c"))))),
			nd(tHorizontalRule, nil),
			nd(tColumns, nil, nd(tColumn, nil, para(txt("col")))),
		), li(para(txt("九十九")), nd(tBlockquote, nil, para(txt("q")))))},
		{"引用里的标签块", nd(tBlockquote, nil, nd(tCallout, nil, para(txt("框里"))), nd(tToggle, nil, nd(tToggleTitle, nil, txt("t")), nd(tToggleBody, nil, para(txt("b")))))},
	}
}

func TestBlockRoundTrip(t *testing.T) {
	for _, c := range blockCases() {
		checkStable(t, c.name, c.n, testOpts)
		checkStable(t, c.name+"(无查询)", c.n, RenderOptions{})
	}
}

func TestWholePageRoundTrip(t *testing.T) {
	var blocks []Node
	for _, c := range blockCases() {
		blocks = append(blocks, c.n)
	}
	checkPage(t, "全部节点", blocks, testOpts)
	// 相邻的同类列表、引用、段落必须仍是独立的块
	adj := []Node{
		nd(tBulletList, nil, li(para(txt("a")))),
		nd(tBulletList, nil, li(para(txt("b")))),
		nd(tOrderedList, map[string]any{"start": 1}, li(para(txt("1")))),
		nd(tOrderedList, map[string]any{"start": 2}, li(para(txt("2")))),
		nd(tTaskList, nil, nd(tTaskItem, nil, para(txt("t")))),
		nd(tTaskList, nil, nd(tTaskItem, nil, para())),
		nd(tBulletList, nil, li(para())),
		nd(tBulletList, nil, li(para())),
		nd(tBlockquote, nil, para(txt("q1"))),
		nd(tBlockquote, nil, para(txt("q2"))),
		para(), para(), para(txt("---")), para(txt("===")),
		nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(txt("t1"))))),
		nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(txt("t2"))))),
		nd(tCodeBlock, nil), nd(tCodeBlock, nil),
		nd(tImage, map[string]any{"src": "a"}), nd(tImage, map[string]any{"src": "b"}),
	}
	checkPage(t, "相邻块", adj, testOpts)
}

// inlineCases 覆盖每种行内标记与行内节点。
func inlineCases() []struct {
	name string
	c    []Node
} {
	m := func(kind, id string) Node {
		return Node{Type: tMention, Attrs: map[string]any{"kind": kind, "id": id}}
	}
	math := func(l string) Node { return Node{Type: tMathInline, Attrs: map[string]any{"latex": l}} }
	br := Node{Type: tHardBreak}
	return []struct {
		name string
		c    []Node
	}{
		{"粗体", []Node{txt("a "), txt("粗", mk(mBold)), txt(" b")}},
		{"斜体", []Node{txt("斜", mk(mItalic))}},
		{"下划线", []Node{txt("下划", mk(mUnderline))}},
		{"删除线", []Node{txt("删", mk(mStrike)), txt("后")}},
		{"行内代码", []Node{txt("a "), txt("x := `y`", mk(mCode))}},
		{"代码首尾空格", []Node{txt(" x ", mk(mCode))}},
		{"代码全空格", []Node{txt("  ", mk(mCode))}},
		{"代码反引号开头", []Node{txt("`", mk(mCode))}},
		{"链接", []Node{txt("链接", mk(mLink, "href", "https://a.com/x_(y)?q=1&r=2"))}},
		{"链接带标题", []Node{txt("链接", mk(mLink, "href", "https://a.com", "title", `说"明"`))}},
		{"链接空地址", []Node{txt("空", mk(mLink, "href", ""))}},
		{"链接地址含空格", []Node{txt("空格", mk(mLink, "href", "file:///a b/c.txt"))}},
		{"链接地址结尾反斜杠", []Node{txt("x", mk(mLink, "href", `C:\dir\`))}},
		{"文字颜色", []Node{txt("红字", mk(mTextColor, "color", "red"))}},
		{"高亮", []Node{txt("黄底", mk(mHighlight, "color", "yellow"))}},
		{"高亮无色", []Node{txt("高亮", mk(mHighlight))}},
		{"下标上标", []Node{txt("H"), txt("2", mk(mSubscript)), txt("O x"), txt("2", mk(mSuperscript))}},
		{"批注标记不显示", []Node{txt("有批注", mk("comment", "threadId", "t1")), txt("无")}},
		{"未知标记丢弃", []Node{txt("x", mk("fancy"))}},
		{"粗斜", []Node{txt("粗斜", mk(mBold), mk(mItalic))}},
		{"粗包斜", []Node{txt("粗", mk(mBold)), txt("粗斜", mk(mBold), mk(mItalic)), txt("粗", mk(mBold))}},
		{"斜包粗", []Node{txt("斜", mk(mItalic)), txt("粗斜", mk(mBold), mk(mItalic)), txt("斜", mk(mItalic))}},
		{"交叉", []Node{txt("a", mk(mBold)), txt("b", mk(mBold), mk(mItalic)), txt("c", mk(mItalic))}},
		{"相邻不同", []Node{txt("a", mk(mBold)), txt("b", mk(mItalic)), txt("c", mk(mStrike)), txt("d", mk(mBold))}},
		{"粗体带空格", []Node{txt("a"), txt(" 粗 ", mk(mBold)), txt("b")}},
		{"粗体只有空格", []Node{txt("a"), txt(" ", mk(mBold)), txt("b")}},
		{"粗体贴标点", []Node{txt("a"), txt("\"引号\"", mk(mBold)), txt("b")}},
		{"中文标点粗体", []Node{txt("注意：", mk(mBold)), txt("内容。说明"), txt("“重点”", mk(mBold)), txt("。")}},
		{"链接里粗体", []Node{txt("a", mk(mLink, "href", "u")), txt("b", mk(mLink, "href", "u"), mk(mBold))}},
		{"粗体里链接", []Node{txt("a", mk(mBold)), txt("b", mk(mLink, "href", "u"), mk(mBold)), txt("c", mk(mBold))}},
		{"两个相邻链接", []Node{txt("a", mk(mLink, "href", "u1")), txt("b", mk(mLink, "href", "u2"))}},
		{"颜色加粗代码", []Node{txt("x", mk(mTextColor, "color", "red"), mk(mBold), mk(mCode))}},
		{"代码与粗体交错", []Node{txt("a", mk(mCode), mk(mBold)), txt("b", mk(mCode))}},
		{"全部标记", []Node{txt("all", mk(mBold), mk(mItalic), mk(mStrike), mk(mUnderline), mk(mSuperscript), mk(mTextColor, "color", "red"), mk(mHighlight, "color", "blue"), mk(mLink, "href", "h"))}},
		{"换行", []Node{txt("一"), br, txt("二")}},
		{"结尾换行", []Node{txt("一"), br}},
		{"开头换行", []Node{br, txt("一")}},
		{"粗体里换行", []Node{txt("a", mk(mBold)), {Type: tHardBreak, Marks: []Mark{mk(mBold)}}, txt("b", mk(mBold))}},
		{"文字里的换行符", []Node{txt("一\n二\r\n三")}},
		{"页面提及", []Node{txt("见 "), m("page", "p1"), txt(" 页")}},
		{"页面提及无标题", []Node{m("page", "unknown")}},
		{"页面提及怪标题", []Node{m("page", "p2")}},
		{"用户提及", []Node{m("user", "u1"), m("user", "u9")}},
		{"日期提及", []Node{{Type: tMention, Attrs: map[string]any{"kind": "date", "date": "2026-11-25", "endDate": "2026-11-30"}}}},
		{"粗体提及", []Node{{Type: tMention, Attrs: map[string]any{"kind": "page", "id": "p1"}, Marks: []Mark{mk(mBold)}}}},
		{"链接提及", []Node{{Type: tMention, Attrs: map[string]any{"kind": "user", "id": "u1"}, Marks: []Mark{mk(mLink, "href", "x")}}}},
		{"行内公式", []Node{txt("面积 "), math(`\pi r^2`), txt(" 平方米")}},
		{"公式后接数字", []Node{math("x"), txt("5 个")}},
		{"公式含美元", []Node{math(`a$b \$ c`)}},
		{"公式结尾反斜杠", []Node{math(`a\`)}},
		{"公式首尾空格", []Node{math("  x  ")}},
		{"公式相邻", []Node{math("a"), math("b")}},
		{"粗体公式", []Node{txt("前"), {Type: tMathInline, Attrs: map[string]any{"latex": "x"}, Marks: []Mark{mk(mBold)}}, txt("后")}},
		{"空公式丢弃", []Node{txt("a"), math(" ")}},
		{"金额", []Node{txt("价格 $5 到 $10，US$3")}},
		{"特殊字符", []Node{txt(`* _ ` + "`" + ` [ ] < > \ # + - ! & | ~ $ { } ( ) " ' = :`)}},
		{"行首井号", []Node{txt("# 不是标题")}},
		{"行首引用", []Node{txt("> 不是引用")}},
		{"行首列表", []Node{txt("- 不是列表")}},
		{"行首加号", []Node{txt("+ 不是列表")}},
		{"行首星号", []Node{txt("* 不是列表")}},
		{"行首有序", []Node{txt("1. 不是列表")}},
		{"行首有序括号", []Node{txt("2024) 年")}},
		{"行首竖线", []Node{txt("| a | b |")}},
		{"行首代码围栏", []Node{txt("```go")}},
		{"行首波浪围栏", []Node{txt("~~~")}},
		{"分隔线样子", []Node{txt("***")}},
		{"下划线分隔线", []Node{txt("___")}},
		{"等号", []Node{txt("===")}},
		{"四空格缩进", []Node{txt("    code?")}},
		{"制表符", []Node{txt("a\tb")}},
		{"下划线单词", []Node{txt("snake_case_name 与 _开头 和 结尾_ 及 __双__")}},
		{"中文下划线", []Node{txt("中_文_字")}},
		{"实体样子", []Node{txt("&amp; &#123; &copy; &x &")}},
		{"反斜杠", []Node{txt(`C:\Users\a\*b\ end\`)}},
		{"感叹号链接", []Node{txt("Wow!"), txt("here", mk(mLink, "href", "u"))}},
		{"尖括号", []Node{txt("a < b > c <div> </p> <!-- x --> <http://x.com> <a@b.c>")}},
		{"标签样子", []Node{txt("<callout>不是标签</callout> <empty-block/> <br>")}},
		{"尾注样子", []Node{txt(`结尾像尾注 {color="red"}`)}},
		{"尾注样子2", []Node{txt(`{width=3}`)}},
		{"尾注样子加真颜色", []Node{txt(`x {align="center"}`)}},
		{"私用区字符", []Node{txt("a\uE000 1\uE001 b\uE002 c\uE003")}},
		{"零字符", []Node{txt("a\x00b")}},
		{"不间断空格", []Node{txt("\u00a0前后\u00a0")}},
		{"全角空格", []Node{txt("　前后　")}},
		{"首尾空格", []Node{txt("  前后  ")}},
		{"emoji粗体", []Node{txt("前"), txt("💡提示", mk(mBold)), txt("后")}},
		{"波浪号", []Node{txt("1~3 天 ~~ ~")}},
		{"表情与符号", []Node{txt("😀 → ✓ © ® ™ ½")}},
		{"阿拉伯与希伯来", []Node{txt("مرحبا שלום")}},
		{"组合字符", []Node{txt("e\u0301 n\u0303")}},
	}
}

func TestInlineRoundTrip(t *testing.T) {
	for _, c := range inlineCases() {
		checkStable(t, c.name, para(c.c...), testOpts)
		// 同样的内容放进标题、列表项、表格、折叠标题、引用
		checkStable(t, c.name+"/标题", nd(tHeading, map[string]any{"level": 2}, c.c...), testOpts)
		checkStable(t, c.name+"/列表", nd(tBulletList, nil, li(para(c.c...)), li(para(txt("x")))), testOpts)
		checkStable(t, c.name+"/有序", nd(tOrderedList, map[string]any{"start": 10}, li(para(c.c...))), testOpts)
		checkStable(t, c.name+"/任务", nd(tTaskList, nil, nd(tTaskItem, nil, para(c.c...))), testOpts)
		checkStable(t, c.name+"/表格", nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(c.c...)), nd(tTableCell, nil, para(txt("y"))))), testOpts)
		checkStable(t, c.name+"/折叠", nd(tToggle, nil, nd(tToggleTitle, nil, c.c...), nd(tToggleBody, nil)), testOpts)
		checkStable(t, c.name+"/引用", nd(tBlockquote, nil, para(c.c...)), testOpts)
		checkStable(t, c.name+"/标注", nd(tCallout, nil, para(c.c...)), testOpts)
		checkStable(t, c.name+"/颜色", nd(tParagraph, map[string]any{"color": "red"}, c.c...), testOpts)
	}
}

// TestInlineMarkCombinations 穷举标记组合：每种组合放在中间一段，并与前后段的不同组合交错。
func TestInlineMarkCombinations(t *testing.T) {
	all := []Mark{mk(mBold), mk(mItalic), mk(mStrike), mk(mUnderline), mk(mCode), mk(mLink, "href", "https://x.y"),
		mk(mTextColor, "color", "red"), mk(mHighlight, "color", "yellow"), mk(mSubscript), mk(mSuperscript)}
	n := len(all)
	for mask := 0; mask < 1<<n; mask++ {
		var ms []Mark
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				ms = append(ms, all[i])
			}
		}
		other := []Mark{all[mask%n]}
		c := []Node{txt("前"), txt("中间", ms...), txt("后", other...), txt("尾")}
		checkStable(t, fmt.Sprintf("组合%d", mask), para(c...), RenderOptions{})
	}
}

// TestInlineFuzz 随机生成行内内容（随机文字、随机标记、随机行内节点）检查往返稳定。
func TestInlineFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(20261125))
	pieces := []string{"a", "中文", " ", "  ", "*", "**", "_", "`", "``", "[", "]", "(", ")", "<", ">", "\\", "#", "-", "+", "!",
		"&", "&amp;", "|", "~", "$", "{", "}", "{color=\"red\"}", "1.", "：", "“", "”", "。", "x_y", "<u>", "</span>", "\"", "'",
		"http://a.b", "💡", "\t", "=", ":", "<br>", "&#36;", "\uE000"}
	marks := []Mark{mk(mBold), mk(mItalic), mk(mStrike), mk(mUnderline), mk(mCode), mk(mLink, "href", "u"), mk(mLink, "href", "v w"),
		mk(mTextColor, "color", "red"), mk(mHighlight), mk(mSubscript), mk(mSuperscript)}
	for iter := 0; iter < 4000; iter++ {
		var c []Node
		for k := rng.Intn(6) + 1; k > 0; k-- {
			switch rng.Intn(10) {
			case 0:
				c = append(c, Node{Type: tHardBreak})
			case 1:
				c = append(c, Node{Type: tMathInline, Attrs: map[string]any{"latex": pieces[rng.Intn(len(pieces))] + "x"}})
			case 2:
				c = append(c, Node{Type: tMention, Attrs: map[string]any{"kind": "page", "id": "p" + fmt.Sprint(rng.Intn(3))}})
			default:
				var sb strings.Builder
				for w := rng.Intn(4) + 1; w > 0; w-- {
					sb.WriteString(pieces[rng.Intn(len(pieces))])
				}
				var ms []Mark
				for _, m := range marks {
					if rng.Intn(5) == 0 {
						ms = append(ms, m)
					}
				}
				c = append(c, txt(sb.String(), ms...))
			}
		}
		name := fmt.Sprintf("随机%d", iter)
		checkStable(t, name, para(c...), testOpts)
		if iter%4 == 0 {
			checkStable(t, name+"/表格", nd(tTable, nil, nd(tTableRow, nil, nd(tTableCell, nil, para(c...)))), testOpts)
			checkStable(t, name+"/列表", nd(tBulletList, nil, li(para(c...))), testOpts)
		}
		if t.Failed() {
			return
		}
	}
}

// TestBlockFuzz 随机生成嵌套块结构检查整页往返。
func TestBlockFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	texts := []string{"文字", "- 列表样", "# 井号", "<callout>", "a*b", "1. x", "", "```", "$$", "| t |", "{color=\"red\"}", "  "}
	var gen func(depth int) Node
	leaf := func() Node {
		s := texts[rng.Intn(len(texts))]
		ms := []Mark{mk(mBold), mk(mItalic), mk(mCode), mk(mLink, "href", "u"), mk(mUnderline)}
		switch rng.Intn(20) {
		case 0:
			return nd(tHeading, map[string]any{"level": rng.Intn(3) + 1}, txt(s))
		case 1:
			return nd(tCodeBlock, nil, txt(s))
		case 2:
			return nd(tImage, map[string]any{"src": "x.png", "alt": s})
		case 3:
			return nd(tMathBlock, map[string]any{"latex": s})
		case 4:
			return nd(tHorizontalRule, nil)
		case 5:
			return nd(tTable, nil, nd(tTableRow, nil, nd(tTableHeader, nil, para(txt(s))), nd(tTableHeader, nil, para(txt("h")))),
				nd(tTableRow, nil, nd(tTableCell, nil, para(txt(s, ms[rng.Intn(len(ms))]))), nd(tTableCell, nil, para())))
		case 6:
			return nd(tMermaid, map[string]any{"code": s})
		case 7:
			return nd(tMindmap, map[string]any{"data": map[string]any{"root": map[string]any{"text": s, "children": []any{map[string]any{"text": "子"}}}}})
		case 8:
			return nd(tChildPage, map[string]any{"pageId": []string{"p1", "p2", "zz"}[rng.Intn(3)]})
		case 9:
			return nd(tSyncedBlock, map[string]any{"sourceId": "src"})
		case 10:
			return nd(tFileBlock, map[string]any{"src": "lemon://file/1", "name": s})
		case 11:
			return nd(tWhiteboard, map[string]any{"id": "w", "title": s})
		case 12:
			return nd(tParagraph, map[string]any{"color": "red"}, txt(s))
		case 13:
			return para(txt(s), txt(s+"粗", ms[rng.Intn(len(ms))]), txt("尾"))
		case 14:
			return nd(tTOC, nil)
		default:
			return para(txt(s))
		}
	}
	gen = func(depth int) Node {
		if depth > 3 {
			return leaf()
		}
		kids := func(n int) []Node {
			var out []Node
			for i := 0; i < n; i++ {
				out = append(out, gen(depth+1))
			}
			return out
		}
		switch rng.Intn(10) {
		case 0:
			var items []Node
			for i := rng.Intn(3) + 1; i > 0; i-- {
				items = append(items, li(append([]Node{para(txt(texts[rng.Intn(len(texts))]))}, kids(rng.Intn(2))...)...))
			}
			return nd([]string{tBulletList, tOrderedList, tTaskList}[rng.Intn(3)], nil, items...)
		case 1:
			return nd(tCallout, map[string]any{"icon": "x"}, kids(rng.Intn(3)+1)...)
		case 2:
			return nd(tBlockquote, nil, kids(rng.Intn(3)+1)...)
		case 3:
			return nd(tToggle, nil, nd(tToggleTitle, nil, txt(texts[rng.Intn(len(texts))])), nd(tToggleBody, nil, kids(rng.Intn(3))...))
		case 4:
			var cols []Node
			for i := rng.Intn(3) + 1; i > 0; i-- {
				cols = append(cols, nd(tColumn, nil, kids(rng.Intn(2)+1)...))
			}
			return nd(tColumns, nil, cols...)
		case 5:
			return nd(tSyncedBlock, map[string]any{"id": fmt.Sprint("s", rng.Intn(100))}, kids(rng.Intn(3))...)
		default:
			return leaf()
		}
	}
	for iter := 0; iter < 400; iter++ {
		var blocks []Node
		for i := rng.Intn(6) + 1; i > 0; i-- {
			blocks = append(blocks, gen(0))
		}
		for i, b := range blocks {
			checkStable(t, fmt.Sprintf("块随机%d/%d", iter, i), b, testOpts)
		}
		checkPage(t, fmt.Sprintf("页随机%d", iter), blocks, testOpts)
		if t.Failed() {
			return
		}
	}
}

func TestEmptyPage(t *testing.T) {
	if s := Render(nil, RenderOptions{}); s != "" {
		t.Fatalf("空页面应渲染为空串，得到 %q", s)
	}
	for _, md := range []string{"", "\n", "   \n\n  \t\n"} {
		bs := mustParse(t, md)
		if len(bs) != 0 {
			t.Fatalf("%q 应解析为空，得到 %s", md, js(bs))
		}
	}
	out, err := Reconcile(nil, "", RenderOptions{})
	if err != nil || len(out) != 0 {
		t.Fatalf("空页面 Reconcile 应为空：%v %s", err, js(out))
	}
}

// TestArbitraryMarkdownFixpoint 随机拼接大模型可能写出的各种片段（含不规范、不配对的标签），
// 断言：解析不会崩溃；解析一次后的结果渲染再解析，块数与逐块文本都不再变化。
func TestArbitraryMarkdownFixpoint(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	frags := []string{"文字", "**粗**", "*斜*", "`码`", "[链](http://a.b)", "![图](x.png)", "<u>下</u>", "<br>", "$x$", "$5", "\n", "\n\n",
		"- ", "1. ", "- [ ] ", "> ", "# ", "## ", "  ", "    ", "```", "```go\n", "~~~", "$$", "---", "| a | b |\n|---|---|\n| 1 | 2 |",
		"<callout icon=\"x\">", "</callout>", "<details>", "<summary>", "</summary>", "</details>", "<columns>", "<column>", "</column>", "</columns>",
		"<page id=\"p1\">", "</page>", "<toc/>", "<empty-block/>", "<synced-block id=\"s\">", "</synced-block>", "<whiteboard id=\"w\"/>",
		"<mention-page id=\"p1\"/>", "<span color=\"red\">", "</span>", "{color=\"red\"}", "{width=3}", "<div>", "\\", "*", "_", "|", "：", "“引”",
		"\t", "<video src=\"v\">", "</video>", "```mindmap\n- 根\n  - 叶\n```", "```chart\n{\"a\":1}\n```", "<!-- c -->", "&amp;", "&#36;",
		"\n- a\n  - b\n", "1) ", "<summary>T</summary>", "[x](u)", "~~删~~", "<mention-user id=\"u1\">名</mention-user>", "<file src=\"f\" name=\"n\"/>",
		"   ", "\n    code\n", "* ", "+ ", "***", "___", "<sub>", "</sub>", "$$x$$", "\\$", "`", "``", "<br/>", "===", "\n===\n", "<", ">", "&",
		"<a href=\"u\">", "</a>", "\n> > ", "<details heading=\"2\" open>", "<link-to-page id=\"x\"/>", "<bookmark url=\"u\"/>", "\n1. a\n\n2. b\n",
		"- [x] ", "<synced-block source=\"s\"/>", "<embed url=\"e\" height=\"3\"/>", "\u3000", "💡", "<mark>", "</mark>", "**", "~~"}
	for iter := 0; iter < 3000; iter++ {
		var sb strings.Builder
		for k := rng.Intn(25) + 1; k > 0; k-- {
			sb.WriteString(frags[rng.Intn(len(frags))])
		}
		md := sb.String()
		bs1 := mustParse(t, md)
		noPseudo(t, md, bs1)
		md1 := Render(bs1, testOpts)
		bs2 := mustParse(t, md1)
		if len(bs1) != len(bs2) {
			t.Fatalf("%d: 块数 %d → %d\n--- 输入:\n%s\n--- 第一次渲染:\n%s\n--- 第二次解析:\n%s", iter, len(bs1), len(bs2), md, md1, js(bs2))
		}
		for i := range bs1 {
			a, b := RenderBlock(bs1[i], testOpts), RenderBlock(bs2[i], testOpts)
			if a != b {
				t.Fatalf("%d: 第 %d 块不稳定\n--- 输入:\n%s\n--- 第一次:\n%s\n--- 第二次:\n%s", iter, i, md, a, b)
			}
		}
	}
}

// TestConcurrentUse 解析器全局共享，并发调用必须安全（配合 -race 运行）。
func TestConcurrentUse(t *testing.T) {
	page := bigPage(200)
	want := Render(page, testOpts)
	done := make(chan string, 8)
	for g := 0; g < 8; g++ {
		go func() {
			bs, _ := Parse(want)
			done <- Render(bs, testOpts)
		}()
	}
	for g := 0; g < 8; g++ {
		if got := <-done; got != want {
			t.Fatal("并发解析结果不一致")
		}
	}
}
