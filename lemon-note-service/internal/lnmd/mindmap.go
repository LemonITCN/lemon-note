package lnmd

import (
	"strconv"
	"strings"
)

// 思维导图块数据：{root:{id,text,children:[...],style?,note?,collapsed?}, layout, theme}。
// Markdown 里只写结构：缩进两格的列表，第一项为中心主题。样式、备注、布局等非结构字段不出现，
// Reconcile 时按「从根到节点的文本路径」从原数据继承回来。

func mindmapText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s := scalarString(m["text"])
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
	return strings.TrimSpace(s)
}

func mindmapChildren(v any) []any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	switch c := m["children"].(type) {
	case []any:
		return c
	case []map[string]any:
		out := make([]any, len(c))
		for i := range c {
			out[i] = c[i]
		}
		return out
	}
	return nil
}

func mindmapRoot(data any) any {
	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	return m["root"]
}

// renderMindmap 把思维导图数据渲染成缩进列表。
func renderMindmap(data any) string {
	root := mindmapRoot(data)
	if root == nil || (mindmapText(root) == "" && len(mindmapChildren(root)) == 0) {
		return ""
	}
	var lines []string
	var walk func(n any, depth int)
	walk = func(n any, depth int) {
		t := mindmapText(n)
		line := strings.Repeat("  ", depth) + "-"
		if t != "" {
			line += " " + t
		}
		lines = append(lines, line)
		for _, c := range mindmapChildren(n) {
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	return strings.Join(lines, "\n")
}

// parseMindmap 解析缩进列表。第一项为中心主题；与它同级的后续项也挂到中心主题下。
func parseMindmap(content string) map[string]any {
	type entry struct {
		indent int
		node   map[string]any
	}
	newNode := func(t string) map[string]any {
		return map[string]any{"text": t, "children": []any{}}
	}
	var root map[string]any
	rootIndent := 0
	var stack []entry
	for _, raw := range strings.Split(content, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		indent := 0
		for _, r := range raw {
			if r == ' ' {
				indent++
			} else if r == '\t' {
				indent += 4
			} else {
				break
			}
		}
		t := strings.TrimSpace(raw)
		switch {
		case t == "-" || t == "*" || t == "+":
			t = ""
		case len(t) >= 2 && (t[0] == '-' || t[0] == '*' || t[0] == '+') && (t[1] == ' ' || t[1] == '\t'):
			t = strings.TrimSpace(t[2:])
		default:
			if ml := listMarkerAt(t, 0); ml > 0 && ml < len(t) && (t[ml] == ' ' || t[ml] == '\t') && isASCIIDigit(t[0]) {
				t = strings.TrimSpace(t[ml+1:])
			}
		}
		n := newNode(t)
		if root == nil {
			root = n
			rootIndent = indent
			stack = []entry{{indent: indent, node: n}}
			continue
		}
		if indent <= rootIndent {
			// 与中心主题同级：挂到中心主题下
			indent = rootIndent + 1
		}
		for len(stack) > 1 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1].node
		parent["children"] = append(parent["children"].([]any), n)
		stack = append(stack, entry{indent: indent, node: n})
	}
	if root == nil {
		root = newNode("")
	}
	return map[string]any{"root": root}
}

// inheritMindmap 把原思维导图的非结构字段继承到新数据上：顶层除 root 外的字段（布局、主题等）原样带回；
// 节点按「从根到节点的文本路径」（同名兄弟按出现次序区分）匹配，带回 id、样式、备注等全部非结构字段。
func inheritMindmap(oldData, newData any) any {
	om, ok1 := oldData.(map[string]any)
	nm, ok2 := newData.(map[string]any)
	if !ok1 || !ok2 {
		return newData
	}
	out := make(map[string]any, len(nm))
	for k, v := range nm {
		out[k] = v
	}
	for k, v := range om {
		if k == "root" {
			continue
		}
		if _, exists := out[k]; !exists {
			out[k] = deepCopyValue(v)
		}
	}
	index := map[string]map[string]any{}
	var collect func(n any, path string)
	collect = func(n any, path string) {
		m, ok := n.(map[string]any)
		if !ok {
			return
		}
		index[path] = m
		seen := map[string]int{}
		for _, c := range mindmapChildren(m) {
			t := mindmapText(c)
			k := seen[t]
			seen[t] = k + 1
			collect(c, path+"\x1f"+t+"\x1e"+strconv.Itoa(k))
		}
	}
	collect(om["root"], "\x1f"+mindmapText(om["root"]))
	var apply func(n any, path string) any
	apply = func(n any, path string) any {
		m, ok := n.(map[string]any)
		if !ok {
			return n
		}
		res := make(map[string]any, len(m))
		for k, v := range m {
			res[k] = v
		}
		if old, ok := index[path]; ok {
			for k, v := range old {
				if k == "text" || k == "children" {
					continue
				}
				if _, exists := res[k]; !exists {
					res[k] = deepCopyValue(v)
				}
			}
		}
		seen := map[string]int{}
		kids := mindmapChildren(m)
		newKids := make([]any, len(kids))
		for i, c := range kids {
			t := mindmapText(c)
			k := seen[t]
			seen[t] = k + 1
			newKids[i] = apply(c, path+"\x1f"+t+"\x1e"+strconv.Itoa(k))
		}
		res["children"] = newKids
		return res
	}
	if nr, ok := nm["root"]; ok {
		out["root"] = apply(nr, "\x1f"+mindmapText(nr))
	}
	return out
}
