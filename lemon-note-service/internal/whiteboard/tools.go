package whiteboard

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/fracidx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/tools"
)

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}
func sstr(d string) map[string]any { return map[string]any{"type": "string", "description": d} }

func bind(args json.RawMessage, dst any) error {
	if len(args) == 0 {
		args = []byte("{}")
	}
	if err := json.Unmarshal(args, dst); err != nil {
		return fmt.Errorf("参数格式不对：%v", err)
	}
	return nil
}

type boardBlock struct {
	block model.Block
	node  map[string]any
	scene *Scene
	title string
}

// boards 找出页面里的白板块（只看顶层块）。
func boards(env *tools.Env, pageID string) ([]*boardBlock, []model.Block, error) {
	bs, err := env.Svc.Pages.Blocks(env.Ctx, pageID)
	if err != nil {
		return nil, nil, err
	}
	var out []*boardBlock
	for _, b := range bs {
		if b.Type != "whiteboard" {
			continue
		}
		var n map[string]any
		if json.Unmarshal([]byte(b.Data), &n) != nil {
			continue
		}
		attrs, _ := n["attrs"].(map[string]any)
		sc := &Scene{}
		if attrs != nil {
			if raw, err := json.Marshal(attrs["data"]); err == nil && string(raw) != "null" {
				_ = json.Unmarshal(raw, sc)
			}
		}
		out = append(out, &boardBlock{block: b, node: n, scene: sc, title: str(attrs, "title")})
	}
	return out, bs, nil
}

func pick(list []*boardBlock, blockID string) (*boardBlock, error) {
	if blockID != "" {
		for _, b := range list {
			if b.block.ID == blockID {
				return b, nil
			}
		}
		return nil, fmt.Errorf("页面里没有 id 为 %s 的白板", blockID)
	}
	switch len(list) {
	case 0:
		return nil, fmt.Errorf("页面里没有白板；要新建请传 create=true")
	case 1:
		return list[0], nil
	}
	var ids []string
	for _, b := range list {
		ids = append(ids, fmt.Sprintf("%s（%s）", b.block.ID, b.title))
	}
	return nil, fmt.Errorf("页面里有多个白板，请用 block_id 指定：%s", strings.Join(ids, "、"))
}

