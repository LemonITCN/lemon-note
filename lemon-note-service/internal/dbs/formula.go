package dbs

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// 公式语言：兼容 Notion 公式 2.0 的常用写法。
// 例：if(prop("数量") > 10, "多", "少")、prop("单价") * prop("数量")、dateBetween(prop("截止"), now(), "days")、prop("标签").length()

// DateVal 是日期值。
type DateVal struct {
	Start   time.Time
	End     *time.Time
	HasTime bool
}

// Value 是公式里的值：nil、float64、string、bool、DateVal、[]Value。
type Value = any

type node interface{}

type numLit struct{ v float64 }
type strLit struct{ v string }
type boolLit struct{ v bool }
type listLit struct{ items []node }
type propRef struct{ name string }
type call struct {
	fn   string
	args []node
}
type binOp struct {
	op   string
	l, r node
}
type unOp struct {
	op string
	x  node
}

// Formula 是解析后的公式。
type Formula struct{ root node }

// ---------- 词法 ----------

type token struct {
	kind string // num str ident op ( ) , [ ] . eof
	text string
	num  float64
}

func lex(src string) ([]token, error) {
	var out []token
	r := []rune(src)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case unicode.IsDigit(c) || (c == '.' && i+1 < len(r) && unicode.IsDigit(r[i+1])):
			j := i
			for j < len(r) && (unicode.IsDigit(r[j]) || r[j] == '.' || r[j] == 'e' || r[j] == 'E' || ((r[j] == '-' || r[j] == '+') && j > i && (r[j-1] == 'e' || r[j-1] == 'E'))) {
				j++
			}
			v, err := strconv.ParseFloat(string(r[i:j]), 64)
			if err != nil {
				return nil, fmt.Errorf("数字格式不对：%s", string(r[i:j]))
			}
			out = append(out, token{kind: "num", num: v})
			i = j
		case c == '"' || c == '\'' || c == '“' || c == '”' || c == '‘':
			q := c
			closing := q
			if q == '“' {
				closing = '”'
			}
			if q == '‘' {
				closing = '’'
			}
			var b strings.Builder
			j := i + 1
			for ; j < len(r) && r[j] != closing && !(q == '“' && r[j] == '"'); j++ {
				if r[j] == '\\' && j+1 < len(r) {
					j++
					switch r[j] {
					case 'n':
						b.WriteRune('\n')
					case 't':
						b.WriteRune('\t')
					default:
						b.WriteRune(r[j])
					}
					continue
				}
				b.WriteRune(r[j])
			}
			if j >= len(r) {
				return nil, errors.New("字符串缺少结束引号")
			}
			out = append(out, token{kind: "str", text: b.String()})
			i = j + 1
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_') {
				j++
			}
			out = append(out, token{kind: "ident", text: string(r[i:j])})
			i = j
		default:
			two := ""
			if i+1 < len(r) {
				two = string(r[i : i+2])
			}
			switch two {
			case "==", "!=", ">=", "<=", "&&", "||":
				out = append(out, token{kind: "op", text: two})
				i += 2
				continue
			}
			switch c {
			case '+', '-', '*', '/', '%', '^', '>', '<', '!':
				out = append(out, token{kind: "op", text: string(c)})
			case '=':
				out = append(out, token{kind: "op", text: "=="})
			case '（':
				out = append(out, token{kind: "("})
			case '）':
				out = append(out, token{kind: ")"})
			case '，':
				out = append(out, token{kind: ","})
			case '(', ')', ',', '[', ']', '.', '?', ':':
				out = append(out, token{kind: string(c)})
			default:
				return nil, fmt.Errorf("无法识别的字符：%c", c)
			}
			i++
		}
	}
	return append(out, token{kind: "eof"}), nil
}

// ---------- 语法 ----------

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *parser) isOp(s ...string) bool {
	t := p.peek()
	for _, x := range s {
		if (t.kind == "op" && t.text == x) || (t.kind == "ident" && strings.EqualFold(t.text, x)) {
			return true
		}
	}
	return false
}

