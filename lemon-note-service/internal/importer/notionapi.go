package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/netsafe"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Notion API 导入：用集成密钥读页面、块（递归子块）、数据库（属性类型、选项、关联、汇总）。
// 页面树边读边建：建一个页面前才去取它的块，遇到子页面 / 子数据库就挂到计划树上，下一层再建。
// Notion 托管的文件地址一小时后过期，所以图片、附件在建页前当场下载。

const notionVersion = "2022-06-28"

// ---------- 客户端 ----------

type notionClient struct {
	base     string
	token    string
	interval time.Duration
	hc       *http.Client
	last     time.Time
}

type notionErr struct {
	status int
	code   string
	msg    string
}

func (e *notionErr) Error() string { return fmt.Sprintf("Notion 返回 %d %s", e.status, e.code) }

var errNotionAuth = &abortError{msg: "Notion 集成密钥无效或已失效，请检查后重试"}

func (c *notionClient) wait(ctx context.Context) error {
	if d := c.interval - time.Since(c.last); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	c.last = time.Now()
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// call 发请求：按 Retry-After 处理 429，5xx 与网络错误退避重试；密钥失效直接终止整个导入。
func (c *notionClient) call(ctx context.Context, method, p string, body any, out any) error {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	tries := 0
	for {
		tries++
		if err := c.wait(ctx); err != nil {
			return err
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+p, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Notion-Version", notionVersion)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if tries < 4 {
				if err := sleepCtx(ctx, time.Duration(tries)*2*time.Second); err != nil {
					return err
				}
				continue
			}
			if errors.Is(err, netsafe.ErrBlocked) {
				return &abortError{msg: "Notion 地址被安全策略拦截"}
			}
			return &abortError{msg: "无法连接 Notion，请检查服务器网络"}
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			if tries > 10 {
				return &abortError{msg: "Notion 一直限流，导入已停止，请稍后重试"}
			}
			wait := time.Second
			if s, err := strconv.ParseFloat(strings.TrimSpace(resp.Header.Get("Retry-After")), 64); err == nil && s > 0 {
				wait = time.Duration(math.Min(s, 120) * float64(time.Second))
			}
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			continue
		case resp.StatusCode >= 500:
			if tries < 4 {
				if err := sleepCtx(ctx, time.Duration(tries)*2*time.Second); err != nil {
					return err
				}
				continue
			}
			return &notionErr{status: resp.StatusCode, code: "server_error"}
		case resp.StatusCode == http.StatusUnauthorized:
			return errNotionAuth
		case resp.StatusCode >= 400:
			var e struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(data, &e)
			return &notionErr{status: resp.StatusCode, code: e.Code, msg: e.Message}
		}
		if rerr != nil {
			return rerr
		}
		if out == nil {
			return nil
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(out); err != nil {
			return fmt.Errorf("Notion 响应无法解析: %w", err)
		}
		return nil
	}
}

func isNotFound(err error) bool {
	var ne *notionErr
	return errors.As(err, &ne) && (ne.status == 404 || ne.status == 403)
}

// ---------- 数据结构 ----------

type nParent struct {
	Type       string `json:"type"`
	PageID     string `json:"page_id"`
	DatabaseID string `json:"database_id"`
	BlockID    string `json:"block_id"`
}

type nFile struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	External *struct {
		URL string `json:"url"`
	} `json:"external"`
	File *struct {
		URL string `json:"url"`
	} `json:"file"`
	Emoji   string     `json:"emoji"`
	Caption []richText `json:"caption"`
}

func (f *nFile) url() string {
	switch {
	case f == nil:
		return ""
	case f.File != nil:
		return f.File.URL
	case f.External != nil:
		return f.External.URL
	}
	return ""
}

type nObject struct {
	Object         string                     `json:"object"`
	ID             string                     `json:"id"`
	Parent         nParent                    `json:"parent"`
	Icon           *nFile                     `json:"icon"`
	Cover          *nFile                     `json:"cover"`
	Archived       bool                       `json:"archived"`
	InTrash        bool                       `json:"in_trash"`
	CreatedTime    string                     `json:"created_time"`
	LastEditedTime string                     `json:"last_edited_time"`
	Properties     map[string]json.RawMessage `json:"properties"`
	Title          []richText                 `json:"title"`
	IsInline       bool                       `json:"is_inline"`
	URL            string                     `json:"url"`
	propOrder      []string
}

type nList struct {
	Results    []json.RawMessage `json:"results"`
	HasMore    bool              `json:"has_more"`
	NextCursor *string           `json:"next_cursor"`
}

type richText struct {
	Type      string  `json:"type"`
	PlainText string  `json:"plain_text"`
	Href      *string `json:"href"`
	Text      *struct {
		Content string `json:"content"`
		Link    *struct {
			URL string `json:"url"`
		} `json:"link"`
	} `json:"text"`
	Mention *struct {
		Type string `json:"type"`
		Page *struct {
			ID string `json:"id"`
		} `json:"page"`
		Database *struct {
			ID string `json:"id"`
		} `json:"database"`
		User *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
		Date *nDate `json:"date"`
	} `json:"mention"`
	Equation *struct {
		Expression string `json:"expression"`
	} `json:"equation"`
	Annotations struct {
		Bold          bool   `json:"bold"`
		Italic        bool   `json:"italic"`
		Strikethrough bool   `json:"strikethrough"`
		Underline     bool   `json:"underline"`
		Code          bool   `json:"code"`
		Color         string `json:"color"`
	} `json:"annotations"`
}

type nDate struct {
	Start string  `json:"start"`
	End   *string `json:"end"`
}

func plain(rt []richText) string {
	var b strings.Builder
	for _, r := range rt {
		b.WriteString(r.PlainText)
	}
	return b.String()
}

// nBlock 是一个块；Data 是块类型同名字段的内容。
type nBlock struct {
	ID          string
	Type        string
	HasChildren bool
	Data        json.RawMessage
	Children    []*nBlock
}

func normID(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "")) }

// ---------- 导入过程 ----------

type napi struct {
	c       *notionClient
	b       *builder
	ids     map[string]string // Notion id → 我们的 id
	planned map[string]*pnode // 已进计划树的页面 / 数据库
	titles  map[string]string // Notion id → 标题（悬空提及降级成文字时用）
	propIDs map[string]string // 数据库 id + 属性 id → 我们的属性 id
	refs    map[string]map[string]string
	dbInfo  map[string]*dbConv
	seenDeg map[string]bool
}

func (s *Service) notionAPIWork(token, root string) work {
	return func(ctx context.Context, b *builder) error {
		base := strings.TrimRight(s.NotionBaseURL, "/")
		u, err := url.Parse(base)
		if err != nil || netsafe.CheckURL(u) != nil {
			return &abortError{msg: "Notion 地址被安全策略拦截"}
		}
		n := &napi{c: &notionClient{base: base, token: token, interval: s.NotionInterval, hc: netsafe.Client(2 * time.Minute)}, b: b,
			ids: map[string]string{}, planned: map[string]*pnode{}, titles: map[string]string{}, propIDs: map[string]string{},
			refs: map[string]map[string]string{}, dbInfo: map[string]*dbConv{}, seenDeg: map[string]bool{}}
		roots, leftovers, err := n.discover(ctx, root)
		if err != nil {
			return err
		}
		if err := b.build(roots); err != nil {
			return err
		}
		// 搜索能看到、但遍历时没有遇到的页面（例如在同步块里）：作为顶层补导入
		var extra []*pnode
		for _, o := range leftovers {
			id := normID(o.ID)
			if _, ok := n.planned[id]; ok {
				continue
			}
			if pn := n.objectNode(o); pn != nil {
				extra = append(extra, pn)
			}
		}
		if len(extra) > 0 {
			if err := b.build(extra); err != nil {
				return err
			}
		}
		return n.fixDangling(ctx)
	}
}

