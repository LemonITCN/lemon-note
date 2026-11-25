package model_test

import (
	"reflect"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// 每个结构体必须恰好覆盖对应表的全部列，否则 tx.Put 会在运行时报错或漏写列。
func TestModelsCoverTables(t *testing.T) {
	cases := map[string]any{
		"users": model.User{}, "api_tokens": model.APIToken{}, "spaces": model.Space{}, "space_members": model.SpaceMember{},
		"pages": model.Page{}, "blocks": model.Block{}, "databases": model.Database{}, "db_views": model.DBView{},
		"page_versions": model.PageVersion{}, "comment_threads": model.CommentThread{}, "comments": model.Comment{},
		"favorites": model.Favorite{}, "files": model.File{}, "page_shares": model.PageShare{}, "notifications": model.Notification{},
		"agent_conversations": model.AgentConversation{}, "agent_messages": model.AgentMessage{}, "agent_checkpoints": model.AgentCheckpoint{},
		"ai_providers": model.AIProvider{}, "settings": model.Setting{}, "backup_targets": model.BackupTarget{}, "backup_runs": model.BackupRun{},
		"audit_logs": model.AuditLog{}, "sessions": model.Session{}, "login_attempts": model.LoginAttempt{}, "recent_pages": model.RecentPage{},
	}
	for table, v := range cases {
		tab, ok := store.TableByName(table)
		if !ok {
			t.Fatalf("表 %s 不存在", table)
		}
		row, err := store.ToRow(v)
		if err != nil {
			t.Fatal(err)
		}
		cols := map[string]bool{}
		for _, c := range tab.Columns {
			cols[c.Name] = true
			if _, ok := row[c.Name]; !ok {
				t.Errorf("%s 缺少列 %s（%s）", reflect.TypeOf(v).Name(), c.Name, table)
			}
		}
		for k := range row {
			if !cols[k] {
				t.Errorf("%s 多出列 %s（%s）", reflect.TypeOf(v).Name(), k, table)
			}
		}
	}
}
