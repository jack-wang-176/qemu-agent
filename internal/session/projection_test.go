package session

import (
	"errors"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

func TestReplaceToolResult(t *testing.T) {
	newSession := func() *Session {
		s := NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})
		s.AddUser("hi")
		s.AddAssistant(llm.Message{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "use_skill", Args: `{}`}}})
		s.AddToolResult("full output", "call-1")
		return s
	}

	t.Run("rewrites the matching message only", func(t *testing.T) {
		s := newSession()
		if err := s.ReplaceToolResult("call-1", "receipt"); err != nil {
			t.Fatal(err)
		}
		if got := s.Messages[len(s.Messages)-1].Content; got != "receipt" {
			t.Fatalf("content = %q", got)
		}
		if len(s.Messages) != 4 {
			t.Fatalf("projection changed the message count: %d", len(s.Messages))
		}
	})

	t.Run("missing call id is reported as not found", func(t *testing.T) {
		s := newSession()
		err := s.ReplaceToolResult("call-2", "receipt")
		if !errors.Is(err, ErrToolResultNotFound) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("duplicate call id is rejected", func(t *testing.T) {
		s := newSession()
		s.AddToolResult("full output", "call-1")
		if err := s.ReplaceToolResult("call-1", "receipt"); err == nil {
			t.Fatal("err = nil")
		}
	})

	t.Run("empty content is rejected", func(t *testing.T) {
		s := newSession()
		if err := s.ReplaceToolResult("call-1", ""); err == nil {
			t.Fatal("err = nil")
		}
	})
}