// ParseFormula 解析公式。
func ParseFormula(src string) (*Formula, error) {
	if strings.TrimSpace(src) == "" {
		return &Formula{root: strLit{}}, nil
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != "eof" {
		return nil, fmt.Errorf("多余的内容：%s", p.peek().text)
	}
	return &Formula{root: n}, nil
}

func (p *parser) expr() (node, error) {
	cond, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == "?" {
		p.next()
		a, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.next().kind != ":" {
			return nil, errors.New("三元表达式缺少 :")
		}
		b, err := p.expr()
		if err != nil {
			return nil, err
		}
		return call{fn: "if", args: []node{cond, a, b}}, nil
	}
	return cond, nil
}

func (p *parser) or() (node, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isOp("||", "or") {
		p.next()
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = binOp{op: "or", l: l, r: r}
	}
	return l, nil
}

func (p *parser) and() (node, error) {
	l, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.isOp("&&", "and") {
		p.next()
		r, err := p.not()
		if err != nil {
			return nil, err
		}
		l = binOp{op: "and", l: l, r: r}
	}
	return l, nil
}

func (p *parser) not() (node, error) {
	if p.isOp("!", "not") {
		p.next()
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return unOp{op: "not", x: x}, nil
	}
	return p.cmp()
}

func (p *parser) cmp() (node, error) {
	l, err := p.add()
	if err != nil {
		return nil, err
	}
	for p.isOp("==", "!=", ">", "<", ">=", "<=") {
		op := p.next().text
		r, err := p.add()
		if err != nil {
			return nil, err
		}
		l = binOp{op: op, l: l, r: r}
	}
	return l, nil
}

func (p *parser) add() (node, error) {
	l, err := p.mul()
	if err != nil {
		return nil, err
	}
	for p.isOp("+", "-") {
		op := p.next().text
		r, err := p.mul()
		if err != nil {
			return nil, err
		}
		l = binOp{op: op, l: l, r: r}
	}
	return l, nil
}

func (p *parser) mul() (node, error) {
	l, err := p.pow()
	if err != nil {
		return nil, err
	}
	for p.isOp("*", "/", "%") {
		op := p.next().text
		r, err := p.pow()
		if err != nil {
			return nil, err
		}
		l = binOp{op: op, l: l, r: r}
	}
	return l, nil
}

func (p *parser) pow() (node, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	if p.isOp("^") {
		p.next()
		r, err := p.pow()
		if err != nil {
			return nil, err
		}
		return binOp{op: "^", l: l, r: r}, nil
	}
	return l, nil
}

func (p *parser) unary() (node, error) {
	if p.isOp("-") {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return unOp{op: "-", x: x}, nil
	}
	if p.isOp("+") {
		p.next()
		return p.unary()
	}
	return p.postfix()
}

func (p *parser) postfix() (node, error) {
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == "." {
		p.next()
		t := p.next()
		if t.kind != "ident" {
			return nil, errors.New("点号后面应该是函数名")
		}
		args := []node{x}
		if p.peek().kind == "(" {
			more, err := p.args()
			if err != nil {
				return nil, err
			}
			args = append(args, more...)
		}
		x = call{fn: strings.ToLower(t.text), args: args}
	}
	return x, nil
}

func (p *parser) args() ([]node, error) {
	p.next() // (
	var out []node
	if p.peek().kind == ")" {
		p.next()
		return out, nil
	}
	for {
		a, err := p.expr()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		t := p.next()
		if t.kind == ")" {
			return out, nil
		}
		if t.kind != "," {
			return nil, errors.New("参数之间应该用逗号分隔")
		}
	}
}

func (p *parser) primary() (node, error) {
	t := p.next()
	switch t.kind {
	case "num":
		return numLit{v: t.num}, nil
	case "str":
		return strLit{v: t.text}, nil
	case "(":
		x, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.next().kind != ")" {
			return nil, errors.New("缺少右括号")
		}
		return x, nil
	case "[":
		var items []node
		if p.peek().kind == "]" {
			p.next()
			return listLit{}, nil
		}
		for {
			x, err := p.expr()
			if err != nil {
				return nil, err
			}
			items = append(items, x)
			t := p.next()
			if t.kind == "]" {
				return listLit{items: items}, nil
			}
			if t.kind != "," {
				return nil, errors.New("列表元素之间应该用逗号分隔")
			}
		}
	case "ident":
		name := strings.ToLower(t.text)
		switch name {
		case "true":
			return boolLit{v: true}, nil
		case "false":
			return boolLit{v: false}, nil
		}
		if p.peek().kind == "(" {
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			if name == "prop" {
				if len(args) != 1 {
					return nil, errors.New("prop() 需要一个属性名")
				}
				s, ok := args[0].(strLit)
				if !ok {
					return nil, errors.New("prop() 的参数必须是字符串")
				}
				return propRef{name: s.v}, nil
			}
			if _, ok := funcs[name]; !ok {
				return nil, fmt.Errorf("未知的函数：%s", t.text)
			}
			return call{fn: name, args: args}, nil
		}
		return propRef{name: t.text}, nil // 裸写属性名
	}
	return nil, fmt.Errorf("意外的符号：%s", t.text)
}

// ---------- 求值 ----------

// EvalEnv 求值环境。
type EvalEnv struct {
	Prop  func(name string) (Value, error)
	ID    string
	depth int
}

// Eval 计算公式。
func (f *Formula) Eval(env *EvalEnv) (Value, error) {
	return eval(f.root, env)
}

func eval(n node, env *EvalEnv) (Value, error) {
	switch x := n.(type) {
	case numLit:
		return x.v, nil
	case strLit:
		return x.v, nil
	case boolLit:
		return x.v, nil
	case listLit:
		out := make([]Value, 0, len(x.items))
		for _, it := range x.items {
			v, err := eval(it, env)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case propRef:
		if env.Prop == nil {
			return nil, nil
		}
		return env.Prop(x.name)
	case unOp:
		v, err := eval(x.x, env)
		if err != nil {
			return nil, err
		}
		if x.op == "not" {
			return !truthy(v), nil
		}
		return -toNum(v), nil
	case binOp:
		if x.op == "and" || x.op == "or" {
			l, err := eval(x.l, env)
			if err != nil {
				return nil, err
			}
			if x.op == "and" && !truthy(l) {
				return false, nil
			}
			if x.op == "or" && truthy(l) {
				return true, nil
			}
			r, err := eval(x.r, env)
			if err != nil {
				return nil, err
			}
			return truthy(r), nil
		}
		l, err := eval(x.l, env)
		if err != nil {
			return nil, err
		}
		r, err := eval(x.r, env)
		if err != nil {
			return nil, err
		}
		return binary(x.op, l, r)
	case call:
		if x.fn == "if" {
			if len(x.args) != 3 {
				return nil, errors.New("if() 需要 3 个参数")
			}
			c, err := eval(x.args[0], env)
			if err != nil {
				return nil, err
			}
			if truthy(c) {
				return eval(x.args[1], env)
			}
			return eval(x.args[2], env)
		}
		if x.fn == "ifs" {
			for i := 0; i+1 < len(x.args); i += 2 {
				c, err := eval(x.args[i], env)
				if err != nil {
					return nil, err
				}
				if truthy(c) {
					return eval(x.args[i+1], env)
				}
			}
			if len(x.args)%2 == 1 {
				return eval(x.args[len(x.args)-1], env)
			}
			return nil, nil
		}
		args := make([]Value, len(x.args))
		for i, a := range x.args {
			v, err := eval(a, env)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		fn := funcs[x.fn]
		if fn == nil {
			return nil, fmt.Errorf("未知的函数：%s", x.fn)
		}
		return fn(env, args)
	}
	return nil, errors.New("无法计算")
}

func binary(op string, l, r Value) (Value, error) {
	switch op {
	case "+":
		if ls, ok := l.(string); ok {
			return ls + ToText(r), nil
		}
		if rs, ok := r.(string); ok {
			return ToText(l) + rs, nil
		}
		if la, ok := l.([]Value); ok {
			if ra, ok := r.([]Value); ok {
				return append(append([]Value{}, la...), ra...), nil
			}
		}
		return toNum(l) + toNum(r), nil
	case "-":
		if ld, ok := l.(DateVal); ok {
			if rd, ok := r.(DateVal); ok {
				return ld.Start.Sub(rd.Start).Hours() / 24, nil
			}
		}
		return toNum(l) - toNum(r), nil
	case "*":
		return toNum(l) * toNum(r), nil
	case "/":
		d := toNum(r)
		if d == 0 {
			return nil, errors.New("除数为 0")
		}
		return toNum(l) / d, nil
	case "%":
		d := toNum(r)
		if d == 0 {
			return nil, errors.New("除数为 0")
		}
		return math.Mod(toNum(l), d), nil
	case "^":
		return math.Pow(toNum(l), toNum(r)), nil
	case "==":
		return Compare(l, r) == 0, nil
	case "!=":
		return Compare(l, r) != 0, nil
	case ">":
		return Compare(l, r) > 0, nil
	case "<":
		return Compare(l, r) < 0, nil
	case ">=":
		return Compare(l, r) >= 0, nil
	case "<=":
		return Compare(l, r) <= 0, nil
	}
	return nil, fmt.Errorf("未知的运算符 %s", op)
}

func truthy(v Value) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []Value:
		return len(x) > 0
	}
	return true
}

func toNum(v Value) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(x, ",", "")), 64)
		return f
	case DateVal:
		return float64(x.Start.UnixMilli())
	case []Value:
		return float64(len(x))
	}
	return 0
}