func init() {
	tools.Default.Register(&tools.Tool{
		Name:  "whiteboard_read",
		Title: "读取白板",
		Description: "读取页面里白板块的内容：每个图形的 id、类型、位置尺寸、文字，独立文字，以及连线的起止图形与标签。" +
			"页面只有一个白板时可以不传 block_id；有多个时先不传，会列出全部白板的 id。",
		Schema:   obj(map[string]any{"page_id": sstr("页面 id"), "block_id": sstr("白板块 id（可选）")}, "page_id"),
		ReadOnly: true,
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			var in struct {
				PageID  string `json:"page_id"`
				BlockID string `json:"block_id"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.PageID, model.RoleViewer)
			if err != nil {
				return nil, err
			}
			list, _, err := boards(env, meta.ID)
			if err != nil {
				return nil, err
			}
			b, err := pick(list, in.BlockID)
			if err != nil {
				return &tools.Result{Text: err.Error(), IsError: true}, nil
			}
			head := fmt.Sprintf("白板 %s", b.block.ID)
			if b.title != "" {
				head += "「" + b.title + "」"
			}
			return &tools.Result{Text: head + "\n" + Summary(b.scene)}, nil
		},
	})
	tools.Default.Register(&tools.Tool{
		Name:  "whiteboard_edit",
		Title: "编辑白板",
		Description: "编辑页面里的白板（Excalidraw）。按顺序执行：clear（清空）→ delete（按 id 删除，图形里的文字一起删）→ update（按 id 改文字、位置、尺寸、颜色）→ add（新增）→ mermaid（按流程图自动布局生成）。\n" +
			"add 每项：{key?, type: rectangle|ellipse|diamond|text|arrow|line, text?, x?, y?, w?, h?, stroke?, fill?, dashed?, from?, to?}；" +
			"不给坐标的图形自动排在现有内容右侧；连线用 from / to 指向图形 id 或本次新增项的 key，连线的 text 是线上的标签。颜色用 #RRGGBB。\n" +
			"mermaid：一段 Mermaid 流程图（flowchart LR / TD），节点形状 [矩形] (圆角/椭圆) {菱形}，连线 --> --- -.-> ==>，标签 -->|文字|，生成后与白板里原有元素并存。\n" +
			"页面没有白板时传 create=true（可带 title）在页面末尾新建一个。改之前先 whiteboard_read 看清现有元素的 id。",
		Schema: obj(map[string]any{
			"page_id":  sstr("页面 id"),
			"block_id": sstr("白板块 id（页面只有一个白板时可省略）"),
			"create":   map[string]any{"type": "boolean", "description": "在页面末尾新建白板"},
			"title":    sstr("新建白板的标题"),
			"clear":    map[string]any{"type": "boolean", "description": "先清空白板"},
			"delete":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要删除的元素 id"},
			"update":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "修改项：{id, text?, x?, y?, w?, h?, stroke?, fill?}"},
			"add":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "新增项，见工具说明"},
			"mermaid":  sstr("Mermaid 流程图源码（可选）"),
		}, "page_id"),
		Handler: func(env *tools.Env, args json.RawMessage) (*tools.Result, error) {
			var in struct {
				PageID  string `json:"page_id"`
				BlockID string `json:"block_id"`
				Create  bool   `json:"create"`
				Title   string `json:"title"`
				EditInput
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			meta, err := env.NeedPage(in.PageID, model.RoleEditor)
			if err != nil {
				return nil, err
			}
			list, all, err := boards(env, meta.ID)
			if err != nil {
				return nil, err
			}
			var b *boardBlock
			if in.Create {
				id := ids.New()
				last := ""
				for _, x := range all {
					if x.Position > last {
						last = x.Position
					}
				}
				pos, err := fracidx.Between(last, "")
				if err != nil {
					return nil, err
				}
				b = &boardBlock{block: model.Block{ID: id, Type: "whiteboard", Position: pos},
					node: map[string]any{"type": "whiteboard", "attrs": map[string]any{"id": id, "title": in.Title}}, scene: &Scene{AppState: map[string]any{"viewBackgroundColor": "#ffffff"}, Files: map[string]any{}}, title: in.Title}
			} else if b, err = pick(list, in.BlockID); err != nil {
				return &tools.Result{Text: err.Error(), IsError: true}, nil
			}
			res, err := Apply(b.scene, in.EditInput)
			if err != nil {
				return &tools.Result{Text: "白板没有改动：" + err.Error(), IsError: true}, nil
			}
			if len(b.scene.Elements) > 5000 {
				return &tools.Result{Text: "白板元素太多（上限 5000）", IsError: true}, nil
			}
			attrs, _ := b.node["attrs"].(map[string]any)
			if attrs == nil {
				attrs = map[string]any{"id": b.block.ID}
				b.node["attrs"] = attrs
			}
			if b.scene.Elements == nil {
				b.scene.Elements = []map[string]any{}
			}
			attrs["data"] = b.scene
			if in.Title != "" {
				attrs["title"] = in.Title
			}
			data, err := json.Marshal(b.node)
			if err != nil {
				return nil, err
			}
			if err := env.BeforeWriteSafe(meta.ID); err != nil {
				return nil, err
			}
			ar, err := env.Svc.Pages.ApplyOps(env.Ctx, env.User.ID, env.Origin, meta.ID, []pages.BlockOp{{Op: "put", ID: b.block.ID, Type: "whiteboard",
				Data: data, Position: b.block.Position, BaseVersion: b.block.Version}})
			if err != nil {
				return nil, err
			}
			for _, r := range ar.Results {
				if r.Conflict {
					return &tools.Result{Text: "白板刚被别人修改过，请先 whiteboard_read 再改。", IsError: true}, nil
				}
			}
			if env.Reads != nil { // 之前读过页面的话，自己这次改动不算「读后被改」
				if _, ok := env.Reads.Get(meta.ID); ok {
					env.Reads.Mark(meta.ID, ar.Rev)
				}
			}
			msg := fmt.Sprintf("白板 %s 已更新：新增 %d、修改 %d、删除 %d 个元素。", b.block.ID, res.Created, res.Updated, res.Deleted)
			if len(res.Added) > 0 {
				var kv []string
				for k, v := range res.Added {
					if !strings.HasPrefix(k, "mm:") {
						kv = append(kv, k+"="+v)
					}
				}
				if len(kv) > 0 {
					msg += " 新元素 id：" + strings.Join(kv, "，")
				}
			}
			return &tools.Result{Text: msg, Data: map[string]any{"block_id": b.block.ID}}, nil
		},
	})
}
