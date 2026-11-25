package lnmd

import (
	"html"
	"strconv"
	"strings"
)

type tagKind int

const (
	tagOpen tagKind = iota
	tagClose
	tagSelf
)

// attrKV 是标签或块属性里的一个键值对，保留书写顺序。
type attrKV struct {
	key    string
	val    string
	hasVal bool
}

// tagTok 是解析出的一个 HTML 风格标签。
type tagTok struct {
	name  string
	kind  tagKind
	attrs []attrKV
	raw   string
}

func (t *tagTok) get(k string) string {
	for _, a := range t.attrs {
		if a.key == k {
			return a.val
		}
	}
	return ""
}

func (t *tagTok) has(k string) bool {
	for _, a := range t.attrs {
		if a.key == k {
			return true
		}
	}
	return false
}

// blockTagNames 是增强 Markdown 的块级自定义标签。
var blockTagNames = map[string]bool{
	"callout": true, "details": true, "summary": true, "columns": true, "column": true,
	"synced-block": true, "page": true, "database": true, "link-to-page": true,
	"file": true, "video": true, "audio": true, "bookmark": true, "embed": true,
	"whiteboard": true, "toc": true, "breadcrumb": true, "empty-block": true,
}

// containerTag 报告标签是否为容器（开闭成对、内部是 Markdown 块）。
func containerTag(t *tagTok) bool {
	switch t.name {
	case "callout", "details", "summary", "columns", "column":
		return true
	case "synced-block":
		return !t.has("source")
	}
	return false
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isASCIIDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isASCIIAlnum(c byte) bool  { return isASCIILetter(c) || isASCIIDigit(c) }

// parseTagAt 在 s[i]=='<' 处尝试解析一个标签（任意标签名）。容忍属性值缺引号、中文引号。
func parseTagAt(s string, i int) (tagTok, int, bool) {
	var t tagTok
	if i >= len(s) || s[i] != '<' {
		return t, 0, false
	}
	j := i + 1
	closing := false
	if j < len(s) && s[j] == '/' {
		closing = true
		j++
	}
	ns := j
	if j >= len(s) || !isASCIILetter(s[j]) {
		return t, 0, false
	}
	for j < len(s) && (isASCIIAlnum(s[j]) || s[j] == '-' || s[j] == '_') {
		j++
	}
	t.name = strings.ToLower(s[ns:j])
	if closing {
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j < len(s) && s[j] == '>' {
			t.kind = tagClose
			t.raw = s[i : j+1]
			return t, j + 1, true
		}
		return t, 0, false
	}
	if j < len(s) && !(s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '/' || s[j] == '>') {
		return t, 0, false
	}
	for {
		for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n') {
			j++
		}
		if j >= len(s) {
			return t, 0, false
		}
		if s[j] == '>' {
			t.kind = tagOpen
			t.raw = s[i : j+1]
			return t, j + 1, true
		}
		if s[j] == '/' {
			if j+1 < len(s) && s[j+1] == '>' {
				t.kind = tagSelf
				t.raw = s[i : j+2]
				return t, j + 2, true
			}
			return t, 0, false
		}
		as := j
		for j < len(s) && !strings.ContainsRune(" \t\n=/>\"'<", rune(s[j])) {
			j++
		}
		if j == as {
			return t, 0, false
		}
		key := strings.ToLower(s[as:j])
		k := j
		for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
			k++
		}
		if k < len(s) && s[k] == '=' {
			j = k + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j >= len(s) {
				return t, 0, false
			}
			var val string
			switch {
			case s[j] == '"' || s[j] == '\'':
				q := s[j]
				e := strings.IndexByte(s[j+1:], q)
				if e < 0 {
					return t, 0, false
				}
				val = s[j+1 : j+1+e]
				j = j + 2 + e
			case strings.HasPrefix(s[j:], "“"):
				e := strings.Index(s[j+len("“"):], "”")
				if e < 0 {
					return t, 0, false
				}
				val = s[j+len("“") : j+len("“")+e]
				j = j + len("“") + e + len("”")
			default:
				vs := j
				for j < len(s) && !(s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '>' || s[j] == '<') {
					if s[j] == '/' && j+1 < len(s) && s[j+1] == '>' {
						break
					}
					j++
				}
				val = s[vs:j]
			}
			t.attrs = append(t.attrs, attrKV{key: key, val: html.UnescapeString(val), hasVal: true})
		} else {
			j = k
			t.attrs = append(t.attrs, attrKV{key: key})
		}
	}
}