// ToText 把值转成显示文本。
func ToText(v Value) string {
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
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case DateVal:
		f := "2006-01-02"
		if x.HasTime {
			f = "2006-01-02 15:04"
		}
		s := x.Start.Format(f)
		if x.End != nil {
			s += " → " + x.End.Format(f)
		}
		return s
	case []Value:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = ToText(e)
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

// Compare 比较两个值（排序与筛选共用）：nil 最小。
func Compare(a, b Value) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	switch x := a.(type) {
	case float64:
		y := toNum(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case bool:
		y := truthy(b)
		if x == y {
			return 0
		}
		if !x {
			return -1
		}
		return 1
	case DateVal:
		if y, ok := b.(DateVal); ok {
			return x.Start.Compare(y.Start)
		}
	}
	return strings.Compare(strings.ToLower(ToText(a)), strings.ToLower(ToText(b)))
}

func toDate(v Value) (DateVal, bool) {
	switch x := v.(type) {
	case DateVal:
		return x, true
	case string:
		return ParseDate(x)
	case float64:
		return DateVal{Start: time.UnixMilli(int64(x))}, true
	}
	return DateVal{}, false
}

// ParseDate 解析日期文本。
func ParseDate(s string) (DateVal, bool) {
	s = strings.TrimSpace(s)
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return DateVal{Start: t, HasTime: true}, true
		}
	}
	for _, f := range []string{"2006-01-02", "2006/01/02", "2006年1月2日"} {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return DateVal{Start: t}, true
		}
	}
	return DateVal{}, false
}

