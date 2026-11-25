package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/realtime"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// Mount 挂载智能体接口。
func (s *Service) Mount(ws *web.Server, r chi.Router) {
	h := ws.H
	r.Get("/api/agent/models", h(s.hModels))
	r.Get("/api/agent/conversations", h(s.hList))
	r.Post("/api/agent/conversations", h(s.hCreate))
	r.Get("/api/agent/conversations/{id}", h(s.hGet))
	r.Patch("/api/agent/conversations/{id}", h(s.hPatch))
	r.Delete("/api/agent/conversations/{id}", h(s.hDelete))
	r.Post("/api/agent/conversations/{id}/messages", h(s.hSend))
	r.Get("/api/agent/runs/{id}/events", s.hEvents)
	r.Post("/api/agent/runs/{id}/stop", h(s.hStop))
	r.Post("/api/agent/runs/{id}/respond", h(s.hRespond))
	r.Post("/api/agent/checkpoints/{id}/revert", h(s.hRevert))
	r.Get("/api/agent/instructions", h(s.hGetInstr))
	r.Put("/api/agent/instructions", h(s.hPutInstr))
	r.Group(func(r chi.Router) {
		r.Use(ws.RequireAdmin)
		r.Get("/api/admin/ai", h(s.hAdminGet))
		r.Put("/api/admin/ai", h(s.hAdminPut))
	})
	ws.WSHandlers["client_tool_result"] = func(conn *realtime.Conn, data []byte) { s.ResolveClient(conn.UserID, data) }
}

func uid(r *http.Request) string { return web.UserOf(r).ID }

func (s *Service) hModels(w http.ResponseWriter, r *http.Request) error {
	prov, err := s.Provider(r.Context())
	if err != nil {
		return web.OK(w, map[string]any{"ready": false, "models": []any{}, "error": err.Error()})
	}
	ms, err := prov.Models(r.Context())
	if err != nil {
		return web.OK(w, map[string]any{"ready": false, "models": []any{}, "error": fmtErr(err)})
	}
	var chat []llm.Model
	for _, m := range ms {
		if m.Type == "chat" {
			chat = append(chat, m)
		}
	}
	return web.OK(w, map[string]any{"ready": true, "models": chat, "default": s.DefaultModel(r.Context(), ms)})
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) error {
	var rows []model.AgentConversation
	arch := r.URL.Query().Get("archived") == "1"
	q := "SELECT * FROM agent_conversations WHERE user_id = ? AND archived_at IS NULL ORDER BY updated_at DESC LIMIT 200"
	if arch {
		q = "SELECT * FROM agent_conversations WHERE user_id = ? AND archived_at IS NOT NULL ORDER BY updated_at DESC LIMIT 200"
	}
	if err := s.st.Select(r.Context(), &rows, q, uid(r)); err != nil {
		return err
	}
	out := []map[string]any{}
	for i := range rows {
		c := decodeConv(&rows[i])
		running := s.ActiveRun(c.ID) != nil
		out = append(out, map[string]any{"id": c.ID, "title": c.Title, "updated_at": c.UpdatedAt, "settings": c.Cfg, "running": running})
	}
	return web.OK(w, map[string]any{"conversations": out})
}

func (s *Service) hCreate(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		SpaceID string       `json:"space_id"`
		Cfg     ConvSettings `json:"settings"`
	}
	_ = web.ReadJSON(r, &in)
	c, err := s.CreateConversation(r.Context(), uid(r), in.SpaceID, in.Cfg)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"conversation": c})
}

func (s *Service) hGet(w http.ResponseWriter, r *http.Request) error {
	c, err := s.getConv(r.Context(), uid(r), chi.URLParam(r, "id"))
	if err != nil {
		return web.NotFound("会话")
	}
	msgs, err := s.loadMessages(r.Context(), c.ID)
	if err != nil {
		return err
	}
	// 隐藏片段与图片数据不发给界面
	for i := range msgs {
		var kept []StoredPart
		for _, p := range msgs[i].Parts {
			if p.Hidden {
				continue
			}
			if p.Type == "image" {
				p.Part.Data = ""
			}
			if p.Type == "reasoning" {
				p.Signature = ""
			}
			kept = append(kept, p)
		}
		msgs[i].Parts = kept
	}
	var ckpts []model.AgentCheckpoint
	_ = s.st.Select(r.Context(), &ckpts, "SELECT * FROM agent_checkpoints WHERE conversation_id = ? ORDER BY created_at", c.ID)
	out := map[string]any{"conversation": c, "messages": msgs, "todos": c.St.Todos, "context": c.St.LastContext, "summary": c.St.Summary, "checkpoints": ckpts}
	if run := s.ActiveRun(c.ID); run != nil {
		evs, st := run.Since(0)
		out["active_run"] = map[string]any{"id": run.ID, "state": st, "events": len(evs)}
	}
	return web.OK(w, out)
}

