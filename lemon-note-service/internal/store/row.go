package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Row 是一行数据（列名 → 值）。值只会是 nil、string、int64、float64、[]byte。
type Row map[string]any

type fieldInfo struct {
	index []int
	col   string
}

var structCache sync.Map // reflect.Type → []fieldInfo

func structFields(t reflect.Type) []fieldInfo {
	if v, ok := structCache.Load(t); ok {
		return v.([]fieldInfo)
	}
	var out []fieldInfo
	var walk func(t reflect.Type, prefix []int)
	walk = func(t reflect.Type, prefix []int) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type, append(append([]int{}, prefix...), i))
				continue
			}
			tag := f.Tag.Get("db")
			if tag == "" || tag == "-" || !f.IsExported() {
				continue
			}
			out = append(out, fieldInfo{index: append(append([]int{}, prefix...), i), col: strings.Split(tag, ",")[0]})
		}
	}
	walk(t, nil)
	structCache.Store(t, out)
	return out
}

// ToRow 把带 db 标签的结构体转成 Row。
func ToRow(v any) (Row, error) {
	if r, ok := v.(Row); ok {
		return r, nil
	}
	if m, ok := v.(map[string]any); ok {
		return Row(m), nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("store: 不支持的行类型 %T", v)
	}
	row := Row{}
	for _, f := range structFields(rv.Type()) {
		row[f.col] = rv.FieldByIndex(f.index).Interface()
	}
	return row, nil
}

// normalize 按列类型把值规整成可写入数据库、可进日志的形式。
func normalize(c Column, v any) (any, error) {
	if v == nil {
		if c.Nullable {
			return nil, nil
		}
		return zeroFor(c), nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			if c.Nullable {
				return nil, nil
			}
			return zeroFor(c), nil
		}
		v = rv.Elem().Interface()
	}
	switch c.Type {
	case TInt, TBool:
		switch x := v.(type) {
		case int64:
			return x, nil
		case int:
			return int64(x), nil
		case int32:
			return int64(x), nil
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		case float64:
			return int64(x), nil
		case json.Number:
			n, err := x.Int64()
			if err != nil {
				f, ferr := x.Float64()
				if ferr != nil {
					return nil, err
				}
				return int64(f), nil
			}
			return n, nil
		}
	case TReal:
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		case int:
			return float64(x), nil
		case json.Number:
			return x.Float64()
		}
	case TBlob:
		switch x := v.(type) {
		case []byte:
			return x, nil
		case string: // 来自日志的 base64
			return base64.StdEncoding.DecodeString(x)
		}
	default:
		switch x := v.(type) {
		case string:
			return x, nil
		case []byte:
			return string(x), nil
		case json.RawMessage:
			return string(x), nil
		case fmt.Stringer:
			return x.String(), nil
		}
	}
	return nil, fmt.Errorf("store: 列 %s 不接受 %T", c.Name, v)
}

func zeroFor(c Column) any {
	switch c.Type {
	case TInt, TBool:
		return int64(0)
	case TReal:
		return float64(0)
	case TBlob:
		return []byte{}
	default:
		return ""
	}
}

// normalizeRow 按表定义规整一整行，忽略表里没有的列；strict 为真时要求行包含全部列。
func normalizeRow(t *Table, row Row, strict bool) ([]string, []any, Row, error) {
	cols := make([]string, 0, len(t.Columns))
	vals := make([]any, 0, len(t.Columns))
	clean := Row{}
	for _, c := range t.Columns {
		v, present := row[c.Name]
		if !present && strict {
			return nil, nil, nil, fmt.Errorf("store: 写入 %s 缺少列 %s", t.Name, c.Name)
		}
		nv, err := normalize(c, v)
		if err != nil {
			return nil, nil, nil, err
		}
		cols = append(cols, c.Name)
		vals = append(vals, nv)
		clean[c.Name] = nv
	}
	if id, _ := clean["id"].(string); id == "" {
		return nil, nil, nil, fmt.Errorf("store: 写入 %s 缺少 id", t.Name)
	}
	return cols, vals, clean, nil
}
