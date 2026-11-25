// Package whiteboard 让智能体读写白板块（Excalidraw 场景）：列出元素摘要、用简化语法增删改元素、
// 按 Mermaid 流程图自动布局生成图形。生成的元素是完整的 Excalidraw 0.18 元素，前端加载时还会再经过 restore 补全。
package whiteboard

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Scene 是白板块 attrs.data。
type Scene struct {
	Elements []map[string]any `json:"elements"`
	AppState map[string]any   `json:"appState,omitempty"`
	Files    map[string]any   `json:"files,omitempty"`
}

const (
	fontExcalifont = 5
	fontSize       = 20.0
	lineHeight     = 1.25
	defaultStroke  = "#1e1e1e"
)

func rnd() int64 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint32(b[:]) & 0x7fffffff)
}

const idChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"

func newID() string {
	b := make([]byte, 21)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = idChars[int(b[i])%len(idChars)]
	}
	return string(b)
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func deleted(e map[string]any) bool { b, _ := e["isDeleted"].(bool); return b }

func base(typ string, x, y, w, h float64) map[string]any {
	var round any
	switch typ {
	case "rectangle", "diamond", "ellipse":
		round = map[string]any{"type": 3}
	case "arrow", "line":
		round = map[string]any{"type": 2}
	}
	return map[string]any{
		"id": newID(), "type": typ, "x": x, "y": y, "width": w, "height": h, "angle": 0,
		"strokeColor": defaultStroke, "backgroundColor": "transparent", "fillStyle": "solid", "strokeWidth": 2, "strokeStyle": "solid",
		"roughness": 1, "opacity": 100, "groupIds": []any{}, "frameId": nil, "roundness": round, "seed": rnd(), "version": 1,
		"versionNonce": rnd(), "isDeleted": false, "boundElements": []any{}, "updated": time.Now().UnixMilli(), "link": nil, "locked": false,
	}
}

// textSize 粗略估算文字尺寸：中日韩字符按一个字号宽，其余按 0.6 个字号。
func textSize(s string, size float64) (float64, float64) {
	lines := strings.Split(s, "\n")
	maxW := 0.0
	for _, l := range lines {
		w := 0.0
		for _, r := range l {
			if r > 0x2e80 || unicode.Is(unicode.Han, r) {
				w += size
			} else {
				w += size * 0.6
			}
		}
		maxW = math.Max(maxW, w)
	}
	return math.Ceil(maxW), math.Ceil(float64(len(lines)) * size * lineHeight)
}

func textElement(text string, x, y float64, containerID string) map[string]any {
	w, h := textSize(text, fontSize)
	e := base("text", x, y, w, h)
	e["roundness"] = nil
	e["strokeWidth"] = 1
	e["text"], e["originalText"] = text, text
	e["fontSize"], e["fontFamily"], e["lineHeight"] = fontSize, fontExcalifont, lineHeight
	e["textAlign"], e["verticalAlign"] = "left", "top"
	e["containerId"] = nil
	e["autoResize"] = true
	if containerID != "" {
		e["containerId"] = containerID
		e["textAlign"], e["verticalAlign"] = "center", "middle"
	}
	return e
}

// sceneIndex 便于按 id 找元素、找容器里的文字。
type sceneIndex struct {
	s      *Scene
	byID   map[string]map[string]any
	textOf map[string]map[string]any // 容器 id → 绑定的文字元素
}

func index(s *Scene) *sceneIndex {
	ix := &sceneIndex{s: s, byID: map[string]map[string]any{}, textOf: map[string]map[string]any{}}
	for _, e := range s.Elements {
		if deleted(e) {
			continue
		}
		ix.byID[str(e, "id")] = e
		if str(e, "type") == "text" && str(e, "containerId") != "" {
			ix.textOf[str(e, "containerId")] = e
		}
	}
	return ix
}

