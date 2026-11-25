package share

import (
	"encoding/json"
	"regexp"
	"strings"
)

var pageHref = regexp.MustCompile(`^(?:https?://[^/]+)?/p/([A-Za-z0-9_-]+)(?:[?#].*)?$`)

// sanitizeBlock 对一个块的 JSON 做公开脱敏，返回新的 JSON。
func (sc *scope) sanitizeBlock(raw json.RawMessage, token string) json.RawMessage {
	var n map[string]any
	if json.Unmarshal(raw, &n) != nil {
		return json.RawMessage(`{"type":"paragraph"}`)
	}
	sc.sanitizeNode(n, token)
	b, err := json.Marshal(n)
	if err != nil {
		return json.RawMessage(`{"type":"paragraph"}`)
	}
	return b
}

func (sc *scope) sanitizeNode(n map[string]any, token string) {
	typ, _ := n["type"].(string)
	attrs, _ := n["attrs"].(map[string]any)
	switch typ {
	case "mention":
		if attrs != nil && attrs["kind"] == "page" {
			id, _ := attrs["id"].(string)
			if !sc.has(id) {
				attrs["id"] = ""
				attrs["label"] = ""
			}
		}
	case "childPage", "linkToPage", "childDatabase":
		if attrs != nil {
			id, _ := attrs["pageId"].(string)
			if !sc.has(id) {
				attrs["pageId"] = ""
				delete(attrs, "viewId")
			}
		}
	}
	if marks, ok := n["marks"].([]any); ok {
		out := marks[:0]
		for _, m := range marks {
			mm, _ := m.(map[string]any)
			if mm == nil {
				continue
			}
			switch mm["type"] {
			case "comment":
				continue
			case "link":
				ma, _ := mm["attrs"].(map[string]any)
				href, _ := ma["href"].(string)
				if id, ok := internalPageID(href); ok {
					// 带域名的 /p/ 链接只有确实是本站页面时才当站内链接处理，别误伤外站链接。
					if _, known := sc.cat.Page(id); known || !strings.HasPrefix(href, "http") {
						if !sc.has(id) {
							continue // 范围外：只留文字，不留链接
						}
						ma["href"] = "/share/" + token + "/" + id
					}
				}
			}
			out = append(out, mm)
		}
		if len(out) == 0 {
			delete(n, "marks")
		} else {
			n["marks"] = out
		}
	}
	if content, ok := n["content"].([]any); ok {
		for _, c := range content {
			if cm, ok := c.(map[string]any); ok {
				sc.sanitizeNode(cm, token)
			}
		}
	}
}

// internalPageID 识别指向站内页面的链接：/p/<id>、http(s)://host/p/<id>、lemon://page/<id>。
func internalPageID(href string) (string, bool) {
	if strings.HasPrefix(href, "lemon://page/") {
		return strings.TrimPrefix(href, "lemon://page/"), true
	}
	if m := pageHref.FindStringSubmatch(href); m != nil {
		return m[1], true
	}
	return "", false
}
