package session

import (
	"time"

	"github.com/google/uuid"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type Session struct {
	ID         string        `json:"id"`
	TraceID    string        `json:"trace_id"`
	Model      string        `json:"model"`
	Messages   []llm.Message `json:"messages"`
	TokenUsage int           `json:"token_usage"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

func NewSession(traceId, systemPrompt, model string) *Session {
	sess := &Session{
		ID:         uuid.NewString(),
		TraceID:    traceId,
		TokenUsage: 0,
		Model:      model,
		Messages:   make([]llm.Message, 0),
		UpdatedAt:  time.Now(),
		CreatedAt:  time.Now(),
	}
	/* systemPrompt should be append as first message.*/
	if systemPrompt != "" {
		sess.Messages = append(sess.Messages, llm.Message{
			Role:    llm.RoleSystem,
			Content: systemPrompt,
		})
	}
	return sess
}

func (s *Session) touch() {
	s.UpdatedAt = time.Now()
}

func (s *Session) AddAssistant(msg llm.Message) {
	msg.Role = llm.RoleAssistant
	s.Messages = append(s.Messages, msg)
	s.touch()
}

func (s *Session) AddUser(text string) {
	s.Messages = append(s.Messages, llm.Message{
		Role:    llm.RoleUser,
		Content: text,
	})
	s.touch()
}

func (s *Session) AddToolResult(data, ID string) {
	s.Messages = append(s.Messages, llm.Message{
		Role:       llm.RoleTool,
		Content:    data,
		ToolCallID: ID,
	})
	s.touch()
}

/* copy and change was used for session refresh.*/
func (s *Session) MessageCopy() []llm.Message {
	return append([]llm.Message(nil), s.Messages...)
}

func (s *Session) MessageReplace(msgs []llm.Message, usage int) {
	s.Messages = append(s.Messages[:0], msgs...)
	s.TokenUsage = usage
	s.touch()
}
