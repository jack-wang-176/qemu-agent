package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
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
