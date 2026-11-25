package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/fracidx"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/netsafe"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// 计划树节点类型
const (
	nPage = "page"
	nDB   = "database"
	nRow  = "row"
)

// pnode 是计划树里的一个页面（普通页面、数据库或数据库行）。id 在生成计划时就分配好。
type pnode struct {
	id       string
	kind     string
	title    string
	icon     string
	cover    string
	src      string // 来源位置（报告用）
	parent   *pnode
	children []*pnode

	// 数据库
	schema *dbs.Schema
	inline bool
	// 行：属性值（属性 id → 存储格式），在 prepare 里也可以再补
	props     map[string]any
	uniqueMax int64

	// prepare 在建页前调用，生成正文（可以上传附件、修正标题与属性）
	prepare func(ctx context.Context, b *builder, n *pnode) error
	blocks  []lnmd.Node
	// noAutoChildren 为真时不在正文末尾自动补子页面块
	noAutoChildren bool

	pos     string
	failed  bool
	created bool
}

func (n *pnode) add(c *pnode) {
	c.parent = n
	n.children = append(n.children, c)
}

func newNode(kind, title, src string) *pnode {
	return &pnode{id: ids.New(), kind: kind, title: title, src: src}
}

// builder 把计划树写进数据库。
type builder struct {
	s         *Service
	j         *job
	ctx       context.Context
	actor     string
	spaceID   string
	parentID  string
	zh        bool
	maxFile   int64
	total     int
	done      int
	uploaded  map[string]*model.File
	dbNodes   []*pnode
	overLimit bool
}

func (s *Service) newBuilder(ctx context.Context, j *job, actor string, tgt Target) (*builder, error) {
	if _, ok := s.cat.Space(tgt.SpaceID); !ok {
		return nil, fail("目标空间不存在")
	}
	if tgt.ParentID != "" {
		p, ok := s.cat.Page(tgt.ParentID)
		if !ok || p.SpaceID != tgt.SpaceID || p.TrashedAt != 0 {
			return nil, fail("目标页面不存在")
		}
		if p.Kind == model.KindDatabase {
			return nil, fail("不能导入到数据库里")
		}
	}
	zh := true
	if u, ok := s.cat.User(actor); ok {
		var full model.User
		if err := s.st.Get(ctx, &full, "SELECT * FROM users WHERE id = ?", u.ID); err == nil && full.Locale == "en" {
			zh = false
		}
	}
	maxFile := int64(s.set.System(ctx).MaxUploadMB) << 20
	if maxFile <= 0 {
		maxFile = 200 << 20
	}
	return &builder{s: s, j: j, ctx: ctx, actor: actor, spaceID: tgt.SpaceID, parentID: tgt.ParentID, zh: zh, maxFile: maxFile,
		uploaded: map[string]*model.File{}}, nil
}

func (b *builder) warn(code, src, detail, msg string) {
	b.j.warn(Warning{Code: code, Path: src, Detail: detail, Message: msg})
}

func (b *builder) phase(p string, total int) {
	b.s.touch(b.ctx, b.j, func(pr *Progress) {
		pr.Phase = p
		if total >= 0 {
			pr.Total = total
		}
	})
}

func (b *builder) step(current string) {
	b.s.touch(b.ctx, b.j, func(pr *Progress) { pr.Current = current })
}

// build 按层级分批建页：上级先提交，下级在之后的批次里建。
func (b *builder) build(roots []*pnode) error {
	if len(roots) == 0 {
		return fail("没有找到可以导入的内容")
	}
	total := 0
	var count func(ns []*pnode)
	count = func(ns []*pnode) {
		for _, n := range ns {
			total++
			count(n.children)
		}
	}
	count(roots)
	if b.s.Limits.MaxPages > 0 && b.total+total > b.s.Limits.MaxPages {
		return fail("要导入的页面有 %d 个，超过单次上限 %d 个，请拆分后再导入", b.total+total, b.s.Limits.MaxPages)
	}
	b.total += total
	b.phase("creating", b.total)

	last := ""
	if sibs := b.s.cat.Children(b.spaceID, b.parentID); len(sibs) > 0 {
		last = sibs[len(sibs)-1].Position
	}
	keys, err := fracidx.NBetween(last, "", len(roots))
	if err != nil {
		return err
	}
	for i, r := range roots {
		r.parent = nil
		r.pos = keys[i]
	}
	level := roots
	for len(level) > 0 {
		if err := b.ctx.Err(); err != nil {
			return err
		}
		if err := b.createLevel(level); err != nil {
			return err
		}
		var next []*pnode
		for _, n := range level {
			if len(n.children) == 0 {
				continue
			}
			if n.failed || !n.created {
				b.skipTree(n.children)
				continue
			}
			ks, err := fracidx.NBetween("", "", len(n.children))
			if err != nil {
				return err
			}
			for i, c := range n.children {
				c.pos = ks[i]
			}
			next = append(next, n.children...)
		}
		level = next
	}
	return b.finishDatabases()
}