func listOf(args []Value) []Value {
	if len(args) == 1 {
		if l, ok := args[0].([]Value); ok {
			return l
		}
	}
	return args
}

func durUnit(unit string) (time.Duration, string) {
	switch strings.ToLower(strings.TrimSuffix(strings.ToLower(unit), "s")) {
	case "minute":
		return time.Minute, ""
	case "hour":
		return time.Hour, ""
	case "day":
		return 24 * time.Hour, ""
	case "week":
		return 7 * 24 * time.Hour, ""
	case "month":
		return 0, "month"
	case "year":
		return 0, "year"
	case "quarter":
		return 0, "quarter"
	}
	return 24 * time.Hour, ""
}

func addDate(d DateVal, n float64, unit string) DateVal {
	dur, cal := durUnit(unit)
	shift := func(t time.Time) time.Time {
		switch cal {
		case "month":
			return t.AddDate(0, int(n), 0)
		case "year":
			return t.AddDate(int(n), 0, 0)
		case "quarter":
			return t.AddDate(0, int(n)*3, 0)
		}
		return t.Add(time.Duration(n * float64(dur)))
	}
	out := DateVal{Start: shift(d.Start), HasTime: d.HasTime}
	if d.End != nil {
		e := shift(*d.End)
		out.End = &e
	}
	return out
}

type fnImpl func(env *EvalEnv, a []Value) (Value, error)

var funcs map[string]fnImpl

func need(a []Value, n int, name string) error {
	if len(a) < n {
		return fmt.Errorf("%s() 至少需要 %d 个参数", name, n)
	}
	return nil
}