// idFor 返回 Notion id 对应的我们的 id（第一次用到时分配）。
func (n *napi) idFor(nid string) string {
	k := normID(nid)
	if id, ok := n.ids[k]; ok {
		return id
	}
	id := ids.New()
	n.ids[k] = id
	return id
}

func (n *napi) degrade(code, detail, msg string) {
	k := code + "\x00" + detail
	if n.seenDeg[k] {
		return
	}
	n.seenDeg[k] = true
	n.b.warn(code, "", detail, msg)
}

// discover 找出要导入的顶层页面：指定了根页面就从它开始；否则搜索密钥能看到的全部页面与数据库，
// 上级不在可见范围内的就是顶层。
func (n *napi) discover(ctx context.Context, root string) ([]*pnode, []nObject, error) {
	if root != "" {
		var o nObject
		err := n.c.call(ctx, "GET", "/v1/pages/"+root, nil, &o)
		if isNotFound(err) {
			err = n.c.call(ctx, "GET", "/v1/databases/"+root, nil, &o)
		}
		if err != nil {
			if isNotFound(err) {
				return nil, nil, fail("找不到这个页面：请确认页面 id 正确，并且已在 Notion 里把页面连接到这个集成")
			}
			return nil, nil, n.wrap(err)
		}
		pn := n.objectNode(o)
		if pn == nil {
			return nil, nil, fail("这个页面在 Notion 回收站里")
		}
		return []*pnode{pn}, nil, nil
	}
	var all []nObject
	var cursor *string
	for {
		body := map[string]any{"page_size": 100}
		if cursor != nil {
			body["start_cursor"] = *cursor
		}
		var l nList
		if err := n.c.call(ctx, "POST", "/v1/search", body, &l); err != nil {
			return nil, nil, n.wrap(err)
		}
		for _, raw := range l.Results {
			var o nObject
			if err := json.Unmarshal(raw, &o); err == nil && !o.Archived && !o.InTrash {
				all = append(all, o)
			}
		}
		n.b.s.touch(ctx, n.b.j, func(p *Progress) { p.Current = fmt.Sprintf("%d", len(all)) })
		if !l.HasMore || l.NextCursor == nil {
			break
		}
		cursor = l.NextCursor
	}
	if len(all) == 0 {
		return nil, nil, fail("这个集成密钥看不到任何页面：请在 Notion 里把要导入的页面连接到这个集成")
	}
	visible := map[string]bool{}
	for _, o := range all {
		visible[normID(o.ID)] = true
	}
	blockParent := map[string]string{}
	var roots []*pnode
	var rest []nObject
	for _, o := range all {
		par := ""
		switch o.Parent.Type {
		case "page_id":
			par = o.Parent.PageID
		case "database_id":
			par = o.Parent.DatabaseID
		case "block_id":
			par = n.pageOfBlock(ctx, o.Parent.BlockID, blockParent)
		}
		if par != "" && visible[normID(par)] {
			if o.Parent.Type != "database_id" {
				rest = append(rest, o)
			}
			continue
		}
		if pn := n.objectNode(o); pn != nil {
			roots = append(roots, pn)
		}
	}
	return roots, rest, nil
}

// pageOfBlock 顺着块的上级找到所在页面。
func (n *napi) pageOfBlock(ctx context.Context, blockID string, cache map[string]string) string {
	cur := blockID
	for hop := 0; hop < 8 && cur != ""; hop++ {
		if v, ok := cache[cur]; ok {
			return v
		}
		var b struct {
			Parent nParent `json:"parent"`
		}
		if err := n.c.call(ctx, "GET", "/v1/blocks/"+cur, nil, &b); err != nil {
			cache[blockID] = ""
			return ""
		}
		switch b.Parent.Type {
		case "page_id":
			cache[blockID] = b.Parent.PageID
			return b.Parent.PageID
		case "database_id":
			cache[blockID] = b.Parent.DatabaseID
			return b.Parent.DatabaseID
		case "block_id":
			cur = b.Parent.BlockID
		default:
			cache[blockID] = ""
			return ""
		}
	}
	return ""
}

func (n *napi) wrap(err error) error {
	var ne *notionErr
	if errors.As(err, &ne) {
		if ne.msg != "" {
			return fail("Notion 返回错误（%d）：%s", ne.status, ne.msg)
		}
		return fail("Notion 返回错误（%d）", ne.status)
	}
	return err
}

// objectNode 把搜索 / 读取到的页面或数据库变成计划节点。
func (n *napi) objectNode(o nObject) *pnode {
	if o.Archived || o.InTrash {
		return nil
	}
	nid := normID(o.ID)
	if pn := n.planned[nid]; pn != nil {
		return nil
	}
	var pn *pnode
	if o.Object == "database" {
		pn = &pnode{id: n.idFor(nid), kind: nDB, title: plain(o.Title), src: o.URL}
		pn.prepare = n.prepareDB(nid)
	} else {
		pn = &pnode{id: n.idFor(nid), kind: nPage, title: pageTitle(o), src: o.URL}
		pn.prepare = n.preparePage(nid, &o)
	}
	n.titles[nid] = pn.title
	n.planned[nid] = pn
	return pn
}

func pageTitle(o nObject) string {
	for _, raw := range o.Properties {
		var p struct {
			Type  string     `json:"type"`
			Title []richText `json:"title"`
		}
		if json.Unmarshal(raw, &p) == nil && p.Type == "title" {
			return plain(p.Title)
		}
	}
	return ""
}

// iconCover 处理图标与封面：表情直接用；Notion 托管的图片当场下载成附件（地址会过期）；外链保留。
func (n *napi) iconCover(b *builder, p *pnode, o *nObject) error {
	if o.Icon != nil {
		switch o.Icon.Type {
		case "emoji":
			p.icon = o.Icon.Emoji
		case "external":
			p.icon = o.Icon.url()
		case "file":
			u := o.Icon.url()
			m, err := b.uploadURL(u, "icon"+path.Ext(urlPath(u)), p.id, p.src)
			if err != nil {
				return err
			}
			if m != nil {
				p.icon = "file:" + m.ID
			}
		}
	}
	if o.Cover != nil {
		switch o.Cover.Type {
		case "external":
			p.cover = o.Cover.url()
		case "file":
			u := o.Cover.url()
			m, err := b.uploadURL(u, "cover"+path.Ext(urlPath(u)), p.id, p.src)
			if err != nil {
				return err
			}
			if m != nil {
				p.cover = "lemon://file/" + m.ID
			}
		}
	}
	return nil
}

func hasHostedFile(o *nObject) bool {
	return o.Icon != nil && o.Icon.Type == "file" || o.Cover != nil && o.Cover.Type == "file"
}

