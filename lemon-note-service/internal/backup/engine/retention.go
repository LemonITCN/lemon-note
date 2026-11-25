package engine

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/backup/target"
)

// pkgFiles 是目标上一个全量包的全部分卷。
type pkgFiles struct {
	Base  string
	ID8   string
	Time  time.Time
	Tail  *target.Entry
	Parts map[int]target.Entry
}

// Complete 表示最后一卷已经上传（包完整可用的前提）。
func (p *pkgFiles) Complete() bool { return p.Tail != nil }

// Size 返回全部分卷的总大小。
func (p *pkgFiles) Size() int64 {
	var n int64
	if p.Tail != nil {
		n += p.Tail.Size
	}
	for _, e := range p.Parts {
		n += e.Size
	}
	return n
}

// Volumes 按顺序返回分卷路径（最后一个是 .lnbak）。
func (p *pkgFiles) Volumes() []string {
	nums := make([]int, 0, len(p.Parts))
	for n := range p.Parts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]string, 0, len(nums)+1)
	for _, n := range nums {
		out = append(out, p.Parts[n].Path)
	}
	if p.Tail != nil {
		out = append(out, p.Tail.Path)
	}
	return out
}

// checkVolumes 确认分卷编号从 1 起连续、除最后一卷外大小一致、最后一卷不大于前面的分卷。
func (p *pkgFiles) checkVolumes() error {
	if p.Tail == nil {
		return uerrf("备份包 %s 不完整（缺少最后一卷 .lnbak）", tailName(p.Base))
	}
	if p.Tail.Size == 0 {
		return uerrf("备份包 %s 的最后一卷是空的", tailName(p.Base))
	}
	var partSize int64 = -1
	for i := 1; i <= len(p.Parts); i++ {
		e, ok := p.Parts[i]
		if !ok {
			return uerrf("备份包 %s 缺少分卷 %s", tailName(p.Base), partName(p.Base, i))
		}
		if partSize < 0 {
			partSize = e.Size
		} else if e.Size != partSize {
			return uerrf("备份包 %s 的分卷 %s 大小不对（%d 字节，其他分卷是 %d 字节），可能上传不完整", tailName(p.Base), e.Path, e.Size, partSize)
		}
	}
	if partSize >= 0 && p.Tail.Size > partSize {
		return uerrf("备份包 %s 的最后一卷比前面的分卷还大，分卷可能被替换过", tailName(p.Base))
	}
	return nil
}

// parsePackages 从目标的文件列表里认出全量包，按 base 分组。只认根目录下的文件。
func parsePackages(entries []target.Entry) map[string]*pkgFiles {
	out := map[string]*pkgFiles{}
	for _, e := range entries {
		m := pkgRe.FindStringSubmatch(e.Path)
		if m == nil {
			continue
		}
		ts, err := time.ParseInLocation(pkgTimeLayout, m[2], time.UTC)
		if err != nil {
			continue
		}
		base := "lemon-note-" + m[1] + "-" + m[2]
		pk := out[base]
		if pk == nil {
			pk = &pkgFiles{Base: base, ID8: m[1], Time: ts, Parts: map[int]target.Entry{}}
			out[base] = pk
		}
		if m[3] == "" {
			ent := e
			pk.Tail = &ent
		} else {
			n, err := strconv.Atoi(m[3])
			if err != nil || n < 1 {
				continue
			}
			pk.Parts[n] = e
		}
	}
	return out
}

// selectKeep 按保留策略从完整的包里挑出要保留的（输入按时间从新到旧排序）。
// 分层保留与 restic 的 keep-daily / keep-weekly / keep-monthly 语义一致：在有备份的最近 N 天（周、月）里，各留当天（周、月）最新的一个。
// 最新的一个包总是保留。
func selectKeep(pkgs []*pkgFiles, r Retention, loc *time.Location) map[string]bool {
	keep := map[string]bool{}
	if len(pkgs) == 0 {
		return keep
	}
	keep[pkgs[0].Base] = true
	switch r.Kind {
	case RetainTiered:
		bucket := func(limit int, key func(time.Time) string) {
			if limit <= 0 {
				return
			}
			seen := map[string]bool{}
			for _, p := range pkgs {
				k := key(p.Time.In(loc))
				if seen[k] {
					continue
				}
				if len(seen) >= limit {
					break
				}
				seen[k] = true
				keep[p.Base] = true
			}
		}
		bucket(r.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
		bucket(r.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) })
		bucket(r.Monthly, func(t time.Time) string { return t.Format("2006-01") })
	default:
		n := r.KeepLast
		if n < 1 {
			n = 1
		}
		for i, p := range pkgs {
			if i >= n {
				break
			}
			keep[p.Base] = true
		}
	}
	return keep
}

// applyRetention 删除超出保留策略的旧包，以及没有最后一卷的残缺包（上次中断留下的）。只处理本实例的包。
func (e *Engine) applyRetention(ctx context.Context, t target.Target, id8, current string, r Retention, p *Progress) error {
	list, err := t.List(ctx, "lemon-note-"+id8+"-")
	if err != nil {
		return wrapUser(err)
	}
	all := parsePackages(list)
	var complete []*pkgFiles
	for _, pk := range all {
		if pk.ID8 != id8 {
			continue
		}
		if pk.Complete() {
			complete = append(complete, pk)
			continue
		}
		if pk.Base == current {
			continue
		}
		for _, v := range pk.Volumes() {
			if err := t.Delete(ctx, v); err != nil {
				return wrapUser(fmt.Errorf("删除残缺分卷 %s 失败：%w", v, err))
			}
		}
		p.Logf("已删除上次未完成的残缺备份 %s（%d 个分卷）", tailName(pk.Base), len(pk.Parts))
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].Time.After(complete[j].Time) })
	keep := selectKeep(complete, r, e.loc())
	keep[current] = true
	for _, pk := range complete {
		if keep[pk.Base] {
			continue
		}
		// 先删最后一卷，让包立刻变成「不完整」，再删前面的分卷；中途失败只会留下残缺分卷，下次会清理。
		vols := pk.Volumes()
		for i := len(vols) - 1; i >= 0; i-- {
			if err := t.Delete(ctx, vols[i]); err != nil {
				return wrapUser(fmt.Errorf("删除旧备份 %s 失败：%w", vols[i], err))
			}
		}
		p.Logf("按保留策略删除旧备份 %s", tailName(pk.Base))
	}
	return nil
}
