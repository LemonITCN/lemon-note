// Package dbs 实现 Notion 式数据库：属性结构、行（即页面）属性值、公式、关联与汇总、视图、筛选排序分组查询。
package dbs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
)

// 属性类型
const (
	TTitle          = "title"
	TText           = "text"
	TNumber         = "number"
	TSelect         = "select"
	TMultiSelect    = "multi_select"
	TStatus         = "status"
	TDate           = "date"
	TPerson         = "person"
	TFiles          = "files"
	TCheckbox       = "checkbox"
	TURL            = "url"
	TEmail          = "email"
	TPhone          = "phone"
	TFormula        = "formula"
	TRelation       = "relation"
	TRollup         = "rollup"
	TCreatedTime    = "created_time"
	TCreatedBy      = "created_by"
	TLastEditedTime = "last_edited_time"
	TLastEditedBy   = "last_edited_by"
	TUniqueID       = "unique_id"
)

var validTypes = map[string]bool{TTitle: true, TText: true, TNumber: true, TSelect: true, TMultiSelect: true, TStatus: true, TDate: true, TPerson: true,
	TFiles: true, TCheckbox: true, TURL: true, TEmail: true, TPhone: true, TFormula: true, TRelation: true, TRollup: true, TCreatedTime: true,
	TCreatedBy: true, TLastEditedTime: true, TLastEditedBy: true, TUniqueID: true}

// Computed 判断属性值是否由系统计算（不能直接写）。
func Computed(t string) bool {
	switch t {
	case TFormula, TRollup, TCreatedTime, TCreatedBy, TLastEditedTime, TLastEditedBy, TUniqueID:
		return true
	}
	return false
}

// Option 是选项。
type Option struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
	Group string `json:"group,omitempty"` // 状态属性：todo | doing | done
}

// Property 是一个属性定义。
type Property struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Options     []Option `json:"options,omitempty"`
	Format      string   `json:"format,omitempty"`      // 数字格式：number | comma | percent | yuan | dollar | euro
	Expression  string   `json:"expression,omitempty"`  // 公式
	DatabaseID  string   `json:"database_id,omitempty"` // 关联到的数据库
	TwoWay      bool     `json:"two_way,omitempty"`
	RelatedProp string   `json:"related_prop,omitempty"` // 双向关联在对方数据库里的属性 id
	Relation    string   `json:"relation,omitempty"`     // 汇总：通过哪个关联属性
	Target      string   `json:"target,omitempty"`       // 汇总：对方数据库的哪个属性
	Function    string   `json:"function,omitempty"`     // 汇总函数
	Prefix      string   `json:"prefix,omitempty"`       // 编号前缀
	Description string   `json:"description,omitempty"`
}

// Schema 是数据库结构。
type Schema struct {
	Properties []Property `json:"properties"`
}

// Prop 按 id 找属性。
func (s *Schema) Prop(id string) *Property {
	for i := range s.Properties {
		if s.Properties[i].ID == id {
			return &s.Properties[i]
		}
	}
	return nil
}

// Find 按 id 或名称（不区分大小写）找属性。
func (s *Schema) Find(key string) *Property {
	if p := s.Prop(key); p != nil {
		return p
	}
	k := strings.ToLower(strings.TrimSpace(key))
	for i := range s.Properties {
		if strings.ToLower(s.Properties[i].Name) == k {
			return &s.Properties[i]
		}
	}
	return nil
}

// Title 返回标题属性。
func (s *Schema) Title() *Property {
	for i := range s.Properties {
		if s.Properties[i].Type == TTitle {
			return &s.Properties[i]
		}
	}
	return nil
}

// ParseSchema 解析结构 JSON。
func ParseSchema(raw string) *Schema {
	var s Schema
	_ = json.Unmarshal([]byte(raw), &s)
	if s.Title() == nil {
		s.Properties = append([]Property{{ID: "title", Name: "名称", Type: TTitle}}, s.Properties...)
	}
	return &s
}

// DefaultSchema 新建数据库的默认结构。
func DefaultSchema(zh bool) *Schema {
	name, tags := "名称", "标签"
	if !zh {
		name, tags = "Name", "Tags"
	}
	return &Schema{Properties: []Property{{ID: "title", Name: name, Type: TTitle}, {ID: newPropID(), Name: tags, Type: TMultiSelect, Options: []Option{}}}}
}