func init() {
	num1 := func(name string, f func(float64) float64) fnImpl {
		return func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, name); err != nil {
				return nil, err
			}
			return f(toNum(a[0])), nil
		}
	}
	str1 := func(name string, f func(string) string) fnImpl {
		return func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, name); err != nil {
				return nil, err
			}
			return f(ToText(a[0])), nil
		}
	}
	dateField := func(name string, f func(time.Time) float64) fnImpl {
		return func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, name); err != nil {
				return nil, err
			}
			d, ok := toDate(a[0])
			if !ok {
				return nil, nil
			}
			return f(d.Start), nil
		}
	}
	funcs = map[string]fnImpl{
		"if": nil, "ifs": nil,
		"empty": func(_ *EvalEnv, a []Value) (Value, error) {
			if len(a) == 0 {
				return true, nil
			}
			return !truthy(a[0]) && a[0] != float64(0), nil
		},
		"length": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "length"); err != nil {
				return nil, err
			}
			if l, ok := a[0].([]Value); ok {
				return float64(len(l)), nil
			}
			return float64(len([]rune(ToText(a[0])))), nil
		},
		"concat": func(_ *EvalEnv, a []Value) (Value, error) {
			allLists := len(a) > 0
			for _, x := range a {
				if _, ok := x.([]Value); !ok {
					allLists = false
				}
			}
			if allLists {
				var out []Value
				for _, x := range a {
					out = append(out, x.([]Value)...)
				}
				return out, nil
			}
			var b strings.Builder
			for _, x := range a {
				b.WriteString(ToText(x))
			}
			return b.String(), nil
		},
		"contains": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 2, "contains"); err != nil {
				return nil, err
			}
			if l, ok := a[0].([]Value); ok {
				for _, x := range l {
					if Compare(x, a[1]) == 0 {
						return true, nil
					}
				}
				return false, nil
			}
			return strings.Contains(ToText(a[0]), ToText(a[1])), nil
		},
		"test": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 2, "test"); err != nil {
				return nil, err
			}
			re, err := regexp.Compile(ToText(a[1]))
			if err != nil {
				return nil, fmt.Errorf("正则无效：%v", err)
			}
			return re.MatchString(ToText(a[0])), nil
		},
		"replace": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 3, "replace"); err != nil {
				return nil, err
			}
			re, err := regexp.Compile(ToText(a[1]))
			if err != nil {
				return nil, fmt.Errorf("正则无效：%v", err)
			}
			s := ToText(a[0])
			done := false
			return re.ReplaceAllStringFunc(s, func(m string) string {
				if done {
					return m
				}
				done = true
				return re.ReplaceAllString(m, ToText(a[2]))
			}), nil
		},
		"replaceall": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 3, "replaceAll"); err != nil {
				return nil, err
			}
			re, err := regexp.Compile(ToText(a[1]))
			if err != nil {
				return nil, fmt.Errorf("正则无效：%v", err)
			}
			return re.ReplaceAllString(ToText(a[0]), ToText(a[2])), nil
		},
		"lower": str1("lower", strings.ToLower),
		"upper": str1("upper", strings.ToUpper),
		"trim":  str1("trim", strings.TrimSpace),
		"format": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "format"); err != nil {
				return nil, err
			}
			return ToText(a[0]), nil
		},
		"tonumber": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "toNumber"); err != nil {
				return nil, err
			}
			return toNum(a[0]), nil
		},
		"slice": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 2, "slice"); err != nil {
				return nil, err
			}
			if l, ok := a[0].([]Value); ok {
				s, e := clampRange(len(l), a)
				return l[s:e], nil
			}
			r := []rune(ToText(a[0]))
			s, e := clampRange(len(r), a)
			return string(r[s:e]), nil
		},
		"split": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 2, "split"); err != nil {
				return nil, err
			}
			var out []Value
			for _, s := range strings.Split(ToText(a[0]), ToText(a[1])) {
				out = append(out, s)
			}
			return out, nil
		},
		"join": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 2, "join"); err != nil {
				return nil, err
			}
			l, _ := a[0].([]Value)
			parts := make([]string, len(l))
			for i, x := range l {
				parts[i] = ToText(x)
			}
			return strings.Join(parts, ToText(a[1])), nil
		},
		"abs":   num1("abs", math.Abs),
		"ceil":  num1("ceil", math.Ceil),
		"floor": num1("floor", math.Floor),
		"sqrt":  num1("sqrt", math.Sqrt),
		"exp":   num1("exp", math.Exp),
		"ln":    num1("ln", math.Log),
		"log10": num1("log10", math.Log10),
		"sign": num1("sign", func(x float64) float64 {
			switch {
			case x > 0:
				return 1
			case x < 0:
				return -1
			}
			return 0
		}),
		"round": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "round"); err != nil {
				return nil, err
			}
			p := 0.0
			if len(a) > 1 {
				p = toNum(a[1])
			}
			m := math.Pow(10, p)
			return math.Round(toNum(a[0])*m) / m, nil
		},
		"pow": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 2, "pow"); err != nil {
				return nil, err
			}
			return math.Pow(toNum(a[0]), toNum(a[1])), nil
		},
		"min": func(_ *EvalEnv, a []Value) (Value, error) { return aggNum(listOf(a), "min"), nil },
		"max": func(_ *EvalEnv, a []Value) (Value, error) { return aggNum(listOf(a), "max"), nil },
		"sum": func(_ *EvalEnv, a []Value) (Value, error) { return aggNum(listOf(a), "sum"), nil },
		"mean": func(_ *EvalEnv, a []Value) (Value, error) { return aggNum(listOf(a), "average"), nil },
		"average": func(_ *EvalEnv, a []Value) (Value, error) { return aggNum(listOf(a), "average"), nil },
		"median": func(_ *EvalEnv, a []Value) (Value, error) { return aggNum(listOf(a), "median"), nil },
		"now":   func(_ *EvalEnv, _ []Value) (Value, error) { return DateVal{Start: time.Now(), HasTime: true}, nil },
		"today": func(_ *EvalEnv, _ []Value) (Value, error) {
			t := time.Now()
			return DateVal{Start: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)}, nil
		},
		"dateadd": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 3, "dateAdd"); err != nil {
				return nil, err
			}
			d, ok := toDate(a[0])
			if !ok {
				return nil, nil
			}
			return addDate(d, toNum(a[1]), ToText(a[2])), nil
		},
		"datesubtract": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 3, "dateSubtract"); err != nil {
				return nil, err
			}
			d, ok := toDate(a[0])
			if !ok {
				return nil, nil
			}
			return addDate(d, -toNum(a[1]), ToText(a[2])), nil
		},
		"datebetween": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 3, "dateBetween"); err != nil {
				return nil, err
			}
			d1, ok1 := toDate(a[0])
			d2, ok2 := toDate(a[1])
			if !ok1 || !ok2 {
				return nil, nil
			}
			dur, cal := durUnit(ToText(a[2]))
			switch cal {
			case "month":
				return float64((d1.Start.Year()-d2.Start.Year())*12 + int(d1.Start.Month()-d2.Start.Month())), nil
			case "year":
				return float64(d1.Start.Year() - d2.Start.Year()), nil
			case "quarter":
				return float64(((d1.Start.Year()-d2.Start.Year())*12 + int(d1.Start.Month()-d2.Start.Month())) / 3), nil
			}
			return math.Trunc(float64(d1.Start.Sub(d2.Start)) / float64(dur)), nil
		},
		"datestart": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "dateStart"); err != nil {
				return nil, err
			}
			d, ok := toDate(a[0])
			if !ok {
				return nil, nil
			}
			return DateVal{Start: d.Start, HasTime: d.HasTime}, nil
		},
		"dateend": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "dateEnd"); err != nil {
				return nil, err
			}
			d, ok := toDate(a[0])
			if !ok {
				return nil, nil
			}
			if d.End != nil {
				return DateVal{Start: *d.End, HasTime: d.HasTime}, nil
			}
			return DateVal{Start: d.Start, HasTime: d.HasTime}, nil
		},
		"formatdate": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "formatDate"); err != nil {
				return nil, err
			}
			d, ok := toDate(a[0])
			if !ok {
				return "", nil
			}
			layout := "2006-01-02"
			if len(a) > 1 {
				layout = convertDateFormat(ToText(a[1]))
			}
			return d.Start.Format(layout), nil
		},
		"parsedate": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 1, "parseDate"); err != nil {
				return nil, err
			}
			d, ok := ParseDate(ToText(a[0]))
			if !ok {
				return nil, nil
			}
			return d, nil
		},
		"year":   dateField("year", func(t time.Time) float64 { return float64(t.Year()) }),
		"month":  dateField("month", func(t time.Time) float64 { return float64(t.Month()) }),
		"day":    dateField("day", func(t time.Time) float64 { return float64(t.Day()) }),
		"hour":   dateField("hour", func(t time.Time) float64 { return float64(t.Hour()) }),
		"minute": dateField("minute", func(t time.Time) float64 { return float64(t.Minute()) }),
		"weekday": dateField("weekday", func(t time.Time) float64 {
			w := float64(t.Weekday())
			if w == 0 {
				return 7
			}
			return w
		}),
		"week": dateField("week", func(t time.Time) float64 { _, w := t.ISOWeek(); return float64(w) }),
		"timestamp": dateField("timestamp", func(t time.Time) float64 { return float64(t.UnixMilli()) }),
		"at": func(_ *EvalEnv, a []Value) (Value, error) {
			if err := need(a, 2, "at"); err != nil {
				return nil, err
			}
			l, _ := a[0].([]Value)
			i := int(toNum(a[1]))
			if i < 0 {
				i += len(l)
			}
			if i < 0 || i >= len(l) {
				return nil, nil
			}
			return l[i], nil
		},
		"first": func(_ *EvalEnv, a []Value) (Value, error) {
			if l, ok := firstArg(a).([]Value); ok && len(l) > 0 {
				return l[0], nil
			}
			return nil, nil
		},
		"last": func(_ *EvalEnv, a []Value) (Value, error) {
			if l, ok := firstArg(a).([]Value); ok && len(l) > 0 {
				return l[len(l)-1], nil
			}
			return nil, nil
		},
		"sort": func(_ *EvalEnv, a []Value) (Value, error) {
			l, _ := firstArg(a).([]Value)
			out := append([]Value{}, l...)
			sort.SliceStable(out, func(i, j int) bool { return Compare(out[i], out[j]) < 0 })
			return out, nil
		},
		"reverse": func(_ *EvalEnv, a []Value) (Value, error) {
			l, _ := firstArg(a).([]Value)
			out := make([]Value, len(l))
			for i := range l {
				out[i] = l[len(l)-1-i]
			}
			return out, nil
		},
		"unique": func(_ *EvalEnv, a []Value) (Value, error) {
			l, _ := firstArg(a).([]Value)
			var out []Value
			for _, x := range l {
				dup := false
				for _, y := range out {
					if Compare(x, y) == 0 {
						dup = true
						break
					}
				}
				if !dup {
					out = append(out, x)
				}
			}
			return out, nil
		},
		"includes": func(env *EvalEnv, a []Value) (Value, error) { return funcs["contains"](env, a) },
		"id": func(env *EvalEnv, _ []Value) (Value, error) { return env.ID, nil },
		"tostring": func(_ *EvalEnv, a []Value) (Value, error) { return ToText(firstArg(a)), nil },
	}
}