// escAttr 转义属性值。
func escAttr(v string, inTable bool) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\n':
			b.WriteString("&#10;")
		case '\r':
			b.WriteString("&#13;")
		case '|':
			if inTable {
				b.WriteString("&#124;")
			} else {
				b.WriteRune(r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tagBuilder 按固定顺序拼接标签属性。
type tagBuilder struct {
	b       strings.Builder
	inTable bool
}

func newTag(name string) *tagBuilder {
	t := &tagBuilder{}
	t.b.WriteByte('<')
	t.b.WriteString(name)
	return t
}

func (t *tagBuilder) attr(k, v string) *tagBuilder {
	if v == "" {
		return t
	}
	t.b.WriteByte(' ')
	t.b.WriteString(k)
	t.b.WriteString(`="`)
	t.b.WriteString(escAttr(v, t.inTable))
	t.b.WriteByte('"')
	return t
}

func (t *tagBuilder) flag(k string, on bool) *tagBuilder {
	if on {
		t.b.WriteByte(' ')
		t.b.WriteString(k)
	}
	return t
}

func (t *tagBuilder) open() string { return t.b.String() + ">" }
func (t *tagBuilder) self() string { return t.b.String() + "/>" }
func closeTag(name string) string  { return "</" + name + ">" }

// ---- 块属性尾注 {color="..." align="..."} ----

var trailerKeys = map[string]bool{"color": true, "align": true, "width": true, "height": true, "textalign": true}

// findTrailer 查找行尾的块属性尾注，返回 '{' 的字节位置。'{' 被反斜杠转义时不算。
func findTrailer(line string) (int, []attrKV, bool) {
	t := strings.TrimRight(line, " \t")
	if !strings.HasSuffix(t, "}") {
		return -1, nil, false
	}
	start := strings.LastIndexByte(t, '{')
	if start < 0 {
		return -1, nil, false
	}
	bs := 0
	for k := start - 1; k >= 0 && t[k] == '\\'; k-- {
		bs++
	}
	if bs%2 == 1 {
		return -1, nil, false
	}
	attrs, ok := parseTrailerAttrs(t[start+1 : len(t)-1])
	if !ok {
		return -1, nil, false
	}
	return start, attrs, true
}

// parseTrailerAttrs 解析尾注内部：key=value 以空白分隔，键必须是已知键。
func parseTrailerAttrs(s string) ([]attrKV, bool) {
	var out []attrKV
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		ks := i
		for i < len(s) && (isASCIIAlnum(s[i]) || s[i] == '_' || s[i] == '-') {
			i++
		}
		if i == ks {
			return nil, false
		}
		key := strings.ToLower(s[ks:i])
		if !trailerKeys[key] {
			return nil, false
		}
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			return nil, false
		}
		i++
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return nil, false
		}
		var val string
		quoted := false
		if s[i] == '"' || s[i] == '\'' {
			q := s[i]
			e := strings.IndexByte(s[i+1:], q)
			if e < 0 {
				return nil, false
			}
			val = s[i+1 : i+1+e]
			i = i + 2 + e
			quoted = true
		} else {
			vs := i
			for i < len(s) && !strings.ContainsRune(" \t\"'{}", rune(s[i])) {
				i++
			}
			if i == vs {
				return nil, false
			}
			val = s[vs:i]
		}
		if key == "textalign" {
			key = "align"
		}
		out = append(out, attrKV{key: key, val: val, hasVal: quoted})
	}
	return out, len(out) > 0
}

// trailerValue 取尾注里键对应的值；未加引号的数字转成 float64。
func trailerValue(a attrKV) any {
	if a.hasVal {
		return a.val
	}
	return parseNumberValue(a.val)
}

// formatTrailer 生成块属性尾注文本；kvs 为空时返回空串。
func formatTrailer(kvs [][2]string, numeric map[string]bool) string {
	var parts []string
	for _, kv := range kvs {
		if kv[1] == "" {
			continue
		}
		v := strings.NewReplacer(`"`, "", "\n", " ", "{", "", "}", "").Replace(kv[1])
		if numeric[kv[0]] {
			if _, err := strconv.ParseFloat(v, 64); err == nil {
				parts = append(parts, kv[0]+"="+v)
				continue
			}
		}
		parts = append(parts, kv[0]+`="`+v+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, " ") + "}"
}
