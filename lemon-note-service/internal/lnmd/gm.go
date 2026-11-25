package lnmd

import (
	"sync"
	"unicode"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// goldmark 配置：CommonMark + GFM 表格、任务列表、删除线；不启用 HTML 块（未知 HTML 一律按行内原文
// 处理，不会整段吞掉）、不启用链接引用定义（避免 `[x]: y` 被当定义吃掉）、不启用裸网址自动链接。
// 强调与删除线换成对中日韩文字更友好的定界符规则，另加行内公式 $…$。

var (
	gmOnce   sync.Once
	gmParser parser.Parser
)

func mdParser() parser.Parser {
	gmOnce.Do(func() {
		gmParser = parser.NewParser(
			parser.WithBlockParsers(
				util.Prioritized(parser.NewSetextHeadingParser(), 100),
				util.Prioritized(parser.NewThematicBreakParser(), 200),
				util.Prioritized(parser.NewListParser(), 300),
				util.Prioritized(parser.NewListItemParser(), 400),
				util.Prioritized(parser.NewCodeBlockParser(), 500),
				util.Prioritized(parser.NewATXHeadingParser(), 600),
				util.Prioritized(parser.NewFencedCodeBlockParser(), 700),
				util.Prioritized(parser.NewBlockquoteParser(), 800),
				util.Prioritized(parser.NewParagraphParser(), 1000),
			),
			parser.WithInlineParsers(
				util.Prioritized(taskCheckParser{}, 0),
				util.Prioritized(parser.NewCodeSpanParser(), 100),
				util.Prioritized(parser.NewLinkParser(), 200),
				util.Prioritized(parser.NewAutoLinkParser(), 300),
				util.Prioritized(parser.NewRawHTMLParser(), 400),
				util.Prioritized(emphasisParser{}, 500),
				util.Prioritized(strikeParser{}, 500),
				util.Prioritized(mathParser{}, 600),
			),
			parser.WithParagraphTransformers(
				util.Prioritized(extension.NewTableParagraphTransformer(), 200),
			),
			parser.WithASTTransformers(
				util.Prioritized(extension.NewTableASTTransformer(), 0),
			),
		)
	})
	return gmParser
}

// isCJK 报告字符是否属于中日韩文字或全角符号。
func isCJK(r rune) bool {
	if r < 0x2E80 {
		return false
	}
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) ||
		(r >= 0xFE30 && r <= 0xFE4F)
}

// scanDelimiter 与 goldmark 的 ScanDelimiter 相同，但定界符另一侧是中日韩字符时放宽标点限制，
// 使「**注意：**内容」「说明**“重点”**」这类写法能正确识别。
func scanDelimiter(line []byte, before rune, proc parser.DelimiterProcessor) *parser.Delimiter {
	c := line[0]
	if !proc.IsDelimiter(c) {
		return nil
	}
	j := 0
	for j < len(line) && line[j] == c {
		j++
	}
	after := rune(' ')
	if j < len(line) {
		after = util.ToRune(line, j)
	}
	bp, bw := util.IsPunctRune(before), util.IsSpaceRune(before)
	ap, aw := util.IsPunctRune(after), util.IsSpaceRune(after)
	isLeft := !aw && (!ap || bw || bp || isCJK(before))
	isRight := !bw && (!bp || aw || ap || isCJK(after))
	var canOpen, canClose bool
	if c == '_' {
		canOpen = isLeft && (!isRight || bp)
		canClose = isRight && (!isLeft || ap)
	} else {
		canOpen, canClose = isLeft, isRight
	}
	return parser.NewDelimiter(canOpen, canClose, j, c, proc)
}

type emphasisProc struct{}

func (emphasisProc) IsDelimiter(b byte) bool { return b == '*' || b == '_' }
func (emphasisProc) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}
func (emphasisProc) OnMatch(consumes int) ast.Node { return ast.NewEmphasis(consumes) }

type emphasisParser struct{}

func (emphasisParser) Trigger() []byte { return []byte{'*', '_'} }

func (emphasisParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	node := scanDelimiter(line, before, emphasisProc{})
	if node == nil {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

type strikeProc struct{}

func (strikeProc) IsDelimiter(b byte) bool { return b == '~' }
func (strikeProc) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}
func (strikeProc) OnMatch(consumes int) ast.Node { return extast.NewStrikethrough() }

type strikeParser struct{}

func (strikeParser) Trigger() []byte { return []byte{'~'} }

func (strikeParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	node := scanDelimiter(line, before, strikeProc{})
	if node == nil || node.OriginalLength > 2 || before == '~' {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

// taskCheckParser 识别列表项开头的任务框。与 goldmark 自带的不同，要求 "]" 后是空白或行尾，
// 避免把以链接 [x](…) 开头的列表项误认成任务。
type taskCheckParser struct{}

func (taskCheckParser) Trigger() []byte { return []byte{'['} }

func (taskCheckParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	if parent.Parent() == nil || parent.Parent().FirstChild() != parent || parent.HasChildren() {
		return nil
	}
	if _, ok := parent.Parent().(*ast.ListItem); !ok {
		return nil
	}
	line, _ := block.PeekLine()
	if len(line) < 3 || line[0] != '[' || line[2] != ']' {
		return nil
	}
	switch line[1] {
	case ' ', '\t', 'x', 'X':
	default:
		return nil
	}
	n := 3
	if n < len(line) && line[n] != ' ' && line[n] != '\t' && line[n] != '\n' && line[n] != '\r' {
		return nil
	}
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	block.Advance(n)
	return extast.NewTaskCheckBox(line[1] == 'x' || line[1] == 'X')
}

// mathNode 是行内公式的 AST 节点。
type mathNode struct {
	ast.BaseInline
	Latex string
}

var kindMath = ast.NewNodeKind("LnmdMathInline")

func (n *mathNode) Kind() ast.NodeKind { return kindMath }

func (n *mathNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Latex": n.Latex}, nil)
}

// mathParser 解析行内公式：$…$ 开头结尾不能是空白，闭合 $ 后不能紧跟数字；扫描途中遇到前面是空白的
// $ 即放弃（「$5 到 $10，公式 $x^2$」只有最后一段是公式）；也接受 $$…$$。
type mathParser struct{}

func (mathParser) Trigger() []byte { return []byte{'$'} }

func (mathParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 3 || line[0] != '$' {
		return nil
	}
	if line[1] == '$' {
		for j := 2; j+1 < len(line); j++ {
			if line[j] == '\\' {
				j++
				continue
			}
			if line[j] == '\n' {
				break
			}
			if line[j] == '$' && line[j+1] == '$' {
				latex := trimSpaceTab(string(line[2:j]))
				if latex == "" {
					return nil
				}
				block.Advance(j + 2)
				return &mathNode{Latex: latex}
			}
		}
		return nil
	}
	if line[1] == ' ' || line[1] == '\t' || line[1] == '\n' {
		return nil
	}
	for j := 1; j < len(line); j++ {
		c := line[j]
		if c == '\\' {
			j++
			continue
		}
		if c == '\n' {
			break
		}
		if c != '$' {
			continue
		}
		if line[j-1] == ' ' || line[j-1] == '\t' {
			// 前面是空白的 $ 更像下一个公式的开头或金额，当前这个 $ 按普通文字处理
			return nil
		}
		if j+1 < len(line) && isASCIIDigit(line[j+1]) {
			continue
		}
		latex := string(line[1:j])
		block.Advance(j + 1)
		return &mathNode{Latex: latex}
	}
	return nil
}

func trimSpaceTab(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}
