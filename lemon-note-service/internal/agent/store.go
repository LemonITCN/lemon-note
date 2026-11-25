package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

// Conversation 是会话及其解析后的设置。
type Conversation struct {
	*model.AgentConversation
	Cfg   ConvSettings `json:"settings"`
	St    ConvState    `json:"-"`
	Usage ConvUsage    `json:"usage"`
}

func decodeConv(c *model.AgentConversation) *Conversation {
	out := &Conversation{AgentConversation: c}
	_ = json.Unmarshal([]byte(c.Settings), &out.Cfg)
	_ = json.Unmarshal([]byte(c.State), &out.St)
	_ = json.Unmarshal([]byte(c.Usage), &out.Usage)
	if out.Cfg.Mode == "" {
		out.Cfg.Mode = ModeAskDestructive
	}
	return out
}

func (c *Conversation) encode() {
	b, _ := json.Marshal(c.Cfg)
	c.Settings = string(b)
	b, _ = json.Marshal(c.St)
	c.State = string(b)
	b, _ = json.Marshal(c.Usage)
	c.AgentConversation.Usage = string(b)
}

// Message 是解析后的消息。
type Message struct {
	ID        string       `json:"id"`
	Seq       int64        `json:"seq"`
	Role      string       `json:"role"`
	Parts     []StoredPart `json:"parts"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	CreatedAt int64        `json:"created_at"`
}

var ErrNoConv = errors.New("会话不存在")

func (s *Service) getConv(ctx context.Context, userID, id string) (*Conversation, error) {
	var c model.AgentConversation
	if err := s.st.Get(ctx, &c, "SELECT * FROM agent_conversations WHERE id = ? AND user_id = ?", id, userID); err != nil {
		return nil, ErrNoConv
	}
	return decodeConv(&c), nil
}

func (s *Service) saveConv(ctx context.Context, c *Conversation) error {
	c.encode()
	c.UpdatedAt = model.Now()
	_, err := s.st.Write(ctx, c.UserID, func(tx *store.Tx) error { return tx.Put("agent_conversations", c.AgentConversation) })
	return err
}

// CreateConversation 新建会话。
func (s *Service) CreateConversation(ctx context.Context, userID, spaceID string, cfg ConvSettings) (*Conversation, error) {
	now := model.Now()
	c := &Conversation{AgentConversation: &model.AgentConversation{ID: ids.New(), UserID: userID, SpaceID: model.StrPtr(spaceID), CreatedAt: now, UpdatedAt: now}, Cfg: cfg}
	if c.Cfg.Mode == "" {
		c.Cfg.Mode = ModeAskDestructive
	}
	c.encode()
	_, err := s.st.Write(ctx, userID, func(tx *store.Tx) error { return tx.Put("agent_conversations", c.AgentConversation) })
	return c, err
}

func (s *Service) loadMessages(ctx context.Context, convID string) ([]Message, error) {
	var rows []model.AgentMessage
	if err := s.st.Select(ctx, &rows, "SELECT * FROM agent_messages WHERE conversation_id = ? ORDER BY seq", convID); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		m := Message{ID: r.ID, Seq: r.Seq, Role: r.Role, CreatedAt: r.CreatedAt}
		_ = json.Unmarshal([]byte(r.Parts), &m.Parts)
		if r.Meta != "" && r.Meta != "{}" {
			m.Meta = json.RawMessage(r.Meta)
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) appendMessage(ctx context.Context, conv *Conversation, role string, parts []StoredPart, meta any) (*Message, error) {
	pb, _ := json.Marshal(parts)
	mb, _ := json.Marshal(meta)
	if meta == nil {
		mb = []byte("{}")
	}
	var msg *Message
	_, err := s.st.Write(ctx, conv.UserID, func(tx *store.Tx) error {
		var seq int64
		if err := tx.QueryRow("SELECT COALESCE(max(seq), 0) FROM agent_messages WHERE conversation_id = ?", conv.ID).Scan(&seq); err != nil {
			return err
		}
		row := model.AgentMessage{ID: ids.New(), ConversationID: conv.ID, Seq: seq + 1, Role: role, Parts: string(pb), Meta: string(mb), CreatedAt: model.Now()}
		msg = &Message{ID: row.ID, Seq: row.Seq, Role: role, Parts: parts, CreatedAt: row.CreatedAt}
		return tx.Put("agent_messages", row)
	})
	return msg, err
}

// autoTitle 用第一条用户消息生成会话标题。
func autoTitle(text string) string {
	t := strings.TrimSpace(strings.Split(text, "\n")[0])
	if utf8.RuneCountInString(t) > 30 {
		t = string([]rune(t)[:30]) + "…"
	}
	if t == "" {
		t = "新对话"
	}
	return t
}
