package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
)

// csvTable 是解析出的 CSV。
type csvTable struct {
	header []string
	rows   [][]string
}

// parseCSV 解析 CSV（容忍引号不规范、列数不齐；自动识别分号与制表符分隔）。
func parseCSV(b []byte, maxRows int) (*csvTable, bool, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(b))
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	first, _, _ := bytes.Cut(b, []byte("\n"))
	switch {
	case bytes.Count(first, []byte("\t")) > bytes.Count(first, []byte(",")):
		r.Comma = '\t'
	case bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")):
		r.Comma = ';'
	}
	t := &csvTable{}
	truncated := false
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, fail("CSV 格式有误：%v", err)
		}
		if t.header == nil {
			t.header = rec
			continue
		}
		if maxRows > 0 && len(t.rows) >= maxRows {
			truncated = true
			break
		}
		empty := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		t.rows = append(t.rows, rec)
	}
	if len(t.header) == 0 {
		return nil, false, fail("CSV 没有表头")
	}
	for i, h := range t.header {
		t.header[i] = strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
	}
	return t, truncated, nil
}

func (t *csvTable) cell(row []string, i int) string {
	if i < len(row) {
		return strings.TrimSpace(row[i])
	}
	return ""
}

// column 是推断出的一列。
type column struct {
	idx   int
	prop  string // 属性 id
	typ   string
	scale float64 // 数字：百分比列按 0.01 存
	// 关联：单元格里的条目 → 行
	rel func(item string) *pnode
	// 文件：单元格里的条目 → 导入包里的文件
	file func(item string) *vfile
}

// inferOpts 给类型推断提供关联与文件的解析。
type inferOpts struct {
	relation func(item string) *pnode // 解析「标题 (路径)」形式的条目；返回目标行
	file     func(item string) *vfile
}

// inferSchema 按列推断属性类型。第一列（或名为「名称 / Name / Title」的列）是标题。
func inferSchema(t *csvTable, o inferOpts, zh bool) (*dbs.Schema, []column, int) {
	titleIdx := 0 // 第一列是标题（Notion 导出的 CSV 也是这样）
	sc := &dbs.Schema{}
	titleName := t.header[titleIdx]
	if titleName == "" {
		titleName = map[bool]string{true: "名称", false: "Name"}[zh]
	}
	sc.Properties = append(sc.Properties, dbs.Property{ID: "title", Name: titleName, Type: dbs.TTitle})
	used := map[string]bool{strings.ToLower(titleName): true}
	var cols []column
	for i, h := range t.header {
		if i == titleIdx {
			continue
		}
		name := strings.TrimSpace(h)
		if name == "" {
			name = fmt.Sprintf("%s %d", map[bool]string{true: "列", false: "Column"}[zh], i+1)
		}
		if utf8.RuneCountInString(name) > 100 {
			name = string([]rune(name)[:100])
		}
		base := name
		for k := 2; used[strings.ToLower(name)]; k++ {
			name = fmt.Sprintf("%s %d", base, k)
		}
		used[strings.ToLower(name)] = true
		vals := make([]string, 0, len(t.rows))
		for _, r := range t.rows {
			if v := t.cell(r, i); v != "" {
				vals = append(vals, v)
			}
		}
		col := column{idx: i, prop: fmt.Sprintf("p%02d", i), scale: 1}
		prop := dbs.Property{ID: col.prop, Name: name}
		inferColumn(&col, &prop, name, vals, o)
		sc.Properties = append(sc.Properties, prop)
		cols = append(cols, col)
	}
	return sc, cols, titleIdx
}

var (
	numRe   = regexp.MustCompile(`^[-+]?[$¥￥€£]?\s?[-+]?(\d{1,3}(,\d{3})+|\d+)(\.\d+)?\s?%?$`)
	phoneRe = regexp.MustCompile(`^\+?[\d\s\-().]{7,20}$`)
	urlRe   = regexp.MustCompile(`^(https?://|www\.)\S+$`)
)

