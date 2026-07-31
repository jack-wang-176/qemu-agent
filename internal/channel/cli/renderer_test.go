package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTextRenderer(t *testing.T) {
	renderer := NewTextRenderer()

	t.Run("prompt", func(t *testing.T) {
		var output bytes.Buffer
		if err := renderer.Prompt(&output, "> "); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "> " {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("reply adds newline", func(t *testing.T) {
		var output bytes.Buffer
		err := renderer.Reply(&output, channel.Outbound{Text: "answer", Action: channel.ActionReply})
		if err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "answer\n" {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("reply preserves newline", func(t *testing.T) {
		var output bytes.Buffer
		err := renderer.Reply(&output, channel.Outbound{Text: "answer\n", Action: channel.ActionReply})
		if err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "answer\n" {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("exit is silent", func(t *testing.T) {
		var output bytes.Buffer
		if err := renderer.Reply(&output, channel.Outbound{Text: "ignored", Action: channel.ActionExit}); err != nil {
			t.Fatal(err)
		}
		if output.Len() != 0 {
			t.Fatalf("output = %q", output.String())
		}
	})

	t.Run("unknown action", func(t *testing.T) {
		if err := renderer.Reply(&bytes.Buffer{}, channel.Outbound{Action: "unknown"}); err == nil {
			t.Fatal("error = nil")
		}
	})

	t.Run("error", func(t *testing.T) {
		var output bytes.Buffer
		if err := renderer.Error(&output, errors.New("bad input")); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "error: bad input\n" {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("writer error", func(t *testing.T) {
		want := errors.New("write failed")
		if err := renderer.Prompt(failingWriter{err: want}, "> "); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
		if err := renderer.Reply(failingWriter{err: want}, channel.Outbound{Text: "x", Action: channel.ActionReply}); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
		if err := renderer.Error(failingWriter{err: want}, errors.New("x")); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestTextRequestRenderer(t *testing.T) {
	var output bytes.Buffer
	renderer, err := NewTextRenderer().New(&output)
	if err != nil {
		t.Fatal(err)
	}
	ok := true
	events := []runstream.Event{
		{Type: runstream.EventRunStarted, Sequence: 1},
		{Type: runstream.EventTurnStarted, Sequence: 2, Turn: 1},
		{Type: runstream.EventTextDelta, Sequence: 3, Turn: 1, Text: "reading"},
		{Type: runstream.EventToolStarted, Sequence: 4, Turn: 1, ToolCallID: "call", ToolName: "read"},
		{Type: runstream.EventToolCompleted, Sequence: 5, Turn: 1, ToolCallID: "call", ToolName: "read", ToolOK: &ok},
		{Type: runstream.EventTurnStarted, Sequence: 6, Turn: 2},
		{Type: runstream.EventTextDelta, Sequence: 7, Turn: 2, Text: "done"},
		{Type: runstream.EventRunCompleted, Sequence: 8},
	}
	for _, event := range events {
		if err := renderer.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := renderer.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "reading\n[tool] read requested\n[tool] read completed\ndone\n"; got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
	if !renderer.StreamedText() {
		t.Fatal("StreamedText()=false")
	}
	if err := renderer.Emit(context.Background(), runstream.Event{Type: runstream.EventRunStarted, Sequence: 9}); err == nil {
		t.Fatal("Emit after Finish error=nil")
	}
}
