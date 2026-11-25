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

// FromRow 把 Row 填进带 db 标签的结构体（dst 必须是指针）。
func FromRow(row Row, dst any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("store: FromRow 需要结构体指针，得到 %T", dst)
	}
	rv = rv.Elem()
	for _, f := range structFields(rv.Type()) {
		v, ok := row[f.col]
		if !ok {
			continue
		}
		if err := assign(rv.FieldByIndex(f.index), v); err != nil {
			return fmt.Errorf("store: 列 %s: %w", f.col, err)
		}
	}
	return nil
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil {
			return int64(f), true
		}
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func assign(fv reflect.Value, v any) error {
	if v == nil {
		fv.Set(reflect.Zero(fv.Type()))
		return nil
	}
	if fv.Kind() == reflect.Pointer {
		nv := reflect.New(fv.Type().Elem())
		if err := assign(nv.Elem(), v); err != nil {
			return err
		}
		fv.Set(nv)
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		switch x := v.(type) {
		case string:
			fv.SetString(x)
		case []byte:
			fv.SetString(string(x))
		default:
			fv.SetString(fmt.Sprint(x))
		}
	case reflect.Int, reflect.Int64, reflect.Int32:
		n, ok := toInt64(v)
		if !ok {
			return fmt.Errorf("无法转换 %T 为整数", v)
		}
		fv.SetInt(n)
	case reflect.Bool:
		n, ok := toInt64(v)
		if !ok {
			return fmt.Errorf("无法转换 %T 为布尔", v)
		}
		fv.SetBool(n != 0)
	case reflect.Float64, reflect.Float32:
		switch x := v.(type) {
		case float64:
			fv.SetFloat(x)
		case json.Number:
			f, _ := x.Float64()
			fv.SetFloat(f)
		default:
			n, _ := toInt64(v)
			fv.SetFloat(float64(n))
		}
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.Uint8 {
			switch x := v.(type) {
			case []byte:
				fv.SetBytes(x)
			case string:
				b, err := base64.StdEncoding.DecodeString(x)
				if err != nil {
					return err
				}
				fv.SetBytes(b)
			}
			return nil
		}
		return fmt.Errorf("不支持的切片类型 %s", fv.Type())
	default:
		return fmt.Errorf("不支持的字段类型 %s", fv.Type())
	}
	return nil
}