func (ix *sceneIndex) label(id string) string {
	if t := ix.textOf[id]; t != nil {
		return str(t, "text")
	}
	if e := ix.byID[id]; e != nil && str(e, "type") == "text" {
		return str(e, "text")
	}
	return ""
}

func binding(e map[string]any, k string) string {
	if b, ok := e[k].(map[string]any); ok {
		return str(b, "elementId")
	}
	return ""
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if utf8.RuneCountInString(s) > 80 {
		r := []rune(s)
		return string(r[:80]) + "…"
	}
	return s
}

// Summary 生成给智能体看的元素清单。
func Summary(s *Scene) string {
	ix := index(s)
	var shapes, texts, links []string
	ids := make([]string, 0, len(ix.byID))
	for id := range ix.byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ix.byID[ids[i]], ix.byID[ids[j]]
		if num(a, "y") != num(b, "y") {
			return num(a, "y") < num(b, "y")
		}
		return num(a, "x") < num(b, "x")
	})
	for _, id := range ids {
		e := ix.byID[id]
		typ := str(e, "type")
		geo := fmt.Sprintf("(%d,%d %d×%d)", int(num(e, "x")), int(num(e, "y")), int(num(e, "width")), int(num(e, "height")))
		switch typ {
		case "text":
			if str(e, "containerId") != "" {
				continue
			}
			texts = append(texts, fmt.Sprintf("- %s text %s %q", id, geo, oneLine(str(e, "text"))))
		case "arrow", "line":
			from, to := binding(e, "startBinding"), binding(e, "endBinding")
			desc := fmt.Sprintf("- %s %s", id, typ)
			if from != "" || to != "" {
				desc += fmt.Sprintf(" %s(%q) → %s(%q)", orDash(from), oneLine(ix.label(from)), orDash(to), oneLine(ix.label(to)))
			} else {
				desc += " " + geo
			}
			if l := ix.label(id); l != "" {
				desc += fmt.Sprintf(" 标签 %q", oneLine(l))
			}
			links = append(links, desc)
		default:
			d := fmt.Sprintf("- %s %s %s", id, typ, geo)
			if l := ix.label(id); l != "" {
				d += fmt.Sprintf(" %q", oneLine(l))
			}
			if bg := str(e, "backgroundColor"); bg != "" && bg != "transparent" {
				d += " 填充 " + bg
			}
			shapes = append(shapes, d)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个图形、%d 段文字、%d 条连线。\n", len(shapes), len(texts), len(links))
	if len(shapes) > 0 {
		b.WriteString("\n图形（id 类型 (x,y 宽×高) 文字）：\n" + strings.Join(shapes, "\n") + "\n")
	}
	if len(texts) > 0 {
		b.WriteString("\n独立文字：\n" + strings.Join(texts, "\n") + "\n")
	}
	if len(links) > 0 {
		b.WriteString("\n连线：\n" + strings.Join(links, "\n") + "\n")
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// bounds 返回现有元素的外框；没有元素时 ok=false。
func bounds(s *Scene) (minX, minY, maxX, maxY float64, ok bool) {
	minX, minY = math.MaxFloat64, math.MaxFloat64
	maxX, maxY = -math.MaxFloat64, -math.MaxFloat64
	for _, e := range s.Elements {
		if deleted(e) {
			continue
		}
		x, y, w, h := num(e, "x"), num(e, "y"), num(e, "width"), num(e, "height")
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x+math.Abs(w)), math.Max(maxY, y+math.Abs(h))
		ok = true
	}
	return
}

func addBound(e map[string]any, typ, id string) {
	list, _ := e["boundElements"].([]any)
	for _, x := range list {
		if m, ok := x.(map[string]any); ok && str(m, "id") == id {
			return
		}
	}
	e["boundElements"] = append(list, map[string]any{"type": typ, "id": id})
}

func touch(e map[string]any) {
	e["version"] = num(e, "version") + 1
	e["versionNonce"] = rnd()
	e["updated"] = time.Now().UnixMilli()
}
