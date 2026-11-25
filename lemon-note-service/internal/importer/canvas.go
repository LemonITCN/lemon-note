package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
)

// Obsidian Canvas（JSON Canvas）转白板：卡片变成 Excalidraw 的矩形 + 绑定文字，分组变成虚线框，
// 连线变成绑定两端的箭头。白板块数据格式见前端 lib/excalidraw.ts：{elements, appState, files}。

type canvasDoc struct {
	Nodes []canvasNode `json:"nodes"`
	Edges []canvasEdge `json:"edges"`
}

type canvasNode struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Text   string  `json:"text"`
	File   string  `json:"file"`
	URL    string  `json:"url"`
	Label  string  `json:"label"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Color  string  `json:"color"`
}

type canvasEdge struct {
	ID       string `json:"id"`
	FromNode string `json:"fromNode"`
	FromSide string `json:"fromSide"`
	FromEnd  string `json:"fromEnd"`
	ToNode   string `json:"toNode"`
	ToSide   string `json:"toSide"`
	ToEnd    string `json:"toEnd"`
	Label    string `json:"label"`
	Color    string `json:"color"`
}

// Obsidian 预设色 1-6 → Excalidraw 调色板（线条色、浅底色）
var canvasColors = map[string][2]string{
	"1": {"#e03131", "#ffc9c9"}, "2": {"#f08c00", "#ffd8a8"}, "3": {"#e8b100", "#fff3bf"},
	"4": {"#2f9e44", "#b2f2bb"}, "5": {"#0c8599", "#99e9f2"}, "6": {"#6741d9", "#d0bfff"},
}

const (
	wbFont       = 20.0
	wbLineHeight = 1.25
	wbPad        = 10.0
)

func (pl *vaultPlan) prepareCanvas(p string) func(ctx context.Context, b *builder, n *pnode) error {
	return func(ctx context.Context, b *builder, n *pnode) error {
		f := pl.v.get(p)
		if f == nil {
			return nil
		}
		raw, err := pl.v.readText(f)
		if err != nil {
			if err == errTextTooBig {
				b.warn("text_too_large", p, "", "文件太大，白板未导入")
				return nil
			}
			return err
		}
		var doc canvasDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			b.warn("canvas_failed", p, "", "Canvas 文件格式无法识别，白板未导入")
			return nil
		}
		els := canvasElements(doc, func(file string) (string, *pnode) {
			if note := pl.resolveNote(file, p); note != nil {
				return note.title, note
			}
			return stripExt(path.Base(file)), nil
		})
		n.blocks = []lnmd.Node{{Type: "whiteboard", Attrs: map[string]any{
			"title": n.title,
			"data":  map[string]any{"elements": els, "appState": map[string]any{"viewBackgroundColor": "#ffffff", "gridSize": nil}, "files": map[string]any{}},
		}}}
		return nil
	}
}

type wbElem = map[string]any

func baseElem(id, typ string, x, y, w, h float64, stroke, bg string) wbElem {
	return wbElem{
		"id": id, "type": typ, "x": x, "y": y, "width": w, "height": h, "angle": 0,
		"strokeColor": stroke, "backgroundColor": bg, "fillStyle": "solid", "strokeWidth": 2, "strokeStyle": "solid",
		"roughness": 1, "opacity": 100, "groupIds": []any{}, "frameId": nil, "roundness": map[string]any{"type": 3},
		"seed": rand.IntN(1 << 30), "version": 1, "versionNonce": rand.IntN(1 << 30), "isDeleted": false,
		"boundElements": []any{}, "updated": model.Now(), "link": nil, "locked": false,
	}
}

// runeWidth 粗略估计字符宽度（全角按一个字号，半角按 0.55 个字号）。
func runeWidth(r rune) float64 {
	if r > 0x2E80 && (unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) || r >= 0xFF00 && r <= 0xFFEF) {
		return wbFont
	}
	return wbFont * 0.55
}

// wrapText 按宽度折行，返回折好的文字与尺寸。
func wrapText(s string, maxW float64) (string, float64, float64) {
	var lines []string
	widest := 0.0
	for _, para := range strings.Split(s, "\n") {
		cur, w := []rune{}, 0.0
		for _, r := range para {
			rw := runeWidth(r)
			if w+rw > maxW && len(cur) > 0 {
				lines = append(lines, string(cur))
				widest = math.Max(widest, w)
				cur, w = nil, 0
				if r == ' ' {
					continue
				}
			}
			cur = append(cur, r)
			w += rw
		}
		lines = append(lines, string(cur))
		widest = math.Max(widest, w)
	}
	return strings.Join(lines, "\n"), math.Ceil(widest), float64(len(lines)) * wbFont * wbLineHeight
}

// plainCanvasText 去掉卡片文字里最常见的 Markdown 标记。
func plainCanvasText(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		t := strings.TrimLeft(l, " ")
		for strings.HasPrefix(t, "#") {
			t = strings.TrimPrefix(t, "#")
		}
		lines[i] = strings.NewReplacer("**", "", "__", "", "==", "").Replace(strings.TrimLeft(t, " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func boundText(id, container, text string, x, y, w, h float64, stroke string) wbElem {
	maxW := math.Max(w-2*wbPad, wbFont)
	wrapped, tw, th := wrapText(text, maxW)
	t := baseElem(id, "text", x+(w-tw)/2, y+(h-th)/2, tw, th, stroke, "transparent")
	t["text"], t["originalText"] = wrapped, text
	t["fontSize"], t["fontFamily"], t["lineHeight"] = wbFont, 5, wbLineHeight
	t["textAlign"], t["verticalAlign"] = "center", "middle"
	t["containerId"], t["autoResize"] = container, true
	t["roundness"] = nil
	return t
}

func canvasElements(doc canvasDoc, noteOf func(file string) (string, *pnode)) []any {
	var groups, cards, arrows []any
	rects := map[string]wbElem{}
	byID := map[string]canvasNode{}
	for i, cn := range doc.Nodes {
		if cn.ID == "" {
			cn.ID = fmt.Sprintf("node%d", i)
		}
		if cn.Width <= 0 {
			cn.Width = 250
		}
		if cn.Height <= 0 {
			cn.Height = 60
		}
		byID[cn.ID] = cn
		stroke, bg := "#1e1e1e", "transparent"
		if c, ok := canvasColors[cn.Color]; ok {
			stroke, bg = c[0], c[1]
		} else if strings.HasPrefix(cn.Color, "#") && (len(cn.Color) == 7 || len(cn.Color) == 4) {
			stroke = cn.Color
		}
		rid := "r-" + cn.ID
		if cn.Type == "group" {
			r := baseElem(rid, "rectangle", cn.X, cn.Y, cn.Width, cn.Height, stroke, "transparent")
			r["strokeStyle"] = "dashed"
			groups = append(groups, r)
			rects[cn.ID] = r
			if cn.Label != "" {
				_, tw, th := wrapText(cn.Label, 1e9)
				t := baseElem("t-"+cn.ID, "text", cn.X, cn.Y-th-6, tw, th, stroke, "transparent")
				t["text"], t["originalText"] = cn.Label, cn.Label
				t["fontSize"], t["fontFamily"], t["lineHeight"] = wbFont, 5, wbLineHeight
				t["textAlign"], t["verticalAlign"] = "left", "top"
				t["containerId"], t["autoResize"], t["roundness"] = nil, true, nil
				groups = append(groups, t)
			}
			continue
		}
		r := baseElem(rid, "rectangle", cn.X, cn.Y, cn.Width, cn.Height, stroke, bg)
		text := ""
		switch cn.Type {
		case "text":
			text = plainCanvasText(cn.Text)
		case "file":
			title, note := noteOf(cn.File)
			text = title
			if note != nil {
				r["link"] = "/p/" + note.id
			}
		case "link":
			text = cn.URL
			if strings.HasPrefix(cn.URL, "http://") || strings.HasPrefix(cn.URL, "https://") {
				r["link"] = cn.URL
			}
		default:
			text = cn.Text
		}
		cards = append(cards, r)
		rects[cn.ID] = r
		if text != "" {
			if utf8.RuneCountInString(text) > 5000 {
				text = string([]rune(text)[:5000])
			}
			tid := "t-" + cn.ID
			cards = append(cards, boundText(tid, rid, text, cn.X, cn.Y, cn.Width, cn.Height, "#1e1e1e"))
			r["boundElements"] = append(r["boundElements"].([]any), map[string]any{"id": tid, "type": "text"})
		}
	}
	for i, e := range doc.Edges {
		from, ok1 := byID[e.FromNode]
		to, ok2 := byID[e.ToNode]
		if !ok1 || !ok2 {
			continue
		}
		id := e.ID
		if id == "" {
			id = fmt.Sprintf("edge%d", i)
		}
		aid := "a-" + id
		fs, ts := e.FromSide, e.ToSide
		if fs == "" || ts == "" {
			dfs, dts := autoSides(from, to)
			if fs == "" {
				fs = dfs
			}
			if ts == "" {
				ts = dts
			}
		}
		sx, sy := sidePoint(from, fs)
		ex, ey := sidePoint(to, ts)
		stroke := "#1e1e1e"
		if c, ok := canvasColors[e.Color]; ok {
			stroke = c[0]
		}
		a := baseElem(aid, "arrow", sx, sy, math.Abs(ex-sx), math.Abs(ey-sy), stroke, "transparent")
		a["points"] = []any{[]any{0, 0}, []any{ex - sx, ey - sy}}
		a["lastCommittedPoint"] = nil
		a["roundness"] = map[string]any{"type": 2}
		a["elbowed"] = false
		a["startBinding"] = map[string]any{"elementId": "r-" + from.ID, "focus": 0, "gap": 4}
		a["endBinding"] = map[string]any{"elementId": "r-" + to.ID, "focus": 0, "gap": 4}
		a["startArrowhead"], a["endArrowhead"] = nil, "arrow"
		if e.ToEnd == "none" {
			a["endArrowhead"] = nil
		}
		if e.FromEnd == "arrow" {
			a["startArrowhead"] = "arrow"
		}
		for _, nid := range []string{from.ID, to.ID} {
			if r := rects[nid]; r != nil {
				r["boundElements"] = append(r["boundElements"].([]any), map[string]any{"id": aid, "type": "arrow"})
			}
		}
		arrows = append(arrows, a)
		if e.Label != "" {
			tid := "t-" + id
			mx, my := (sx+ex)/2, (sy+ey)/2
			_, tw, th := wrapText(e.Label, 200)
			arrows = append(arrows, boundText(tid, aid, e.Label, mx-tw/2-wbPad, my-th/2, tw+2*wbPad, th, stroke))
			a["boundElements"] = []any{map[string]any{"id": tid, "type": "text"}}
		}
	}
	out := append(groups, cards...)
	return append(out, arrows...)
}

func sidePoint(n canvasNode, side string) (float64, float64) {
	switch side {
	case "top":
		return n.X + n.Width/2, n.Y
	case "bottom":
		return n.X + n.Width/2, n.Y + n.Height
	case "left":
		return n.X, n.Y + n.Height/2
	default:
		return n.X + n.Width, n.Y + n.Height/2
	}
}

func autoSides(a, b canvasNode) (string, string) {
	dx := (b.X + b.Width/2) - (a.X + a.Width/2)
	dy := (b.Y + b.Height/2) - (a.Y + a.Height/2)
	if math.Abs(dx) >= math.Abs(dy) {
		if dx >= 0 {
			return "right", "left"
		}
		return "left", "right"
	}
	if dy >= 0 {
		return "bottom", "top"
	}
	return "top", "bottom"
}