func newPropID() string { return "p" + ids.RandomToken(6) }

// NewOptionID 生成选项 id。
func NewOptionID() string { return "o" + ids.RandomToken(6) }

var colors = map[string]bool{"default": true, "gray": true, "brown": true, "orange": true, "yellow": true, "green": true, "blue": true, "purple": true, "pink": true, "red": true}

var optColorCycle = []string{"gray", "blue", "green", "yellow", "orange", "purple", "pink", "red", "brown"}

// DefaultStatusOptions 状态属性的默认选项。
func DefaultStatusOptions(zh bool) []Option {
	if zh {
		return []Option{{ID: NewOptionID(), Name: "未开始", Color: "gray", Group: "todo"}, {ID: NewOptionID(), Name: "进行中", Color: "blue", Group: "doing"}, {ID: NewOptionID(), Name: "已完成", Color: "green", Group: "done"}}
	}
	return []Option{{ID: NewOptionID(), Name: "Not started", Color: "gray", Group: "todo"}, {ID: NewOptionID(), Name: "In progress", Color: "blue", Group: "doing"}, {ID: NewOptionID(), Name: "Done", Color: "green", Group: "done"}}
}

// Validate 校验并规整结构（补 id、去重名、检查类型）。
func (s *Schema) Validate() error {
	seenID := map[string]bool{}
	seenName := map[string]bool{}
	titles := 0
	if len(s.Properties) > 200 {
		return errors.New("属性最多 200 个")
	}
	for i := range s.Properties {
		p := &s.Properties[i]
		if p.ID == "" {
			p.ID = newPropID()
		}
		if p.Type == TTitle {
			titles++
			p.ID = "title"
		}
		if seenID[p.ID] {
			return fmt.Errorf("属性 id 重复：%s", p.ID)
		}
		seenID[p.ID] = true
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			return errors.New("属性名称不能为空")
		}
		if utf8.RuneCountInString(p.Name) > 100 {
			return fmt.Errorf("属性名称太长：%s", p.Name)
		}
		ln := strings.ToLower(p.Name)
		if seenName[ln] {
			return fmt.Errorf("属性名称重复：%s", p.Name)
		}
		seenName[ln] = true
		if !validTypes[p.Type] {
			return fmt.Errorf("不支持的属性类型：%s", p.Type)
		}
		switch p.Type {
		case TSelect, TMultiSelect, TStatus:
			seenOpt := map[string]bool{}
			for j := range p.Options {
				o := &p.Options[j]
				o.Name = strings.TrimSpace(o.Name)
				if o.ID == "" {
					o.ID = NewOptionID()
				}
				if o.Name == "" || seenOpt[strings.ToLower(o.Name)] {
					return fmt.Errorf("属性「%s」的选项为空或重复", p.Name)
				}
				seenOpt[strings.ToLower(o.Name)] = true
				if !colors[o.Color] {
					o.Color = optColorCycle[j%len(optColorCycle)]
				}
				if p.Type == TStatus && o.Group != "todo" && o.Group != "doing" && o.Group != "done" {
					o.Group = "todo"
				}
			}
		case TFormula:
			if _, err := ParseFormula(p.Expression); err != nil {
				return fmt.Errorf("公式「%s」有误：%v", p.Name, err)
			}
		case TRollup:
			if p.Relation == "" || p.Target == "" {
				return fmt.Errorf("汇总属性「%s」需要指定关联属性与目标属性", p.Name)
			}
			if p.Function == "" {
				p.Function = "show_original"
			}
		case TRelation:
			if p.DatabaseID == "" {
				return fmt.Errorf("关联属性「%s」需要指定关联的数据库", p.Name)
			}
		}
	}
	if titles != 1 {
		return errors.New("必须有且只有一个标题属性")
	}
	return nil
}

// OptionByName 按名称找选项（不区分大小写），没有则返回 nil。
func (p *Property) OptionByName(name string) *Option {
	n := strings.ToLower(strings.TrimSpace(name))
	for i := range p.Options {
		if strings.ToLower(p.Options[i].Name) == n || p.Options[i].ID == name {
			return &p.Options[i]
		}
	}
	return nil
}

// Option 按 id 找选项。
func (p *Property) Option(id string) *Option {
	for i := range p.Options {
		if p.Options[i].ID == id {
			return &p.Options[i]
		}
	}
	return nil
}
