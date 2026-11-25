// Package share 实现页面分享：分享给本系统用户、公开链接（匿名只读访问）。
//
// 公开链接的安全边界：
//   - 只读；令牌 32 字节随机数；可设口令（argon2id）与有效期；默认禁止搜索引擎收录。
//   - 范围 = 分享的页面（可含子孙页面）+ 范围内页面里的内嵌数据库及其行。
//   - 返回的正文、数据库都经过脱敏：指向范围外页面的提及、链接、子页面块一律去掉标题与 id；
//     关联到范围外数据库的关联、汇总、依赖它们的公式属性整列去掉；评论标记去掉。
//   - 系统设置关闭「允许公开链接」后，全部公开接口立即返回不存在。
package share

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/crypt"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/dbs"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/files"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/realtime"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/settings"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Service 分享服务。
type Service struct {
	st    *store.Store
	cat   *catalog.Catalog
	pages *pages.Service
	dbs   *dbs.Service
	files *files.Service
	set   *settings.Service
	box   *crypt.Box
	hub   *realtime.Hub
}

// New 创建分享服务。
func New(st *store.Store, cat *catalog.Catalog, pg *pages.Service, db *dbs.Service, fs *files.Service, set *settings.Service, box *crypt.Box, hub *realtime.Hub) *Service {
	return &Service{st: st, cat: cat, pages: pg, dbs: db, files: fs, set: set, box: box, hub: hub}
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Service) publicEnabled(ctx context.Context) bool { return s.set.System(ctx).PublicSharing }

// scope 是一次公开访问的可见范围判断（带缓存，单次请求内使用）。
type scope struct {
	cat   *catalog.Catalog
	share model.PageShare
	memo  map[string]bool
}

func (s *Service) newScope(sh model.PageShare) *scope {
	return &scope{cat: s.cat, share: sh, memo: map[string]bool{}}
}

// has 判断页面是否在公开范围内：
// 分享的页面本身 / 含子页面时的子孙 / 范围内页面里的内嵌数据库 / 范围内数据库的行。
func (sc *scope) has(id string) bool {
	if id == "" {
		return false
	}
	if v, ok := sc.memo[id]; ok {
		return v
	}
	sc.memo[id] = false // 防环
	v := sc.compute(id, 0)
	sc.memo[id] = v
	return v
}

func (sc *scope) compute(id string, depth int) bool {
	if depth > 64 {
		return false
	}
	if sc.cat.PublicCanView(sc.share, id) {
		return true
	}
	p, ok := sc.cat.Page(id)
	if !ok || p.TrashedAt != 0 {
		return false
	}
	if p.DatabaseID != "" {
		return sc.has(p.DatabaseID)
	}
	if p.Kind == model.KindDatabase && p.Inline && p.ParentID != "" {
		return sc.has(p.ParentID)
	}
	return false
}
