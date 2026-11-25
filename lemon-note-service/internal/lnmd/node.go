// Package lnmd 实现「增强 Markdown（Lemon Markdown）」与 ProseMirror JSON 块之间的双向转换。
//
// 页面正文是一串顶层块（每块一段 ProseMirror 节点 JSON）。智能体、MCP、导入导出都通过增强
// Markdown 读写页面，因此本包保证：
//
//   - RenderBlock 输出稳定：RenderBlock(n) == RenderBlock(Parse(RenderBlock(n))[0])；
//   - Parse(Render(blocks)) 切出的顶层块数量与原来一致，逐块渲染文本相同；
//   - Reconcile 按渲染文本做最长公共子序列对齐，没变的块原样返回原 JSON。
//
// 节点目录与写法以 docs/spec.md §5.2、§6 为准。
package lnmd

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Mark 是 ProseMirror 行内标记。
type Mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Node 是 ProseMirror 节点。
type Node struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []Node         `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []Mark         `json:"marks,omitempty"`
}

// RenderOptions 提供渲染时需要的外部查询。
type RenderOptions struct {
	PageTitle func(id string) (string, bool) // 子页面、页面链接、页面提及显示标题用，可为 nil
	UserName  func(id string) (string, bool) // 用户提及，可为 nil
}

// 块级节点名。
const (
	tParagraph      = "paragraph"
	tHeading        = "heading"
	tBulletList     = "bulletList"
	tOrderedList    = "orderedList"
	tListItem       = "listItem"
	tTaskList       = "taskList"
	tTaskItem       = "taskItem"
	tToggle         = "toggle"
	tToggleTitle    = "toggleTitle"
	tToggleBody     = "toggleBody"
	tBlockquote     = "blockquote"
	tCallout        = "callout"
	tCodeBlock      = "codeBlock"
	tHorizontalRule = "horizontalRule"
	tImage          = "image"
	tFileBlock      = "fileBlock"
	tVideo          = "video"
	tAudio          = "audio"
	tBookmark       = "bookmark"
	tEmbed          = "embed"
	tTable          = "table"
	tTableRow       = "tableRow"
	tTableCell      = "tableCell"
	tTableHeader    = "tableHeader"
	tColumns        = "columns"
	tColumn         = "column"
	tMathBlock      = "mathBlock"
	tMermaid        = "mermaid"
	tGraphviz       = "graphviz"
	tMindmap        = "mindmap"
	tWhiteboard     = "whiteboard"
	tChart          = "chart"
	tChildPage      = "childPage"
	tChildDatabase  = "childDatabase"
	tLinkToPage     = "linkToPage"
	tSyncedBlock    = "syncedBlock"
	tTOC            = "tableOfContents"
	tBreadcrumb     = "breadcrumb"
)

// 行内节点名。
const (
	tText       = "text"
	tHardBreak  = "hardBreak"
	tMention    = "mention"
	tMathInline = "mathInline"
)

// 行内标记名。
const (
	mBold        = "bold"
	mItalic      = "italic"
	mUnderline   = "underline"
	mStrike      = "strike"
	mCode        = "code"
	mLink        = "link"
	mTextColor   = "textColor"
	mHighlight   = "highlight"
	mSubscript   = "subscript"
	mSuperscript = "superscript"
)

// 解析过程中使用的内部伪节点类型，不会出现在 Parse 的结果里。
const (
	pSummary = "\x00summary"
	pHTML    = "\x00html"
	pCheck   = "\x00check"
	pBlkTok  = "\x00blocktag"
	pTrlTok  = "\x00trailer"
	pImage   = "\x00image"
)

func emptyParagraph() Node { return Node{Type: tParagraph} }

// attrRaw 取属性原值。
func attrRaw(n Node, k string) (any, bool) {
	if n.Attrs == nil {
		return nil, false
	}
	v, ok := n.Attrs[k]
	return v, ok && v != nil
}

// attrStr 取字符串属性；数字等标量转成文本，nil 返回空串。
func attrStr(n Node, k string) string {
	v, ok := attrRaw(n, k)
	if !ok {
		return ""
	}
	return scalarString(v)
}

// scalarString 把标量转成文本（数字用最短十进制表示）。
func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number:
		return x.String()
	case float64:
		return formatFloat(x)
	case float32:
		return formatFloat(float64(x))
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	default:
		return fmt.Sprint(x)
	}
}

func formatFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// attrNum 取数字属性，字符串形式的数字也接受。
func attrNum(n Node, k string) (float64, bool) {
	v, ok := attrRaw(n, k)
	if !ok {
		return 0, false
	}
	return toFloat(v)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case uint32:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// attrInt 取整数属性，缺省返回 def。
func attrInt(n Node, k string, def int) int {
	f, ok := attrNum(n, k)
	if !ok {
		return def
	}
	return int(f)
}

// attrBool 取布尔属性。
func attrBool(n Node, k string) bool {
	v, ok := attrRaw(n, k)
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "open"
	}
	f, ok := toFloat(v)
	return ok && f != 0
}

// markAttrStr 取标记属性字符串。
func markAttrStr(m Mark, k string) string {
	if m.Attrs == nil {
		return ""
	}
	return scalarString(m.Attrs[k])
}

// parseNumberValue 把文本转成数字（能转则 float64，否则保留字符串）。
func parseNumberValue(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return f
	}
	return s
}

// deepCopyValue 深拷贝 JSON 风格的值。
func deepCopyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			m[k] = deepCopyValue(vv)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, vv := range x {
			s[i] = deepCopyValue(vv)
		}
		return s
	default:
		return v
	}
}

// setAttr 设置属性（必要时创建 map）。
func setAttr(n *Node, k string, v any) {
	if n.Attrs == nil {
		n.Attrs = map[string]any{}
	}
	n.Attrs[k] = v
}

// nodeID 取块 id。
func nodeID(n Node) string { return attrStr(n, "id") }

// childBlocks 返回子块，空时补一个空段落（用于 block+ 内容）。
func childBlocksPlus(n Node) []Node {
	if len(n.Content) == 0 {
		return []Node{emptyParagraph()}
	}
	return n.Content
}