func isBool(s string) bool {
	switch strings.ToLower(s) {
	case "yes", "no", "true", "false", "是", "否":
		return true
	}
	return false
}

func parseNumber(s string) (float64, bool) {
	if !numRe.MatchString(s) {
		return 0, false
	}
	pct := strings.HasSuffix(s, "%")
	c := strings.NewReplacer(",", "", "$", "", "¥", "", "￥", "", "€", "", "£", "", "%", "", " ", "").Replace(s)
	f, err := strconv.ParseFloat(c, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if pct {
		f /= 100
	}
	return f, true
}

func isPhone(s string) bool {
	if !phoneRe.MatchString(s) || !strings.ContainsAny(s, "+-() ") {
		return false
	}
	d := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			d++
		}
	}
	return d >= 7 && d <= 15
}

func isEmail(s string) bool {
	if strings.ContainsAny(s, " <>,") || !strings.Contains(s, "@") {
		return false
	}
	_, err := mail.ParseAddress(s)
	return err == nil
}

func all(vals []string, fn func(string) bool) bool {
	for _, v := range vals {
		if !fn(v) {
			return false
		}
	}
	return true
}

// splitMulti 拆多选值（Notion 导出用「, 」分隔）。
func splitMulti(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var selectHints = []string{"tag", "tags", "标签", "type", "类型", "category", "分类", "status", "状态", "priority", "优先级", "stage", "阶段", "label", "labels"}

func inferColumn(col *column, p *dbs.Property, name string, vals []string, o inferOpts) {
	p.Type = dbs.TText
	col.typ = dbs.TText
	if len(vals) == 0 {
		return
	}
	set := func(t string) { p.Type, col.typ = t, t }
	switch {
	case all(vals, isBool):
		set(dbs.TCheckbox)
		return
	case all(vals, func(s string) bool { _, ok := parseNumber(s); return ok }):
		set(dbs.TNumber)
		switch v := vals[0]; {
		case strings.HasSuffix(v, "%"):
			p.Format = "percent"
		case strings.ContainsAny(v, "$"):
			p.Format = "dollar"
		case strings.ContainsAny(v, "¥￥"):
			p.Format = "yuan"
		case strings.ContainsAny(v, "€"):
			p.Format = "euro"
		case strings.Contains(v, ","):
			p.Format = "comma"
		}
		return
	case all(vals, func(s string) bool { _, _, ok := parseDateRange(s); return ok }):
		set(dbs.TDate)
		return
	}
	if o.relation != nil {
		var target *pnode
		ok := all(vals, func(s string) bool {
			items := relItems(s)
			if len(items) == 0 {
				return false
			}
			for _, it := range items {
				r := o.relation(it)
				if r == nil || r.kind != nRow || (target != nil && r.parent != target) {
					return false
				}
				target = r.parent
			}
			return true
		})
		if ok && target != nil {
			set(dbs.TRelation)
			p.DatabaseID = target.id
			col.rel = o.relation
			return
		}
	}
	if o.file != nil && all(vals, func(s string) bool {
		for _, it := range splitMulti(s) {
			if o.file(it) == nil {
				return false
			}
		}
		return true
	}) {
		set(dbs.TFiles)
		col.file = o.file
		return
	}
	switch {
	case all(vals, func(s string) bool { return urlRe.MatchString(s) }):
		set(dbs.TURL)
		return
	case all(vals, isEmail):
		set(dbs.TEmail)
		return
	case all(vals, isPhone):
		set(dbs.TPhone)
		return
	}
	// 单选 / 多选：值都很短、不同的值不多且有重复
	short := true
	multi := false
	distinct := map[string]bool{}
	occ := 0
	for _, v := range vals {
		items := splitMulti(v)
		if len(items) > 1 {
			multi = true
		}
		for _, it := range items {
			if utf8.RuneCountInString(it) > 40 || strings.ContainsAny(it, "\n\r") {
				short = false
			}
			distinct[it] = true
			occ++
		}
	}
	if !short || len(distinct) == 0 {
		return
	}
	hint := false
	ln := strings.ToLower(name)
	for _, h := range selectHints {
		if ln == h || strings.HasSuffix(ln, " "+h) {
			hint = true
		}
	}
	repeats := len(distinct) < occ
	if multi && len(distinct) <= 100 && (repeats || hint) {
		set(dbs.TMultiSelect)
	} else if !multi && len(distinct) <= 50 && (repeats && len(distinct)*3 <= len(vals)*2 || hint) {
		set(dbs.TSelect)
	} else {
		return
	}
	// 选项按首次出现的顺序
	seen := map[string]bool{}
	for _, v := range vals {
		items := []string{v}
		if p.Type == dbs.TMultiSelect {
			items = splitMulti(v)
		}
		for _, it := range items {
			k := strings.ToLower(it)
			if !seen[k] {
				seen[k] = true
				p.Options = append(p.Options, dbs.Option{ID: dbs.NewOptionID(), Name: it})
			}
		}
	}
}

// relRe 匹配 Notion 关联单元格里「标题 (地址)」条目的地址部分。
var relRe = regexp.MustCompile(`\(([^()\s]+?\.md|https?://[^()\s]+)\)`)

// relItems 拆出关联单元格里的地址。
func relItems(s string) []string {
	var out []string
	for _, m := range relRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// cellValue 把单元格转成属性的存储格式；转不了返回 nil 与原因。
func (c *column) cellValue(p *dbs.Property, s string) (any, string) {
	if s == "" {
		return nil, ""
	}
	switch c.typ {
	case dbs.TCheckbox:
		l := strings.ToLower(s)
		return l == "yes" || l == "true" || l == "是", ""
	case dbs.TNumber:
		f, ok := parseNumber(s)
		if !ok {
			return nil, "不是数字"
		}
		return f, ""
	case dbs.TDate:
		a, e, ok := parseDateRange(s)
		if !ok {
			return nil, "不是日期"
		}
		m := map[string]any{"start": a}
		if e != "" {
			m["end"] = e
		}
		return m, ""
	case dbs.TSelect:
		if o := p.OptionByName(s); o != nil {
			return o.ID, ""
		}
		return nil, "没有这个选项"
	case dbs.TMultiSelect:
		var out []any
		for _, it := range splitMulti(s) {
			if o := p.OptionByName(it); o != nil {
				out = append(out, o.ID)
			}
		}
		if len(out) == 0 {
			return nil, ""
		}
		return out, ""
	case dbs.TRelation:
		var out []any
		for _, it := range relItems(s) {
			if r := c.rel(it); r != nil {
				out = append(out, r.id)
			}
		}
		if len(out) == 0 {
			return nil, ""
		}
		return out, ""
	case dbs.TURL:
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		return s, ""
	}
	return s, ""
}

// ---------- 日期 ----------

var dateLayouts = []struct {
	layout  string
	hasTime bool
}{
	{"January 2, 2006 3:04 PM", true}, {"January 2, 2006 15:04", true}, {"Jan 2, 2006 3:04 PM", true}, {"January 2, 2006", false}, {"Jan 2, 2006", false},
	{"2006-01-02T15:04:05Z07:00", true}, {"2006-01-02T15:04:05.000Z07:00", true}, {"2006-01-02T15:04:05", true}, {"2006-01-02T15:04", true},
	{"2006-01-02 15:04:05", true}, {"2006-01-02 15:04", true}, {"2006/01/02 15:04", true}, {"2006/1/2 15:04", true},
	{"2006-01-02", false}, {"2006/01/02", false}, {"2006/1/2", false}, {"2006-1-2", false},
	{"2006年1月2日 15:04", true}, {"2006年1月2日", false}, {"01/02/2006", false},
}

// parseDate 把常见日期写法转成 2006-01-02 或 2006-01-02 15:04。
func parseDate(s string) (string, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	if s == "" || len(s) > 40 {
		return "", false
	}
	if i := strings.Index(s, " (GMT"); i > 0 {
		s = s[:i]
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l.layout, s); err == nil {
			if t.Year() < 1000 || t.Year() > 9999 {
				return "", false
			}
			if l.hasTime {
				if strings.Contains(l.layout, "Z07:00") {
					t = t.In(time.Local)
				}
				return t.Format("2006-01-02 15:04"), true
			}
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// parseDateRange 解析「开始 → 结束」形式的日期范围。
func parseDateRange(s string) (string, string, bool) {
	for _, sep := range []string{"→", " ~ ", " - ", " – "} {
		if a, e, ok := strings.Cut(s, sep); ok {
			sa, ok1 := parseDate(a)
			se, ok2 := parseDate(e)
			if ok1 && ok2 {
				return sa, se, true
			}
			if sep == "→" {
				if ok1 && strings.TrimSpace(e) == "" {
					return sa, "", true
				}
				return "", "", false
			}
		}
	}
	d, ok := parseDate(s)
	return d, "", ok
}

// ---------- 建数据库计划 ----------

// csvPlan 用解析好的 CSV 生成数据库节点与行节点。rowFor 按行标题找已有的行页面节点（Notion 导出里行有自己的 .md）。
func csvPlan(b *builder, v *vfs, db *pnode, t *csvTable, o inferOpts, rowFor func(title string) *pnode) {
	sc, cols, titleIdx := inferSchema(t, o, b.zh)
	if err := sc.Validate(); err != nil {
		// 推断出的结构不合法（例如列名冲突）：全部退回文本
		for i := range sc.Properties {
			p := &sc.Properties[i]
			if p.Type != dbs.TTitle {
				p.Type, p.Options, p.Format, p.DatabaseID = dbs.TText, nil, "", ""
			}
		}
		for i := range cols {
			cols[i].typ = dbs.TText
		}
		_ = sc.Validate()
	}
	db.schema = sc
	for _, r := range t.rows {
		title := t.cell(r, titleIdx)
		row := rowFor(title)
		if row == nil {
			row = newNode(nRow, title, db.src)
			db.add(row)
		}
		row.kind = nRow
		if row.title == "" {
			row.title = title
		}
		cells := map[string]string{}
		for _, c := range cols {
			cells[c.prop] = t.cell(r, c.idx)
		}
		colsCopy := cols
		prev := row.prepare
		row.prepare = func(ctx context.Context, b *builder, n *pnode) error {
			if prev != nil {
				if err := prev(ctx, b, n); err != nil {
					return err
				}
			}
			if n.props == nil {
				n.props = map[string]any{}
			}
			for _, c := range colsCopy {
				s := cells[c.prop]
				p := db.schema.Prop(c.prop)
				if p == nil || s == "" {
					continue
				}
				if c.typ == dbs.TFiles {
					var list []any
					for _, it := range splitMulti(s) {
						f := c.file(it)
						if f == nil {
							continue
						}
						m, err := b.uploadVFS(v, f, n.id)
						if err != nil {
							return err
						}
						if m != nil {
							list = append(list, map[string]any{"file_id": m.ID, "name": m.Name})
						}
					}
					if len(list) > 0 {
						n.props[c.prop] = list
					}
					continue
				}
				v, why := c.cellValue(p, s)
				if v == nil {
					if why != "" {
						b.warn("value_dropped", db.src, p.Name+"："+s, "属性值无法识别，已留空："+why)
					}
					continue
				}
				nv, _, err := dbs.NormalizeValue(p, v, dbs.Resolver{
					Relation: func(_, id string) (string, bool) { return id, true },
					User:     func(id string) (string, bool) { return id, true },
				})
				if err != nil || nv == nil {
					b.warn("value_dropped", db.src, p.Name+"："+s, "属性值无法识别，已留空")
					continue
				}
				n.props[c.prop] = nv
			}
			return nil
		}
	}
}