func firstArg(a []Value) Value {
	if len(a) == 0 {
		return nil
	}
	return a[0]
}

func clampRange(n int, a []Value) (int, int) {
	s := int(toNum(a[1]))
	e := n
	if len(a) > 2 {
		e = int(toNum(a[2]))
	}
	if s < 0 {
		s += n
	}
	if e < 0 {
		e += n
	}
	if s < 0 {
		s = 0
	}
	if e > n {
		e = n
	}
	if s > e {
		s = e
	}
	return s, e
}

func aggNum(l []Value, fn string) Value {
	var nums []float64
	for _, x := range l {
		if x == nil {
			continue
		}
		if s, ok := x.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		nums = append(nums, toNum(x))
	}
	if len(nums) == 0 {
		if fn == "sum" {
			return 0.0
		}
		return nil
	}
	switch fn {
	case "sum":
		t := 0.0
		for _, n := range nums {
			t += n
		}
		return t
	case "average":
		t := 0.0
		for _, n := range nums {
			t += n
		}
		return t / float64(len(nums))
	case "min":
		m := nums[0]
		for _, n := range nums {
			m = math.Min(m, n)
		}
		return m
	case "max":
		m := nums[0]
		for _, n := range nums {
			m = math.Max(m, n)
		}
		return m
	case "median":
		sort.Float64s(nums)
		if len(nums)%2 == 1 {
			return nums[len(nums)/2]
		}
		return (nums[len(nums)/2-1] + nums[len(nums)/2]) / 2
	case "range":
		mn, mx := nums[0], nums[0]
		for _, n := range nums {
			mn, mx = math.Min(mn, n), math.Max(mx, n)
		}
		return mx - mn
	}
	return nil
}

// convertDateFormat 把常见的日期格式写法（YYYY-MM-DD）转成 Go 的格式。
func convertDateFormat(f string) string {
	r := strings.NewReplacer("YYYY", "2006", "YY", "06", "MMMM", "January", "MMM", "Jan", "MM", "01", "M", "1", "DD", "02", "D", "2",
		"HH", "15", "hh", "03", "mm", "04", "ss", "05", "A", "PM", "dddd", "Monday", "ddd", "Mon")
	return r.Replace(f)
}