func (s *Service) hPatch(w http.ResponseWriter, r *http.Request) error {
	c, err := s.getConv(r.Context(), uid(r), chi.URLParam(r, "id"))
	if err != nil {
		return web.NotFound("会话")
	}
	var in struct {
		Title    *string       `json:"title"`
		Archived *bool         `json:"archived"`
		Cfg      *ConvSettings `json:"settings"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if in.Title != nil {
		c.Title = truncateRunes(strings.TrimSpace(*in.Title), 100)
	}
	if in.Archived != nil {
		if *in.Archived {
			now := model.Now()
			c.ArchivedAt = &now
		} else {
			c.ArchivedAt = nil
		}
	}
	if in.Cfg != nil {
		if in.Cfg.Model != "" {
			c.Cfg.Model = in.Cfg.Model
		}
		c.Cfg.Effort = in.Cfg.Effort
		switch in.Cfg.Mode {
		case ModePlan, ModeAskAll, ModeAskDestructive, ModeAuto:
			c.Cfg.Mode = in.Cfg.Mode
		}
	}
	if err := s.saveConv(r.Context(), c); err != nil {
		return err
	}
	return web.OK(w, map[string]any{"conversation": c})
}

func (s *Service) hDelete(w http.ResponseWriter, r *http.Request) error {
	c, err := s.getConv(r.Context(), uid(r), chi.URLParam(r, "id"))
	if err != nil {
		return web.NotFound("会话")
	}
	if run := s.ActiveRun(c.ID); run != nil {
		run.cancel()
	}
	_, err = s.st.Write(r.Context(), uid(r), func(tx *store.Tx) error {
		var ids []string
		if err := tx.Select(&ids, "SELECT id FROM agent_messages WHERE conversation_id = ?", c.ID); err != nil {
			return err
		}
		for _, id := range ids {
			if err := tx.Delete("agent_messages", id); err != nil {
				return err
			}
		}
		var cks []string
		if err := tx.Select(&cks, "SELECT id FROM agent_checkpoints WHERE conversation_id = ?", c.ID); err != nil {
			return err
		}
		for _, id := range cks {
			if err := tx.Delete("agent_checkpoints", id); err != nil {
				return err
			}
		}
		return tx.Delete("agent_conversations", c.ID)
	})
	if err != nil {
		return err
	}
	return web.OK(w, nil)
}

func (s *Service) hSend(w http.ResponseWriter, r *http.Request) error {
	var in SendInput
	if err := web.ReadJSONLimit(r, &in, 2<<20); err != nil {
		return err
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Refs) == 0 && len(in.Images) == 0 {
		return web.Bad("消息不能为空")
	}
	run, msg, err := s.Send(r.Context(), web.UserOf(r), chi.URLParam(r, "id"), web.ConnID(r), in)
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"run_id": run.ID, "message": msg})
}

// hEvents 以 SSE 推送运行事件；after 指定从哪个序号之后开始（断线重连）。
func (s *Service) hEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.GetRun(uid(r), chi.URLParam(r, "id"))
	if !ok {
		web.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "not_found", "message": "运行不存在或已结束"}})
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		if v, err := strconv.Atoi(le); err == nil {
			after = v
		}
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	notify, unsub := run.Subscribe()
	defer unsub()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		evs, state := run.Since(after)
		for _, ev := range evs {
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, b)
			after = ev.Seq
		}
		if len(evs) > 0 {
			fl.Flush()
		}
		if state == "done" || state == "error" || state == "stopped" {
			if len(evs) == 0 {
				return
			}
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-notify:
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Service) hStop(w http.ResponseWriter, r *http.Request) error {
	if err := s.Stop(uid(r), chi.URLParam(r, "id")); err != nil {
		return web.NotFound("运行")
	}
	return web.OK(w, nil)
}

func (s *Service) hRespond(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ID string `json:"id"`
		Reply
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := s.Respond(uid(r), chi.URLParam(r, "id"), in.ID, in.Reply); err != nil {
		return web.Bad(err.Error())
	}
	return web.OK(w, nil)
}

func (s *Service) hRevert(w http.ResponseWriter, r *http.Request) error {
	n, err := s.RevertCheckpoint(r.Context(), web.UserOf(r), chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return web.OK(w, map[string]any{"pages": n})
}

func (s *Service) hGetInstr(w http.ResponseWriter, r *http.Request) error {
	var v struct {
		Instructions string `json:"instructions"`
	}
	_, _ = s.settings.GetJSON(r.Context(), "user:"+uid(r)+":agent", &v)
	return web.OK(w, v)
}

func (s *Service) hPutInstr(w http.ResponseWriter, r *http.Request) error {
	var v struct {
		Instructions string `json:"instructions"`
	}
	if err := web.ReadJSONLimit(r, &v, 64<<10); err != nil {
		return err
	}
	if len([]rune(v.Instructions)) > 8000 {
		return web.Bad("指令不能超过 8000 字")
	}
	if err := s.settings.PutJSON(r.Context(), uid(r), "user:"+uid(r)+":agent", v); err != nil {
		return err
	}
	return web.OK(w, v)
}

func (s *Service) hAdminGet(w http.ResponseWriter, r *http.Request) error {
	st := s.ProviderStatus(r.Context())
	out := map[string]any{"provider": st, "default_model": s.settings.System(r.Context()).AgentDefaultModel}
	if st.HasKey {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if prov, err := s.Provider(ctx); err == nil {
			if ms, err := prov.Models(ctx); err == nil {
				out["models"] = ms
			} else {
				out["error"] = fmtErr(err)
			}
		}
	}
	return web.OK(w, out)
}

func (s *Service) hAdminPut(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		APIKey       string `json:"api_key"`
		Enabled      bool   `json:"enabled"`
		DefaultModel string `json:"default_model"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		return err
	}
	if err := s.SaveProvider(r.Context(), uid(r), strings.TrimSpace(in.APIKey), "", in.Enabled); err != nil {
		return err
	}
	sys := s.settings.System(r.Context())
	sys.AgentDefaultModel = in.DefaultModel
	if err := s.settings.SaveSystem(r.Context(), uid(r), sys); err != nil {
		return err
	}
	return s.hAdminGet(w, r)
}