func urlPath(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	return ""
}

// preparePage 读页面（图标、封面、标题）并取全部块转换。o 为空时先读页面本身。
func (n *napi) preparePage(nid string, o *nObject) func(ctx context.Context, b *builder, p *pnode) error {
	return func(ctx context.Context, b *builder, p *pnode) error {
		if o == nil || hasHostedFile(o) {
			var fresh nObject
			if err := n.c.call(ctx, "GET", "/v1/pages/"+nid, nil, &fresh); err == nil {
				o = &fresh
			} else if fatal(err) {
				return err
			}
		}
		if o != nil {
			if t := pageTitle(*o); t != "" {
				p.title = t
			}
			if err := n.iconCover(b, p, o); err != nil {
				return err
			}
		}
		return n.pageBlocks(ctx, b, p, nid)
	}
}

// pageBlocks 取页面的全部块并转换。
func (n *napi) pageBlocks(ctx context.Context, b *builder, p *pnode, nid string) error {
	bs, err := n.children(ctx, nid, 0)
	if err != nil {
		return err
	}
	cv := &blockConv{n: n, b: b, cur: p, ctx: ctx}
	p.blocks = cv.blocks(bs)
	p.noAutoChildren = true
	return cv.err
}

// children 取一个块（或页面）的全部子块，递归取嵌套子块。子页面、子数据库的内容单独导入。
func (n *napi) children(ctx context.Context, id string, depth int) ([]*nBlock, error) {
	var out []*nBlock
	var cursor *string
	for {
		q := "/v1/blocks/" + id + "/children?page_size=100"
		if cursor != nil {
			q += "&start_cursor=" + url.QueryEscape(*cursor)
		}
		var l nList
		if err := n.c.call(ctx, "GET", q, nil, &l); err != nil {
			if isNotFound(err) {
				n.b.warn("notion_no_access", id, "", "无权读取这部分内容，已跳过")
				return out, nil
			}
			return nil, n.wrap(err)
		}
		for _, raw := range l.Results {
			var head struct {
				ID          string `json:"id"`
				Type        string `json:"type"`
				HasChildren bool   `json:"has_children"`
				Archived    bool   `json:"archived"`
				InTrash     bool   `json:"in_trash"`
			}
			var all map[string]json.RawMessage
			if json.Unmarshal(raw, &head) != nil || json.Unmarshal(raw, &all) != nil || head.Archived || head.InTrash {
				continue
			}
			out = append(out, &nBlock{ID: head.ID, Type: head.Type, HasChildren: head.HasChildren, Data: all[head.Type]})
		}
		if !l.HasMore || l.NextCursor == nil {
			break
		}
		cursor = l.NextCursor
	}
	for _, b := range out {
		if !b.HasChildren || b.Type == "child_page" || b.Type == "child_database" {
			continue
		}
		if depth >= 30 {
			n.degrade("too_deep", "", "块嵌套太深，深处的内容未导入")
			continue
		}
		ch, err := n.children(ctx, b.ID, depth+1)
		if err != nil {
			return nil, err
		}
		b.Children = ch
	}
	return out, nil
}

// ---------- 块转换 ----------

type blockConv struct {
	n   *napi
	b   *builder
	cur *pnode
	ctx context.Context
	err error
}

func (c *blockConv) ref(targetNID string) string {
	id := c.n.idFor(targetNID)
	m := c.n.refs[c.cur.id]
	if m == nil {
		m = map[string]string{}
		c.n.refs[c.cur.id] = m
	}
	m[id] = normID(targetNID)
	return id
}

func (c *blockConv) blocks(bs []*nBlock) []lnmd.Node {
	var out []lnmd.Node
	for i := 0; i < len(bs); i++ {
		b := bs[i]
		switch b.Type {
		case "bulleted_list_item", "numbered_list_item", "to_do":
			lt, it := "bulletList", "listItem"
			switch b.Type {
			case "numbered_list_item":
				lt = "orderedList"
			case "to_do":
				lt, it = "taskList", "taskItem"
			}
			list := lnmd.Node{Type: lt}
			for ; i < len(bs) && bs[i].Type == b.Type; i++ {
				list.Content = append(list.Content, c.listItem(bs[i], it))
			}
			i--
			out = append(out, list)
		default:
			out = append(out, c.block(b)...)
		}
	}
	return out
}

type textBlock struct {
	RichText     []richText `json:"rich_text"`
	Color        string     `json:"color"`
	Checked      bool       `json:"checked"`
	Icon         *nFile     `json:"icon"`
	Language     string     `json:"language"`
	IsToggleable bool       `json:"is_toggleable"`
	Expression   string     `json:"expression"`
	URL          string     `json:"url"`
	Caption      []richText `json:"caption"`
	Title        string     `json:"title"`
	// 表格
	TableWidth      int  `json:"table_width"`
	HasColumnHeader bool `json:"has_column_header"`
	HasRowHeader    bool `json:"has_row_header"`
	Cells           [][]richText
	// 页面链接
	Type       string `json:"type"`
	PageID     string `json:"page_id"`
	DatabaseID string `json:"database_id"`
	// 列宽
	WidthRatio float64 `json:"width_ratio"`
}

func (b *nBlock) data() textBlock {
	var t textBlock
	_ = json.Unmarshal(b.Data, &t)
	if b.Type == "table_row" {
		var r struct {
			Cells [][]richText `json:"cells"`
		}
		_ = json.Unmarshal(b.Data, &r)
		t.Cells = r.Cells
	}
	return t
}

func colorAttr(color string) map[string]any {
	if color == "" || color == "default" {
		return nil
	}
	return map[string]any{"color": color}
}

