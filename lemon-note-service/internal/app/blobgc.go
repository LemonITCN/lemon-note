package app

import (
	"context"
	"os"
	"regexp"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// 附件回收：契约 §3 规定「引用计数为零且超过 30 天才清理」。
//  1. 扫描正文、页面封面图标与行属性、历史版本、智能体对话与检查点里出现的附件 id；
//  2. 创建超过 30 天且没有任何引用的附件行标记删除（deleted_at）；被重新引用的取消标记；
//  3. 同一内容（sha256）的全部附件行都已删除超过 30 天，才删除数据目录里的附件文件；
//     有备份正在进行时不删文件。数据目录里没有任何附件行指向、且修改时间超过 30 天的孤儿文件也删除。
const blobGrace = 30 * 24 * time.Hour

var fileRef = regexp.MustCompile(`(?:lemon://file/|/api/files/)([0-9a-z]{26})`)

func init() {
	RegisterJob(Job{Name: "gc-files", Interval: 24 * time.Hour, Run: gcFiles})
}

func scanRefs(ctx context.Context, st *store.Store, q string, refs map[string]bool, decode func([]byte) ([]byte, error)) error {
	rows, err := st.W.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return err
		}
		if decode != nil {
			if d, err := decode(b); err == nil {
				b = d
			}
		}
		for _, m := range fileRef.FindAllSubmatch(b, -1) {
			refs[string(m[1])] = true
		}
	}
	return rows.Err()
}

func gcFiles(ctx context.Context, s *Services) error {
	st := s.Inst.Store
	refs := map[string]bool{}
	for _, q := range []string{
		"SELECT data FROM blocks",
		"SELECT cover FROM pages", // 字符串拼接各数据库写法不同，分开查
		"SELECT icon FROM pages",
		"SELECT props FROM pages",
		"SELECT parts FROM agent_messages",
		"SELECT changes FROM agent_checkpoints",
	} {
		if err := scanRefs(ctx, st, q, refs, nil); err != nil {
			return err
		}
	}
	if err := scanRefs(ctx, st, "SELECT data FROM page_versions", refs, pages.VersionJSON); err != nil {
		return err
	}
	var files []model.File
	if err := st.Select(ctx, &files, "SELECT * FROM files"); err != nil {
		return err
	}
	now := time.Now()
	cut := now.Add(-blobGrace).UnixMilli()
	// 标记 / 取消标记
	var changed []model.File
	for _, f := range files {
		switch {
		case f.DeletedAt == nil && !refs[f.ID] && f.CreatedAt < cut:
			t := now.UnixMilli()
			f.DeletedAt = &t
			changed = append(changed, f)
		case f.DeletedAt != nil && refs[f.ID]:
			f.DeletedAt = nil
			changed = append(changed, f)
		}
	}
	for i := 0; i < len(changed); i += 200 {
		batch := changed[i:min(i+200, len(changed))]
		if _, err := st.Write(ctx, "system", func(tx *store.Tx) error {
			for _, f := range batch {
				if err := tx.Put("files", f); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	// 每个内容哈希是否还需要保留：有未删除的行，或删除不满 30 天
	keep := map[string]bool{}
	known := map[string]bool{}
	for _, f := range files {
		known[f.SHA256] = true
		del := f.DeletedAt
		for _, c := range changed {
			if c.ID == f.ID {
				del = c.DeletedAt
			}
		}
		if del == nil || *del >= cut {
			keep[f.SHA256] = true
		}
	}
	removed := 0
	var victims []string
	_ = s.Inst.Dir.ListBlobs(func(sha string, size int64) error {
		if keep[sha] {
			return nil
		}
		if !known[sha] { // 孤儿文件：上传中途失败等留下的，按修改时间判断
			p, err := s.Inst.Dir.BlobPath(sha)
			if err != nil {
				return nil
			}
			fi, err := os.Stat(p)
			if err != nil || now.Sub(fi.ModTime()) < blobGrace {
				return nil
			}
		}
		victims = append(victims, sha)
		return nil
	})
	for _, sha := range victims {
		if s.Inst.Held() {
			break // 备份进行中，下次再删
		}
		if err := s.Inst.Dir.DeleteBlob(sha); err == nil {
			removed++
		}
	}
	if len(changed) > 0 || removed > 0 {
		s.Tools.Log.Info("附件回收", "标记", len(changed), "删除文件", removed)
	}
	return nil
}
