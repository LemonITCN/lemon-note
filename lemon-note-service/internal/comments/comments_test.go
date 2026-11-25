package comments

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/accounts"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

func TestCommentLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Engine: store.SQLite, WorkPath: filepath.Join(t.TempDir(), "w.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cat, err := catalog.Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	acc := accounts.New(st, cat)
	a, _ := acc.CreateUser(ctx, "", accounts.NewUser{Username: "alice", Password: "password-123", Role: model.RoleAdmin})
	b, _ := acc.CreateUser(ctx, a.ID, accounts.NewUser{Username: "bob", Password: "password-123", Role: model.RoleMember})
	sp := cat.VisibleSpaces(a.ID)[0].ID
	p, err := pages.New(st, cat).Create(ctx, a.ID, pages.CreateInput{SpaceID: sp, Title: "文档"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, cat)

	if _, err := s.Add(ctx, a.ID, p.ID, AddInput{Body: "   "}); err != ErrEmpty {
		t.Fatalf("空评论应报错: %v", err)
	}
	th, err := s.Add(ctx, a.ID, p.ID, AddInput{BlockID: "blk", Anchor: "被评论的原文", Body: "这里要改"})
	if err != nil {
		t.Fatal(err)
	}
	if th.Anchor != "被评论的原文" || len(th.Comments) != 1 || model.Deref(th.BlockID) != "blk" {
		t.Fatalf("线程不对: %+v", th)
	}
	if _, err := s.Edit(ctx, b.ID, th.Comments[0].ID, "改别人的"); err != ErrNotOwner {
		t.Fatalf("不能改别人的评论: %v", err)
	}
	if _, err := s.Resolve(ctx, a.ID, th.ID, true); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(ctx, p.ID, false); len(l) != 0 {
		t.Fatal("已解决的默认不列出")
	}
	if l, _ := s.List(ctx, p.ID, true); len(l) != 1 {
		t.Fatal("include_resolved 应列出")
	}
	th2, err := s.Add(ctx, a.ID, p.ID, AddInput{ThreadID: th.ID, Body: "回复"})
	if err != nil {
		t.Fatal(err)
	}
	if th2.ResolvedAt != nil || len(th2.Comments) != 2 {
		t.Fatal("回复应重新打开线程")
	}
	if _, err := s.Add(ctx, a.ID, "other-page", AddInput{ThreadID: th.ID, Body: "串页"}); err != ErrNotFound {
		t.Fatalf("不能往别的页面的线程里回复: %v", err)
	}
	if _, err := s.Delete(ctx, b.ID, th2.Comments[0].ID, false); err != ErrNotOwner {
		t.Fatal("普通用户不能删别人的评论")
	}
	gone, err := s.Delete(ctx, a.ID, th2.Comments[0].ID, false)
	if err != nil || gone {
		t.Fatalf("删一条后线程还在: %v %v", gone, err)
	}
	gone, err = s.Delete(ctx, b.ID, th2.Comments[1].ID, true)
	if err != nil || !gone {
		t.Fatalf("删光后线程应删除: %v %v", gone, err)
	}
	if l, _ := s.List(ctx, p.ID, true); len(l) != 0 {
		t.Fatal("线程应已删除")
	}
}