// grow 建页过程中发现了新页面（Notion API 边读边建）。
func (b *builder) grow(n int) {
	b.total += n
	if b.s.Limits.MaxPages > 0 && b.total > b.s.Limits.MaxPages && !b.overLimit {
		b.overLimit = true
	}
	total := b.total
	b.s.touch(b.ctx, b.j, func(p *Progress) { p.Total = total })
}

func (b *builder) skipTree(ns []*pnode) {
	for _, n := range ns {
		n.failed = true
		b.done++
		b.skipTree(n.children)
	}
}

const (
	batchPages = 25
	batchBytes = 4 << 20
)

type prepared struct {
	n      *pnode
	blocks []pages.BlockDoc
}

func (b *builder) createLevel(level []*pnode) error {
	var batch []prepared
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := b.commit(batch)
		batch, size = nil, 0
		return err
	}
	for _, n := range level {
		if err := b.ctx.Err(); err != nil {
			return err
		}
		if b.overLimit {
			return fail("要导入的页面超过单次上限 %d 个，已停止（已导入的部分保留）", b.s.Limits.MaxPages)
		}
		b.step(n.title)
		if n.prepare != nil {
			if err := n.prepare(b.ctx, b, n); err != nil {
				if fatal(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				b.warn("content_failed", n.src, "", "正文读取失败，已建空白页面："+userMessage(err))
				n.blocks = nil
			}
		}
		docs := b.blockDocs(n)
		for _, d := range docs {
			size += len(d.Data)
		}
		batch = append(batch, prepared{n: n, blocks: docs})
		if len(batch) >= batchPages || size >= batchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// blockDocs 把节点正文转成块，并在末尾补上正文里没有引用到的子页面。
func (b *builder) blockDocs(n *pnode) []pages.BlockDoc {
	if n.kind == nDB {
		return nil
	}
	nodes := n.blocks
	if len(n.children) > 0 {
		// 子页面按正文里出现的顺序排在前面，其余保持原顺序
		seen := map[string]bool{}
		var order []string
		walkNodes(nodes, func(x *lnmd.Node) {
			if x.Type == "childPage" || x.Type == "childDatabase" {
				if id, _ := x.Attrs["pageId"].(string); id != "" && !seen[id] {
					seen[id] = true
					order = append(order, id)
				}
			}
		})
		if len(order) > 0 {
			byID := map[string]*pnode{}
			for _, c := range n.children {
				byID[c.id] = c
			}
			sorted := make([]*pnode, 0, len(n.children))
			for _, id := range order {
				if c := byID[id]; c != nil {
					sorted = append(sorted, c)
				}
			}
			for _, c := range n.children {
				if !seen[c.id] {
					sorted = append(sorted, c)
				}
			}
			n.children = sorted
		}
		if n.noAutoChildren {
			seen = nil
		}
		for _, c := range n.children {
			if seen == nil {
				break
			}
			if seen[c.id] || c.kind == nRow {
				continue
			}
			t := "childPage"
			if c.kind == nDB {
				t = "childDatabase"
			}
			nodes = append(nodes, lnmd.Node{Type: t, Attrs: map[string]any{"pageId": c.id}})
		}
	}
	out := make([]pages.BlockDoc, 0, len(nodes))
	for _, x := range nodes {
		if x.Attrs != nil {
			delete(x.Attrs, "id")
		}
		data, err := json.Marshal(x)
		if err != nil {
			continue
		}
		if len(data) > pages.MaxBlockBytes {
			b.warn("block_too_large", n.src, x.Type, "有一个块内容过大，已跳过")
			data, _ = json.Marshal(lnmd.Node{Type: "paragraph", Content: []lnmd.Node{{Type: "text", Text: "[内容过大，未导入]"}}})
		}
		out = append(out, pages.BlockDoc{Type: x.Type, Data: data})
	}
	return out
}

// walkNodes 深度优先遍历节点。
func walkNodes(ns []lnmd.Node, fn func(n *lnmd.Node)) {
	for i := range ns {
		fn(&ns[i])
		walkNodes(ns[i].Content, fn)
	}
}

// commit 在一个事务里建一批页面；失败时逐个重试，仍失败的跳过（连同子页面）。
func (b *builder) commit(batch []prepared) error {
	_, err := b.s.st.Write(b.ctx, b.actor, func(tx *store.Tx) error {
		for _, p := range batch {
			if err := b.createTx(tx, p); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		for _, p := range batch {
			b.created(p.n)
		}
		return nil
	}
	if errors.Is(err, store.ErrReadOnly) || b.ctx.Err() != nil {
		return err
	}
	for _, p := range batch {
		_, err := b.s.st.Write(b.ctx, b.actor, func(tx *store.Tx) error { return b.createTx(tx, p) })
		if err != nil {
			if errors.Is(err, store.ErrReadOnly) || b.ctx.Err() != nil {
				return err
			}
			p.n.failed = true
			b.done++
			b.warn("page_failed", p.n.src, p.n.title, "页面无法创建，已跳过（含子页面）："+userMessage(err))
			b.s.log.Warn("导入建页失败", "job", b.j.id, "title", p.n.title, "err", err)
			continue
		}
		b.created(p.n)
	}
	return nil
}

func (b *builder) parentOf(n *pnode) string {
	if n.parent == nil {
		return b.parentID
	}
	return n.parent.id
}

func (b *builder) createTx(tx *store.Tx, p prepared) error {
	n := p.n
	switch n.kind {
	case nDB:
		_, _, err := b.s.dbs.CreateTx(tx, b.actor, dbs.CreateInput{ID: n.id, Position: n.pos, SpaceID: b.spaceID, ParentID: b.parentOf(n),
			Title: n.title, Icon: n.icon, Inline: n.inline, Schema: n.schema, Zh: b.zh})
		return err
	case nRow:
		props, _ := json.Marshal(n.props)
		if n.props == nil {
			props = []byte("{}")
		}
		_, err := b.s.pages.CreateTx(tx, b.actor, pages.CreateInput{ID: n.id, SpaceID: b.spaceID, ParentID: n.parent.id, DatabaseID: n.parent.id,
			Title: n.title, Icon: n.icon, Cover: n.cover, Props: props, Position: n.pos, Blocks: p.blocks})
		return err
	default:
		_, err := b.s.pages.CreateTx(tx, b.actor, pages.CreateInput{ID: n.id, SpaceID: b.spaceID, ParentID: b.parentOf(n), Kind: model.KindPage,
			Title: n.title, Icon: n.icon, Cover: n.cover, Position: n.pos, Blocks: p.blocks})
		return err
	}
}

func (b *builder) created(n *pnode) {
	n.created = true
	n.blocks = nil
	b.done++
	b.j.mu.Lock()
	switch n.kind {
	case nDB:
		b.j.report.Databases++
	case nRow:
		b.j.report.Rows++
	default:
		b.j.report.Pages++
	}
	if n.parent == nil {
		b.j.report.RootIDs = append(b.j.report.RootIDs, n.id)
	}
	b.j.mu.Unlock()
	if n.kind == nDB {
		b.dbNodes = append(b.dbNodes, n)
	}
	done := b.done
	b.s.touch(b.ctx, b.j, func(pr *Progress) { pr.Done = done })
}

// finishDatabases 更新数据库的行号计数器（编号属性接着导入的最大值往下编）。
func (b *builder) finishDatabases() error {
	for i := 0; i < len(b.dbNodes); i += 50 {
		end := min(i+50, len(b.dbNodes))
		group := b.dbNodes[i:end]
		_, err := b.s.st.Write(b.ctx, b.actor, func(tx *store.Tx) error {
			for _, n := range group {
				rows := int64(0)
				for _, c := range n.children {
					if c.kind == nRow && c.created {
						rows++
						if c.uniqueMax > rows {
							rows = c.uniqueMax
						}
					}
				}
				if rows == 0 {
					continue
				}
				var d model.Database
				if err := tx.Get(&d, "SELECT * FROM databases WHERE id = ?", n.id); err != nil {
					continue
				}
				if rows > d.RowCounter {
					d.RowCounter = rows
					d.UpdatedAt = model.Now()
					if err := tx.Put("databases", d); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ---------- 附件 ----------

// uploadVFS 把导入包里的文件存成附件（同一个文件只存一次）。
func (b *builder) uploadVFS(v *vfs, f *vfile, pageID string) (*model.File, error) {
	key := "vfs:" + f.path
	if m, ok := b.uploaded[key]; ok {
		return m, nil
	}
	if f.size > b.maxFile {
		b.warn("file_too_large", f.path, fmt.Sprintf("%d MB", b.maxFile>>20), fmt.Sprintf("附件超过大小上限（%d MB），已跳过", b.maxFile>>20))
		return nil, nil
	}
	rc, err := v.open(f)
	if err != nil {
		b.warn("file_failed", f.path, "", "附件读取失败，已跳过")
		return nil, nil
	}
	defer rc.Close()
	m, err := b.s.files.Save(b.ctx, b.actor, b.spaceID, pageID, path.Base(f.path), rc, b.maxFile)
	if err != nil {
		if fatal(err) {
			return nil, err
		}
		if strings.Contains(err.Error(), "大小上限") {
			b.warn("file_too_large", f.path, fmt.Sprintf("%d MB", b.maxFile>>20), fmt.Sprintf("附件超过大小上限（%d MB），已跳过", b.maxFile>>20))
		} else {
			b.warn("file_failed", f.path, "", "附件保存失败，已跳过")
			b.s.log.Warn("导入附件失败", "job", b.j.id, "err", err)
		}
		return nil, nil
	}
	b.uploaded[key] = m
	b.j.mu.Lock()
	b.j.report.Files++
	b.j.mu.Unlock()
	return m, nil
}

// uploadURL 下载外部文件存成附件（经防 SSRF 客户端）。
func (b *builder) uploadURL(raw, name, pageID, src string) (*model.File, error) {
	key := "url:" + raw
	if m, ok := b.uploaded[key]; ok {
		return m, nil
	}
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		b.warn("file_failed", src, name, "附件地址无效，已跳过")
		return nil, nil
	}
	if err := netsafe.CheckURL(req.URL); err != nil {
		b.warn("file_failed", src, name, "附件地址被拦截，已跳过")
		return nil, nil
	}
	req.Header.Set("User-Agent", "lemon-note-importer")
	resp, err := netsafe.Client(10 * time.Minute).Do(req)
	if err != nil {
		if b.ctx.Err() != nil {
			return nil, b.ctx.Err()
		}
		b.warn("file_failed", src, name, "附件下载失败，已跳过")
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.warn("file_failed", src, name, fmt.Sprintf("附件下载失败（HTTP %d），已跳过", resp.StatusCode))
		return nil, nil
	}
	if resp.ContentLength > b.maxFile {
		b.warn("file_too_large", src, name, fmt.Sprintf("附件超过大小上限（%d MB），已跳过", b.maxFile>>20))
		return nil, nil
	}
	m, err := b.s.files.Save(b.ctx, b.actor, b.spaceID, pageID, name, io.LimitReader(resp.Body, b.maxFile+1), b.maxFile)
	if err != nil {
		if strings.Contains(err.Error(), "大小上限") {
			b.warn("file_too_large", src, name, fmt.Sprintf("附件超过大小上限（%d MB），已跳过", b.maxFile>>20))
		} else {
			b.warn("file_failed", src, name, "附件保存失败，已跳过")
		}
		return nil, nil
	}
	b.uploaded[key] = m
	b.j.mu.Lock()
	b.j.report.Files++
	b.j.mu.Unlock()
	return m, nil
}

// fileNode 按附件类型生成块：图片、视频、音频或文件块。
func fileNode(m *model.File, alt string) lnmd.Node {
	src := "lemon://file/" + m.ID
	switch {
	case strings.HasPrefix(m.Mime, "image/"):
		a := map[string]any{"src": src}
		if alt != "" {
			a["alt"] = alt
		}
		return lnmd.Node{Type: "image", Attrs: a}
	case strings.HasPrefix(m.Mime, "video/"):
		return lnmd.Node{Type: "video", Attrs: map[string]any{"src": src, "title": m.Name}}
	case strings.HasPrefix(m.Mime, "audio/"):
		return lnmd.Node{Type: "audio", Attrs: map[string]any{"src": src, "title": m.Name}}
	}
	return lnmd.Node{Type: "fileBlock", Attrs: map[string]any{"src": src, "name": m.Name, "size": m.Size, "mime": m.Mime}}
}
