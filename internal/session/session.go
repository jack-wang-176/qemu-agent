package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type Session struct {
	ID         string        `json:"id"`
	TraceID    string        `json:"trace_id"`
	ModelRef   llm.ModelRef  `json:"model_ref"`
	Messages   []llm.Message `json:"messages"`
	TokenUsage int           `json:"token_usage"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

func NewSession(traceId, systemPrompt string, modelRef llm.ModelRef) *Session {
	sess := &Session{
		ID:         uuid.NewString(),
		TraceID:    traceId,
		TokenUsage: 0,
		ModelRef:   modelRef,
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

type sessionWire struct {
	ID          string        `json:"id"`
	TraceID     string        `json:"trace_id"`
	ModelRef    *llm.ModelRef `json:"model_ref,omitempty"`
	LegacyModel string        `json:"model,omitempty"`
	Messages    []llm.Message `json:"messages"`
	TokenUsage  int           `json:"token_usage"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

func (s Session) MarshalJSON() ([]byte, error) {
	ref, err := llm.NormalizeModelRef(s.ModelRef)
	if err != nil {
		return nil, fmt.Errorf("marshal session model: %w", err)
	}
	return json.Marshal(sessionWire{ID: s.ID, TraceID: s.TraceID, ModelRef: &ref, Messages: s.Messages, TokenUsage: s.TokenUsage, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt})
}

func (s *Session) UnmarshalJSON(data []byte) error {
	var wire sessionWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.ModelRef == nil && wire.LegacyModel == "" {
		return errors.New("session model is missing")
	}
	if wire.ModelRef != nil {
		s.ModelRef = *wire.ModelRef
		if wire.LegacyModel != "" && wire.LegacyModel != s.ModelRef.Model {
			return errors.New("session model_ref conflicts with legacy model")
		}
	} else {
		s.ModelRef = llm.ModelRef{Model: wire.LegacyModel}
	}
	s.ID, s.TraceID = wire.ID, wire.TraceID
	s.Messages, s.TokenUsage = wire.Messages, wire.TokenUsage
	s.CreatedAt, s.UpdatedAt = wire.CreatedAt, wire.UpdatedAt
	return nil
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
	return cloneMessages(s.Messages)
}

// ErrToolResultNotFound reports that no tool message carries the given call id.
// Compaction may legitimately drop old tool messages, so callers decide whether
// a missing target is fatal.
var ErrToolResultNotFound = errors.New("tool result not found")

// ReplaceToolResult rewrites the persisted text of one tool message. It is the
// only supported way to store a projection of a tool result: the message must
// exist exactly once, so a projection can never create, delete or duplicate a
// tool message and break the assistant/tool pairing the provider requires.
func (s *Session) ReplaceToolResult(callID, content string) error {
	if s == nil {
		return errors.New("session is nil")
	}
	if callID == "" {
		return errors.New("tool call id is empty")
	}
	if content == "" {
		return errors.New("tool result content is empty")
	}
	found := -1
	for index, message := range s.Messages {
		if message.Role != llm.RoleTool || message.ToolCallID != callID {
			continue
		}
		if found >= 0 {
			return fmt.Errorf("duplicate tool result for call %q", callID)
		}
		found = index
	}
	if found < 0 {
		return fmt.Errorf("%w: call %q", ErrToolResultNotFound, callID)
	}
	s.Messages[found].Content = content
	s.touch()
	return nil
}

func (s *Session) MessageReplace(msgs []llm.Message, usage int) {
	s.Messages = cloneMessages(msgs)
	s.TokenUsage = usage
	s.touch()
}

// Clone returns a deep copy suitable for request-scoped speculative updates.
func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Messages = cloneMessages(s.Messages)
	return &clone
}

// CanReplaceFrom validates that another value is the same persistent session.
// Agent runs may update messages, usage and timestamps, but not identity/model.
func (s *Session) CanReplaceFrom(other *Session) error {
	if s == nil || other == nil {
		return errors.New("session replacement is nil")
	}
	if s.ID != other.ID {
		return errors.New("session replacement changes id")
	}
	if s.TraceID != other.TraceID {
		return errors.New("session replacement changes trace id")
	}
	if s.ModelRef != other.ModelRef {
		return errors.New("session replacement changes model ref")
	}
	if !s.CreatedAt.Equal(other.CreatedAt) {
		return errors.New("session replacement changes creation time")
	}
	return nil
}

// ReplaceFrom commits a previously validated working copy to the live session.
func (s *Session) ReplaceFrom(other *Session) error {
	if err := s.CanReplaceFrom(other); err != nil {
		return err
	}
	s.Messages = cloneMessages(other.Messages)
	s.TokenUsage = other.TokenUsage
	s.UpdatedAt = other.UpdatedAt
	return nil
}

func cloneMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, len(messages))
	for index, message := range messages {
		result[index] = message
		result[index].ToolCalls = append([]llm.ToolCall(nil), message.ToolCalls...)
	}
	return result
}