func withAttrs(a map[string]any, kv ...any) map[string]any {
	if a == nil {
		a = map[string]any{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		a[kv[i].(string)] = kv[i+1]
	}
	return a
}

func para(inline []lnmd.Node, attrs map[string]any) lnmd.Node {
	return lnmd.Node{Type: "paragraph", Attrs: attrs, Content: inline}
}

func (c *blockConv) listItem(b *nBlock, typ string) lnmd.Node {
	d := b.data()
	item := lnmd.Node{Type: typ, Content: []lnmd.Node{para(c.inline(d.RichText), colorAttr(d.Color))}}
	if typ == "taskItem" {
		item.Attrs = map[string]any{"checked": d.Checked}
	}
	item.Content = append(item.Content, c.blocks(b.Children)...)
	return item
}

func ensureBlocks(ns []lnmd.Node) []lnmd.Node {
	if len(ns) == 0 {
		return []lnmd.Node{{Type: "paragraph"}}
	}
	return ns
}

func (c *blockConv) block(b *nBlock) []lnmd.Node {
	if c.err != nil {
		return nil
	}
	d := b.data()
	switch b.Type {
	case "paragraph":
		return append([]lnmd.Node{para(c.inline(d.RichText), colorAttr(d.Color))}, c.blocks(b.Children)...)
	case "heading_1", "heading_2", "heading_3":
		level := int(b.Type[len(b.Type)-1] - '0')
		if d.IsToggleable {
			return []lnmd.Node{{Type: "toggle", Attrs: map[string]any{"open": false, "level": level}, Content: []lnmd.Node{
				{Type: "toggleTitle", Content: c.inline(d.RichText)},
				{Type: "toggleBody", Content: ensureBlocks(c.blocks(b.Children))},
			}}}
		}
		return []lnmd.Node{{Type: "heading", Attrs: withAttrs(colorAttr(d.Color), "level", level), Content: c.inline(d.RichText)}}
	case "toggle":
		return []lnmd.Node{{Type: "toggle", Attrs: map[string]any{"open": false, "level": 0}, Content: []lnmd.Node{
			{Type: "toggleTitle", Content: c.inline(d.RichText)},
			{Type: "toggleBody", Content: ensureBlocks(c.blocks(b.Children))},
		}}}
	case "quote":
		return []lnmd.Node{{Type: "blockquote", Content: append([]lnmd.Node{para(c.inline(d.RichText), colorAttr(d.Color))}, c.blocks(b.Children)...)}}
	case "callout":
		icon := "icon:info"
		if d.Icon != nil && d.Icon.Type == "emoji" && d.Icon.Emoji != "" {
			icon = d.Icon.Emoji
		}
		color := d.Color
		if color == "" || color == "default" {
			color = "gray_background"
		}
		body := []lnmd.Node{}
		if len(d.RichText) > 0 {
			body = append(body, para(c.inline(d.RichText), nil))
		}
		body = append(body, c.blocks(b.Children)...)
		return []lnmd.Node{{Type: "callout", Attrs: map[string]any{"icon": icon, "color": color}, Content: ensureBlocks(body)}}
	case "code":
		code := plain(d.RichText)
		lang := strings.ToLower(d.Language)
		switch lang {
		case "mermaid":
			return []lnmd.Node{{Type: "mermaid", Attrs: map[string]any{"code": code}}}
		case "plain text", "":
			lang = ""
		}
		n := lnmd.Node{Type: "codeBlock"}
		if lang != "" {
			n.Attrs = map[string]any{"language": lang}
		}
		if code != "" {
			n.Content = []lnmd.Node{{Type: "text", Text: code}}
		}
		return []lnmd.Node{n}
	case "equation":
		return []lnmd.Node{{Type: "mathBlock", Attrs: map[string]any{"latex": d.Expression}}}
	case "divider":
		return []lnmd.Node{{Type: "horizontalRule"}}
	case "table_of_contents":
		return []lnmd.Node{{Type: "tableOfContents"}}
	case "breadcrumb":
		return []lnmd.Node{{Type: "breadcrumb"}}
	case "image", "file", "pdf", "video", "audio":
		return c.media(b)
	case "bookmark", "link_preview":
		a := map[string]any{"url": d.URL}
		if t := plain(d.Caption); t != "" {
			a["title"] = t
		}
		return []lnmd.Node{{Type: "bookmark", Attrs: a}}
	case "embed":
		return []lnmd.Node{{Type: "embed", Attrs: map[string]any{"url": d.URL}}}
	case "table":
		return []lnmd.Node{c.table(b, d)}
	case "column_list":
		cols := lnmd.Node{Type: "columns"}
		w := 1.0
		if len(b.Children) > 0 {
			w = math.Round(1000/float64(len(b.Children))) / 1000
		}
		for _, col := range b.Children {
			cw := w
			if r := col.data().WidthRatio; r > 0 {
				cw = math.Round(r*1000) / 1000
			}
			cols.Content = append(cols.Content, lnmd.Node{Type: "column", Attrs: map[string]any{"width": cw}, Content: ensureBlocks(c.blocks(col.Children))})
		}
		if len(cols.Content) == 0 {
			return nil
		}
		return []lnmd.Node{cols}
	case "column":
		return c.blocks(b.Children)
	case "child_page":
		child := c.n.childNode(c.cur, b.ID, nPage, d.Title)
		return []lnmd.Node{{Type: "childPage", Attrs: map[string]any{"pageId": child.id}}}
	case "child_database":
		child := c.n.childNode(c.cur, b.ID, nDB, d.Title)
		return []lnmd.Node{{Type: "childDatabase", Attrs: map[string]any{"pageId": child.id}}}
	case "link_to_page":
		target := d.PageID
		if d.Type == "database_id" {
			target = d.DatabaseID
		}
		if target == "" {
			return nil
		}
		return []lnmd.Node{{Type: "linkToPage", Attrs: map[string]any{"pageId": c.ref(target)}}}
	case "synced_block":
		c.n.degrade("synced_block", "", "同步块已转成普通内容")
		return c.blocks(b.Children)
	case "template", "unsupported", "ai_block":
		c.n.degrade("unsupported_block", b.Type, "有不支持的块（"+b.Type+"），已跳过")
		return nil
	}
	c.n.degrade("unsupported_block", b.Type, "有不支持的块（"+b.Type+"），已跳过")
	return c.blocks(b.Children)
}

// childNode 在当前页面下挂一个子页面 / 子数据库节点（之后在下一层建）。
func (n *napi) childNode(parent *pnode, nid, kind, title string) *pnode {
	k := normID(nid)
	if pn := n.planned[k]; pn != nil {
		return pn
	}
	pn := &pnode{id: n.idFor(k), kind: kind, title: title, src: nid}
	if kind == nDB {
		pn.prepare = n.prepareDB(k)
	} else {
		pn.prepare = n.preparePage(k, nil)
	}
	n.titles[k] = title
	n.planned[k] = pn
	parent.add(pn)
	n.b.grow(1)
	return pn
}

func (c *blockConv) table(b *nBlock, d textBlock) lnmd.Node {
	t := lnmd.Node{Type: "table"}
	for ri, row := range b.Children {
		if row.Type != "table_row" {
			continue
		}
		r := lnmd.Node{Type: "tableRow"}
		for ci, cell := range row.data().Cells {
			typ := "tableCell"
			if ri == 0 && d.HasColumnHeader || ci == 0 && d.HasRowHeader {
				typ = "tableHeader"
			}
			r.Content = append(r.Content, lnmd.Node{Type: typ, Content: []lnmd.Node{para(c.inline(cell), nil)}})
		}
		if len(r.Content) > 0 {
			t.Content = append(t.Content, r)
		}
	}
	if len(t.Content) == 0 {
		return para(nil, nil)
	}
	return t
}

// media 处理图片、文件、视频、音频：Notion 托管的下载成附件，外链保留。
func (c *blockConv) media(b *nBlock) []lnmd.Node {
	var f nFile
	_ = json.Unmarshal(b.Data, &f)
	u := f.url()
	if u == "" {
		return nil
	}
	caption := plain(f.Caption)
	if f.Type == "file" {
		name := f.Name
		if name == "" {
			name = path.Base(urlPath(u))
		}
		if dec, err := url.PathUnescape(name); err == nil {
			name = dec
		}
		m, err := c.b.uploadURL(u, name, c.cur.id, c.cur.src)
		if err != nil {
			c.err = err
			return nil
		}
		if m == nil {
			return []lnmd.Node{para([]lnmd.Node{{Type: "text", Text: "[" + name + "]"}}, nil)}
		}
		return []lnmd.Node{fileNode(m, caption)}
	}
	switch b.Type {
	case "image":
		a := map[string]any{"src": u}
		if caption != "" {
			a["alt"] = caption
		}
		return []lnmd.Node{{Type: "image", Attrs: a}}
	case "video", "audio":
		if b.Type == "video" && !videoURLRe.MatchString(u) {
			return []lnmd.Node{{Type: "embed", Attrs: map[string]any{"url": u}}}
		}
		return []lnmd.Node{{Type: b.Type, Attrs: map[string]any{"src": u, "title": caption}}}
	}
	name := f.Name
	if name == "" {
		name = path.Base(urlPath(u))
	}
	return []lnmd.Node{{Type: "bookmark", Attrs: map[string]any{"url": u, "title": name}}}
}

// inline 转换富文本：粗体、斜体、删除线、下划线、行内代码、颜色、链接、提及、行内公式。
func (c *blockConv) inline(rts []richText) []lnmd.Node {
	var out []lnmd.Node
	for _, r := range rts {
		switch r.Type {
		case "equation":
			if r.Equation != nil {
				out = append(out, lnmd.Node{Type: "mathInline", Attrs: map[string]any{"latex": r.Equation.Expression}})
			}
			continue
		case "mention":
			if m := r.Mention; m != nil {
				switch m.Type {
				case "page":
					if m.Page != nil {
						out = append(out, mentionNode(c.ref(m.Page.ID)))
						c.n.titles[normID(m.Page.ID)] = firstNonEmpty(c.n.titles[normID(m.Page.ID)], r.PlainText)
						continue
					}
				case "database":
					if m.Database != nil {
						out = append(out, mentionNode(c.ref(m.Database.ID)))
						c.n.titles[normID(m.Database.ID)] = firstNonEmpty(c.n.titles[normID(m.Database.ID)], r.PlainText)
						continue
					}
				case "date":
					if m.Date != nil {
						if s, ok := parseDate(m.Date.Start); ok {
							a := map[string]any{"kind": "date", "date": s[:10]}
							if m.Date.End != nil {
								if e, ok := parseDate(*m.Date.End); ok {
									a["endDate"] = e[:10]
								}
							}
							out = append(out, lnmd.Node{Type: "mention", Attrs: a})
							continue
						}
					}
				case "user":
					c.n.degrade("user_mention", "", "人员提及已转成文字")
				}
			}
		}
		text := r.PlainText
		if r.Text != nil && r.Text.Content != "" {
			text = r.Text.Content
		}
		if text == "" {
			continue
		}
		var marks []lnmd.Mark
		a := r.Annotations
		if a.Bold {
			marks = append(marks, lnmd.Mark{Type: "bold"})
		}
		if a.Italic {
			marks = append(marks, lnmd.Mark{Type: "italic"})
		}
		if a.Strikethrough {
			marks = append(marks, lnmd.Mark{Type: "strike"})
		}
		if a.Underline {
			marks = append(marks, lnmd.Mark{Type: "underline"})
		}
		if a.Code {
			marks = append(marks, lnmd.Mark{Type: "code"})
		}
		if col := a.Color; col != "" && col != "default" {
			if strings.HasSuffix(col, "_background") {
				marks = append(marks, lnmd.Mark{Type: "highlight", Attrs: map[string]any{"color": strings.TrimSuffix(col, "_background")}})
			} else {
				marks = append(marks, lnmd.Mark{Type: "textColor", Attrs: map[string]any{"color": col}})
			}
		}
		href := ""
		if r.Text != nil && r.Text.Link != nil {
			href = r.Text.Link.URL
		} else if r.Href != nil {
			href = *r.Href
		}
		if href != "" {
			if strings.HasPrefix(href, "/") {
				if id := hashIn(href); id != "" {
					href = "/p/" + c.ref(id)
				} else {
					href = "https://www.notion.so" + href
				}
			} else if u, err := url.Parse(href); err == nil && strings.HasSuffix(u.Hostname(), "notion.so") {
				if id := hashIn(u.Path); id != "" {
					href = "/p/" + c.ref(id)
				}
			}
			marks = append(marks, lnmd.Mark{Type: "link", Attrs: map[string]any{"href": href}})
		}
		parts := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		for i, p := range parts {
			if i > 0 {
				out = append(out, lnmd.Node{Type: "hardBreak"})
			}
			if p != "" {
				out = append(out, lnmd.Node{Type: "text", Text: p, Marks: marks})
			}
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---------- 数据库 ----------

// dbConv 记录一个数据库的属性转换方式。
type dbConv struct {
	nid    string
	props  []propConv
	schema *dbs.Schema
}

type propConv struct {
	name   string
	nid    string // Notion 属性 id
	id     string // 我们的属性 id
	ntype  string // Notion 类型
	typ    string // 我们的类型（空表示不导入）
	opts   map[string]string
	relDB  string // 关联的 Notion 数据库
	format string
}

func (n *napi) propID(dbNID, propNID string) string {
	k := normID(dbNID) + "\x00" + propNID
	if id, ok := n.propIDs[k]; ok {
		return id
	}
	id := "p" + ids.RandomToken(6)
	n.propIDs[k] = id
	return id
}

var numberFormats = map[string]string{"number_with_commas": "comma", "percent": "percent", "dollar": "dollar", "euro": "euro", "yuan": "yuan"}

var rollupFuncs = map[string]string{"count": "count", "count_all": "count", "count_values": "count_values", "not_empty": "not_empty", "count_not_empty": "not_empty",
	"empty": "empty", "count_empty": "empty", "unique": "unique", "count_unique_values": "unique", "percent_empty": "percent_empty",
	"percent_not_empty": "percent_not_empty", "sum": "sum", "average": "average", "median": "median", "min": "min", "max": "max", "range": "range",
	"earliest_date": "earliest", "latest_date": "latest", "checked": "checked", "count_checked": "checked", "show_original": "show_original"}

// orderedProps 按 JSON 里的书写顺序取出属性。
func orderedProps(raw json.RawMessage) ([]string, map[string]json.RawMessage) {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	var order []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err == nil && t == json.Delim('{') {
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				break
			}
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				break
			}
			if s, ok := k.(string); ok {
				order = append(order, s)
			}
		}
	}
	return order, m
}

var videoURLRe = regexp.MustCompile(`(?i)\.(mp4|webm|mov|ogv)(\?|$)`)

var notionPropRef = regexp.MustCompile(`\{\{notion:block_property:([^:}]+):[^}]*\}\}`)

// prepareDB 读数据库结构、转换属性，再分页取全部行挂到计划树上。
func (n *napi) prepareDB(nid string) func(ctx context.Context, b *builder, p *pnode) error {
	return func(ctx context.Context, b *builder, p *pnode) error {
		var raw json.RawMessage
		if err := n.c.call(ctx, "GET", "/v1/databases/"+nid, nil, &raw); err != nil {
			if isNotFound(err) {
				b.warn("notion_no_access", p.src, p.title, "无权读取这个数据库（可能是链接的数据库），已建空表")
				return nil
			}
			return n.wrap(err)
		}
		var o nObject
		_ = json.Unmarshal(raw, &o)
		var propsRaw struct {
			Properties json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal(raw, &propsRaw)
		if t := plain(o.Title); t != "" {
			p.title = t
		}
		p.inline = o.IsInline
		if err := n.iconCover(b, p, &o); err != nil {
			return err
		}
		dc := n.convertSchema(nid, propsRaw.Properties)
		n.dbInfo[normID(nid)] = dc
		p.schema = dc.schema
		// 行
		var cursor *string
		for {
			body := map[string]any{"page_size": 100}
			if cursor != nil {
				body["start_cursor"] = *cursor
			}
			var l nList
			if err := n.c.call(ctx, "POST", "/v1/databases/"+nid+"/query", body, &l); err != nil {
				if isNotFound(err) {
					break
				}
				return n.wrap(err)
			}
			for _, rr := range l.Results {
				var ro nObject
				if json.Unmarshal(rr, &ro) != nil || ro.Archived || ro.InTrash {
					continue
				}
				n.rowNode(p, dc, ro)
			}
			if !l.HasMore || l.NextCursor == nil {
				break
			}
			cursor = l.NextCursor
		}
		return nil
	}
}

func (n *napi) convertSchema(dbNID string, raw json.RawMessage) *dbConv {
	order, m := orderedProps(raw)
	dc := &dbConv{nid: dbNID, schema: &dbs.Schema{}}
	nameOf := map[string]string{}
	for _, name := range order {
		var p struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(m[name], &p)
		nameOf[p.ID] = name
	}
	used := map[string]bool{}
	for _, name := range order {
		var p struct {
			ID          string `json:"id"`
			Type        string `json:"type"`
			Description string `json:"description"`
			Number      *struct {
				Format string `json:"format"`
			} `json:"number"`
			Select *struct {
				Options []struct{ ID, Name, Color string } `json:"options"`
			} `json:"select"`
			MultiSelect *struct {
				Options []struct{ ID, Name, Color string } `json:"options"`
			} `json:"multi_select"`
			Status *struct {
				Options []struct{ ID, Name, Color string } `json:"options"`
				Groups  []struct {
					Name      string   `json:"name"`
					OptionIDs []string `json:"option_ids"`
				} `json:"groups"`
			} `json:"status"`
			Formula *struct {
				Expression string `json:"expression"`
			} `json:"formula"`
			Relation *struct {
				DatabaseID   string `json:"database_id"`
				Type         string `json:"type"`
				DualProperty *struct {
					SyncedPropertyID string `json:"synced_property_id"`
				} `json:"dual_property"`
			} `json:"relation"`
			Rollup *struct {
				RelationPropertyID string `json:"relation_property_id"`
				RollupPropertyID   string `json:"rollup_property_id"`
				Function           string `json:"function"`
			} `json:"rollup"`
			UniqueID *struct {
				Prefix *string `json:"prefix"`
			} `json:"unique_id"`
		}
		if json.Unmarshal(m[name], &p) != nil {
			continue
		}
		pc := propConv{name: name, nid: p.ID, ntype: p.Type, id: n.propID(dbNID, p.ID), opts: map[string]string{}}
		prop := dbs.Property{ID: pc.id, Name: name, Description: p.Description}
		addOpts := func(opts []struct{ ID, Name, Color string }, group func(id string) string) {
			seen := map[string]bool{}
			for _, o := range opts {
				if strings.TrimSpace(o.Name) == "" || seen[strings.ToLower(strings.TrimSpace(o.Name))] {
					continue
				}
				seen[strings.ToLower(strings.TrimSpace(o.Name))] = true
				oid := dbs.NewOptionID()
				pc.opts[o.ID] = oid
				pc.opts["name:"+strings.ToLower(strings.TrimSpace(o.Name))] = oid
				color := o.Color
				if color == "default" {
					color = "gray"
				}
				op := dbs.Option{ID: oid, Name: o.Name, Color: color}
				if group != nil {
					op.Group = group(o.ID)
				}
				prop.Options = append(prop.Options, op)
			}
		}
		switch p.Type {
		case "title":
			prop.ID, pc.id, pc.typ, prop.Type = "title", "title", dbs.TTitle, dbs.TTitle
		case "rich_text":
			pc.typ = dbs.TText
		case "number":
			pc.typ = dbs.TNumber
			if p.Number != nil {
				prop.Format = numberFormats[p.Number.Format]
			}
		case "select":
			pc.typ = dbs.TSelect
			if p.Select != nil {
				addOpts(p.Select.Options, nil)
			}
		case "multi_select":
			pc.typ = dbs.TMultiSelect
			if p.MultiSelect != nil {
				addOpts(p.MultiSelect.Options, nil)
			}
		case "status":
			pc.typ = dbs.TStatus
			if p.Status != nil {
				groupOf := map[string]string{}
				names := []string{"todo", "doing", "done"}
				for gi, g := range p.Status.Groups {
					for _, oid := range g.OptionIDs {
						groupOf[oid] = names[min(gi, 2)]
					}
				}
				addOpts(p.Status.Options, func(id string) string { return firstNonEmpty(groupOf[id], "todo") })
			}
		case "date":
			pc.typ = dbs.TDate
		case "checkbox":
			pc.typ = dbs.TCheckbox
		case "url":
			pc.typ = dbs.TURL
		case "email":
			pc.typ = dbs.TEmail
		case "phone_number":
			pc.typ = dbs.TPhone
		case "files":
			pc.typ = dbs.TFiles
		case "people", "created_by", "last_edited_by":
			pc.typ = dbs.TText
			n.degrade("people_as_text", name, "人员属性已转成文本")
		case "created_time", "last_edited_time":
			pc.typ = dbs.TDate
			n.degrade("time_as_date", name, "创建 / 编辑时间属性已转成普通日期，保留原来的时间")
		case "unique_id":
			pc.typ = dbs.TUniqueID
			if p.UniqueID != nil && p.UniqueID.Prefix != nil {
				prop.Prefix = *p.UniqueID.Prefix
			}
		case "formula":
			pc.typ = dbs.TText
			if p.Formula != nil {
				expr := notionPropRef.ReplaceAllStringFunc(p.Formula.Expression, func(s string) string {
					sm := notionPropRef.FindStringSubmatch(s)
					id := sm[1]
					if d, err := url.PathUnescape(id); err == nil {
						id = d
					}
					if nm, ok := nameOf[id]; ok {
						b, _ := json.Marshal(nm)
						return "prop(" + string(b) + ")"
					}
					return s
				})
				if _, err := dbs.ParseFormula(expr); err == nil && !strings.Contains(expr, "{{") {
					pc.typ = dbs.TFormula
					prop.Expression = expr
				} else {
					n.degrade("formula_as_text", name, "公式无法转换，已改成文本并保留当时的计算结果")
				}
			}
		case "relation":
			if p.Relation != nil && p.Relation.DatabaseID != "" {
				pc.typ = dbs.TRelation
				pc.relDB = normID(p.Relation.DatabaseID)
				prop.DatabaseID = n.idFor(pc.relDB)
				if p.Relation.DualProperty != nil && p.Relation.DualProperty.SyncedPropertyID != "" {
					prop.TwoWay = true
					prop.RelatedProp = n.propID(pc.relDB, p.Relation.DualProperty.SyncedPropertyID)
				}
			}
		case "rollup":
			if p.Rollup != nil {
				pc.typ = dbs.TRollup
				prop.Relation = n.propID(dbNID, p.Rollup.RelationPropertyID)
				prop.Function = firstNonEmpty(rollupFuncs[p.Rollup.Function], "show_original")
				// 目标属性在对方数据库里，等对方结构转换完才知道 id：先按关联属性找对方数据库
				var relDB string
				for _, other := range order {
					var q struct {
						ID       string `json:"id"`
						Relation *struct {
							DatabaseID string `json:"database_id"`
						} `json:"relation"`
					}
					if json.Unmarshal(m[other], &q) == nil && q.ID == p.Rollup.RelationPropertyID && q.Relation != nil {
						relDB = q.Relation.DatabaseID
					}
				}
				if relDB == "" {
					pc.typ = ""
				} else {
					prop.Target = n.propID(relDB, p.Rollup.RollupPropertyID)
				}
			}
		default:
			n.degrade("unsupported_property", p.Type, "有不支持的属性类型（"+p.Type+"），已跳过")
		}
		if pc.typ == "" {
			continue
		}
		if prop.Type == "" {
			prop.Type = pc.typ
		}
		if used[strings.ToLower(strings.TrimSpace(name))] {
			continue
		}
		used[strings.ToLower(strings.TrimSpace(name))] = true
		dc.props = append(dc.props, pc)
		dc.schema.Properties = append(dc.schema.Properties, prop)
	}
	if dc.schema.Title() == nil {
		dc.schema.Properties = append([]dbs.Property{{ID: "title", Name: "Name", Type: dbs.TTitle}}, dc.schema.Properties...)
	}
	if err := dc.schema.Validate(); err != nil {
		// 结构不合法（例如属性名冲突、公式有误）：不能校验通过的属性改成文本
		n.degrade("schema_fallback", "", "数据库结构有无法转换的部分，相关属性已改成文本")
		for i := range dc.schema.Properties {
			pr := &dc.schema.Properties[i]
			if pr.Type == dbs.TFormula || pr.Type == dbs.TRollup {
				pr.Type, pr.Expression, pr.Relation, pr.Target, pr.Function = dbs.TText, "", "", "", ""
				for j := range dc.props {
					if dc.props[j].id == pr.ID {
						dc.props[j].typ = dbs.TText
					}
				}
			}
		}
		_ = dc.schema.Validate()
	}
	return dc
}

// rowNode 把查询到的行挂到数据库节点下。
func (n *napi) rowNode(db *pnode, dc *dbConv, o nObject) {
	nid := normID(o.ID)
	if n.planned[nid] != nil {
		return
	}
	row := &pnode{id: n.idFor(nid), kind: nRow, title: pageTitle(o), src: o.URL, props: map[string]any{}}
	n.planned[nid] = row
	n.titles[nid] = row.title
	db.add(row)
	n.b.grow(1)
	needFiles := hasHostedFile(&o)
	for _, pc := range dc.props {
		if raw, ok := o.Properties[pc.name]; ok && pc.typ == dbs.TFiles && strings.Contains(string(raw), `"type":"file"`) {
			needFiles = true
		}
	}
	obj := o
	row.prepare = func(ctx context.Context, b *builder, p *pnode) error {
		if needFiles {
			// 文件地址一小时后过期：建行前重新读一次拿新地址
			var fresh nObject
			if err := n.c.call(ctx, "GET", "/v1/pages/"+nid, nil, &fresh); err == nil {
				obj = fresh
			} else if fatal(err) {
				return err
			}
		}
		if err := n.iconCover(b, p, &obj); err != nil {
			return err
		}
		if err := n.rowProps(ctx, b, p, dc, obj.Properties); err != nil {
			return err
		}
		return n.pageBlocks(ctx, b, p, nid)
	}
}

func (n *napi) rowProps(ctx context.Context, b *builder, p *pnode, dc *dbConv, props map[string]json.RawMessage) error {
	for _, pc := range dc.props {
		raw, ok := props[pc.name]
		if !ok || pc.typ == dbs.TTitle || pc.typ == dbs.TFormula || pc.typ == dbs.TRollup {
			continue
		}
		var v struct {
			Type        string                      `json:"type"`
			RichText    []richText                  `json:"rich_text"`
			Number      *float64                    `json:"number"`
			Select      *struct{ ID, Name string }  `json:"select"`
			Status      *struct{ ID, Name string }  `json:"status"`
			MultiSelect []struct{ ID, Name string } `json:"multi_select"`
			Date        *nDate                      `json:"date"`
			Checkbox    bool                        `json:"checkbox"`
			URL         *string                     `json:"url"`
			Email       *string                     `json:"email"`
			Phone       *string                     `json:"phone_number"`
			People      []struct {
				Name string `json:"name"`
			} `json:"people"`
			Files    []nFile `json:"files"`
			Relation []struct {
				ID string `json:"id"`
			} `json:"relation"`
			HasMore     bool   `json:"has_more"`
			CreatedTime string `json:"created_time"`
			EditedTime  string `json:"last_edited_time"`
			CreatedBy   *struct {
				Name string `json:"name"`
			} `json:"created_by"`
			EditedBy *struct {
				Name string `json:"name"`
			} `json:"last_edited_by"`
			Formula *struct {
				Type    string   `json:"type"`
				String  *string  `json:"string"`
				Number  *float64 `json:"number"`
				Boolean *bool    `json:"boolean"`
				Date    *nDate   `json:"date"`
			} `json:"formula"`
			UniqueID *struct {
				Number *float64 `json:"number"`
			} `json:"unique_id"`
		}
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		var val any
		opt := func(id, name string) any {
			if o, ok := pc.opts[id]; ok {
				return o
			}
			if o, ok := pc.opts["name:"+strings.ToLower(strings.TrimSpace(name))]; ok {
				return o
			}
			return nil
		}
		date := func(d *nDate) any {
			if d == nil {
				return nil
			}
			s, ok := parseDate(d.Start)
			if !ok {
				return nil
			}
			m := map[string]any{"start": s}
			if d.End != nil {
				if e, ok := parseDate(*d.End); ok {
					m["end"] = e
				}
			}
			return m
		}
		switch v.Type {
		case "rich_text":
			if s := plain(v.RichText); s != "" {
				val = s
			}
		case "number":
			if v.Number != nil {
				val = *v.Number
			}
		case "select":
			if v.Select != nil {
				val = opt(v.Select.ID, v.Select.Name)
			}
		case "status":
			if v.Status != nil {
				val = opt(v.Status.ID, v.Status.Name)
			}
		case "multi_select":
			var l []any
			for _, o := range v.MultiSelect {
				if x := opt(o.ID, o.Name); x != nil {
					l = append(l, x)
				}
			}
			if len(l) > 0 {
				val = l
			}
		case "date":
			val = date(v.Date)
		case "checkbox":
			val = v.Checkbox
		case "url":
			if v.URL != nil && *v.URL != "" {
				val = *v.URL
			}
		case "email":
			if v.Email != nil && *v.Email != "" {
				val = *v.Email
			}
		case "phone_number":
			if v.Phone != nil && *v.Phone != "" {
				val = *v.Phone
			}
		case "people":
			var names []string
			for _, u := range v.People {
				if u.Name != "" {
					names = append(names, u.Name)
				}
			}
			if len(names) > 0 {
				val = strings.Join(names, ", ")
			}
		case "created_by":
			if v.CreatedBy != nil && v.CreatedBy.Name != "" {
				val = v.CreatedBy.Name
			}
		case "last_edited_by":
			if v.EditedBy != nil && v.EditedBy.Name != "" {
				val = v.EditedBy.Name
			}
		case "created_time":
			val = date(&nDate{Start: v.CreatedTime})
		case "last_edited_time":
			val = date(&nDate{Start: v.EditedTime})
		case "files":
			var l []any
			for _, f := range v.Files {
				u := f.url()
				if u == "" {
					continue
				}
				name := firstNonEmpty(f.Name, path.Base(urlPath(u)))
				if f.Type == "file" {
					m, err := b.uploadURL(u, name, p.id, p.src)
					if err != nil {
						return err
					}
					if m != nil {
						l = append(l, map[string]any{"file_id": m.ID, "name": m.Name})
					}
				} else {
					l = append(l, map[string]any{"url": u, "name": name})
				}
			}
			if len(l) > 0 {
				val = l
			}
		case "relation":
			var l []any
			for _, r := range v.Relation {
				l = append(l, n.idFor(r.ID))
			}
			if len(l) > 0 {
				val = l
			}
			if v.HasMore {
				n.degrade("relation_truncated", pc.name, "关联超过 25 条的只导入了前 25 条")
			}
		case "formula":
			if v.Formula != nil {
				switch {
				case v.Formula.String != nil && *v.Formula.String != "":
					val = *v.Formula.String
				case v.Formula.Number != nil:
					val = strconv.FormatFloat(*v.Formula.Number, 'f', -1, 64)
				case v.Formula.Boolean != nil:
					val = strconv.FormatBool(*v.Formula.Boolean)
				case v.Formula.Date != nil:
					val = v.Formula.Date.Start
				}
			}
		case "unique_id":
			if v.UniqueID != nil && v.UniqueID.Number != nil {
				val = *v.UniqueID.Number
				if int64(*v.UniqueID.Number) > p.uniqueMax {
					p.uniqueMax = int64(*v.UniqueID.Number)
				}
			}
		}
		if val == nil {
			continue
		}
		if pc.typ == dbs.TUniqueID {
			p.props[pc.id] = val
			continue
		}
		prop := dc.schema.Prop(pc.id)
		if prop == nil {
			continue
		}
		nv, _, err := dbs.NormalizeValue(prop, val, dbs.Resolver{
			Relation: func(_, id string) (string, bool) { return id, true },
			User:     func(id string) (string, bool) { return id, true },
		})
		if err != nil || nv == nil {
			continue
		}
		p.props[pc.id] = nv
	}
	return nil
}

// ---------- 收尾：悬空引用 ----------

// fixDangling 处理指向没有导入的页面的引用：正文里的提及、页面链接改成文字；关联到没有导入的数据库的属性删掉。
func (n *napi) fixDangling(ctx context.Context) error {
	created := map[string]bool{}
	for _, pn := range n.planned {
		if pn.created {
			created[pn.id] = true
		}
	}
	// 关联属性
	for dbNID, dc := range n.dbInfo {
		pn := n.planned[dbNID]
		if pn == nil || !pn.created {
			continue
		}
		var keep []dbs.Property
		changed := false
		dropped := map[string]bool{}
		for _, p := range dc.schema.Properties {
			if p.Type == dbs.TRelation && !created[p.DatabaseID] {
				dropped[p.ID] = true
				changed = true
				n.b.warn("relation_dropped", pn.title, p.Name, "关联的数据库不在导入范围内，关联属性未导入")
				continue
			}
			keep = append(keep, p)
		}
		if !changed {
			continue
		}
		var final []dbs.Property
		for _, p := range keep {
			if p.Type == dbs.TRollup && dropped[p.Relation] {
				continue
			}
			if p.Type == dbs.TRelation && p.TwoWay && !created[p.DatabaseID] {
				continue
			}
			final = append(final, p)
		}
		if _, err := n.b.s.dbs.UpdateSchema(ctx, n.b.actor, pn.id, &dbs.Schema{Properties: final}); err != nil {
			n.b.s.log.Warn("导入后修正数据库结构失败", "job", n.b.j.id, "err", err)
		}
	}
	// 正文里的提及与页面链接
	for pageID, targets := range n.refs {
		var missing map[string]string
		for id, nid := range targets {
			if !created[id] {
				if missing == nil {
					missing = map[string]string{}
				}
				missing[id] = firstNonEmpty(n.titles[nid], "Notion 页面")
			}
		}
		if missing == nil || !created[pageID] {
			continue
		}
		if err := n.replaceRefs(ctx, pageID, missing); err != nil {
			n.b.s.log.Warn("导入后修正悬空提及失败", "job", n.b.j.id, "err", err)
		}
	}
	return nil
}

func (n *napi) replaceRefs(ctx context.Context, pageID string, missing map[string]string) error {
	bs, err := n.b.s.pages.Blocks(ctx, pageID)
	if err != nil {
		return err
	}
	docs := make([]pages.BlockDoc, 0, len(bs))
	changed := false
	for _, b := range bs {
		var node lnmd.Node
		if json.Unmarshal([]byte(b.Data), &node) != nil {
			docs = append(docs, pages.BlockDoc{ID: b.ID, Type: b.Type, Data: json.RawMessage(b.Data), Version: b.Version})
			continue
		}
		nn, ch := dropRefs(node, missing)
		if ch {
			changed = true
			n.degrade("dangling_ref", "", "有提及或页面链接指向没有导入的 Notion 页面，已改成文字")
		}
		data, _ := json.Marshal(nn)
		docs = append(docs, pages.BlockDoc{ID: b.ID, Type: nn.Type, Data: data, Version: b.Version})
	}
	if !changed {
		return nil
	}
	_, err = n.b.s.st.Write(ctx, n.b.actor, func(tx *store.Tx) error {
		_, err := n.b.s.pages.ReplaceBlocksTx(tx, n.b.actor, pageID, docs)
		return err
	})
	return err
}

func dropRefs(node lnmd.Node, missing map[string]string) (lnmd.Node, bool) {
	changed := false
	switch node.Type {
	case "mention":
		if id, _ := node.Attrs["id"].(string); node.Attrs["kind"] == "page" && missing[id] != "" {
			return lnmd.Node{Type: "text", Text: missing[id]}, true
		}
	case "linkToPage":
		if id, _ := node.Attrs["pageId"].(string); missing[id] != "" {
			attrs := map[string]any{}
			if v, ok := node.Attrs["id"]; ok {
				attrs["id"] = v
			}
			return lnmd.Node{Type: "paragraph", Attrs: attrs, Content: []lnmd.Node{{Type: "text", Text: missing[id]}}}, true
		}
	case "text":
		for i, m := range node.Marks {
			if m.Type == "link" {
				if h, _ := m.Attrs["href"].(string); strings.HasPrefix(h, "/p/") && missing[strings.TrimPrefix(h, "/p/")] != "" {
					node.Marks = append(append([]lnmd.Mark{}, node.Marks[:i]...), node.Marks[i+1:]...)
					return node, true
				}
			}
		}
	}
	for i := range node.Content {
		c, ch := dropRefs(node.Content[i], missing)
		if ch {
			node.Content[i] = c
			changed = true
		}
	}
	return node, changed
}
