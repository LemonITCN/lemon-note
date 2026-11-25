package lnmd
import ("fmt"; "math/rand"; "strings"; "testing")
var seedBig int64 = 5

func TestInlineFuzzBig(t *testing.T) {
	rng := rand.New(rand.NewSource(seedBig))
	pieces := []string{"a", "中文", " ", "  ", "*", "**", "_", "`", "``", "[", "]", "(", ")", "<", ">", "\\", "#", "-", "+", "!",
		"&", "&amp;", "|", "~", "$", "{", "}", "{color=\"red\"}", "1.", "：", "“", "”", "。", "x_y", "<u>", "</span>", "\"", "'",
		"http://a.b", "💡", "\t", "=", ":", "<br>", "&#36;", "\uE000"}
	marks := []Mark{mk(mBold), mk(mItalic), mk(mStrike), mk(mUnderline), mk(mCode), mk(mLink, "href", "u"), mk(mLink, "href", "v w"),
		mk(mTextColor, "color", "red"), mk(mHighlight), mk(mSubscript), mk(mSuperscript)}
	for iter := 0; iter < 60000; iter++ {
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
func TestBlockFuzzBig(t *testing.T) {
	rng := rand.New(rand.NewSource(seedBig))
	texts := []string{"文字", "- 列表样", "# 井号", "<callout>", "a*b", "1. x", "", "```", "$$", "| t |", "{color=\"red\"}", "  "}
	var gen func(depth int) Node
	leaf := func() Node {
		s := texts[rng.Intn(len(texts))]
		switch rng.Intn(8) {
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
	for iter := 0; iter < 6000; iter++ {
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
