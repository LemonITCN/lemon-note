package lnmd
import ("fmt"; "math/rand"; "testing")
var seedBig int64 = 8

func TestBlockFuzzBig(t *testing.T) {
	rng := rand.New(rand.NewSource(seedBig))
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
