package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

// define out msg deal
type Renderer interface {
	Prompt(io.Writer, string) error
	// deal action.
	Reply(io.Writer, channel.Outbound) error
	// output err.
	Error(io.Writer, error) error
}

type RequestRenderer interface {
	runstream.EventSink
	Finish(context.Context) error
	StreamedText() bool
}

type EventRendererFactory interface {
	New(io.Writer) (RequestRenderer, error)
}

// bearer and concrete impletention of renderer
type TextRenderer struct{}

func NewTextRenderer() *TextRenderer {
	return &TextRenderer{}
}

func (*TextRenderer) New(writer io.Writer) (RequestRenderer, error) {
	if writer == nil {
		return nil, errors.New("CLI event writer is nil")
	}
	return &TextRequestRenderer{writer: writer}, nil
}

type TextRequestRenderer struct {
	writer io.Writer

	started      bool
	streamedText bool
	lineOpen     bool
	terminal     bool
	finished     bool
	sequence     uint64
}

func (r *TextRequestRenderer) Emit(ctx context.Context, event runstream.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.finished {
		return errors.New("CLI request renderer is finished")
	}
	if event.Sequence == 0 || event.Sequence <= r.sequence {
		return fmt.Errorf("CLI run event sequence %d is not increasing", event.Sequence)
	}
	r.sequence = event.Sequence

	switch event.Type {
	case runstream.EventRunStarted:
		if r.started {
			return errors.New("CLI request renderer received duplicate run_started")
		}
		r.started = true
	case runstream.EventTurnStarted:
		if !r.started || r.terminal {
			return errors.New("CLI request renderer received turn_started outside an active run")
		}
	case runstream.EventTextDelta:
		if !r.started || r.terminal {
			return errors.New("CLI request renderer received text_delta outside an active run")
		}
		if _, err := io.WriteString(r.writer, event.Text); err != nil {
			return fmt.Errorf("write CLI text delta: %w", err)
		}
		r.streamedText = true
		r.lineOpen = !strings.HasSuffix(event.Text, "\n")
	case runstream.EventToolStarted:
		if !r.started || r.terminal {
			return errors.New("CLI request renderer received tool_started outside an active run")
		}
		if err := r.ensureNewline(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(r.writer, "[tool] %s requested\n", event.ToolName); err != nil {
			return fmt.Errorf("write CLI tool request: %w", err)
		}
	case runstream.EventToolCompleted:
		if !r.started || r.terminal {
			return errors.New("CLI request renderer received tool_completed outside an active run")
		}
		if err := r.ensureNewline(); err != nil {
			return err
		}
		status := "completed"
		if event.ToolOK == nil || !*event.ToolOK {
			status = "failed"
		}
		if _, err := fmt.Fprintf(r.writer, "[tool] %s %s\n", event.ToolName, status); err != nil {
			return fmt.Errorf("write CLI tool result: %w", err)
		}
	case runstream.EventRunCompleted:
		if !r.started || r.terminal {
			return errors.New("CLI request renderer received invalid run_completed")
		}
		r.terminal = true
	case runstream.EventRunFailed:
		if !r.started || r.terminal {
			return errors.New("CLI request renderer received invalid run_failed")
		}
		if err := r.ensureNewline(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(r.writer, "[run] failed: %s\n", event.Summary); err != nil {
			return fmt.Errorf("write CLI run failure: %w", err)
		}
		r.terminal = true
	default:
		return fmt.Errorf("unsupported CLI run event %q", event.Type)
	}
	return nil
}

func (r *TextRequestRenderer) Finish(ctx context.Context) error {
	if r.finished {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ensureNewline(); err != nil {
		return err
	}
	r.finished = true
	return nil
}

func (r *TextRequestRenderer) StreamedText() bool { return r.streamedText }

func (r *TextRequestRenderer) ensureNewline() error {
	if !r.lineOpen {
		return nil
	}
	if _, err := io.WriteString(r.writer, "\n"); err != nil {
		return fmt.Errorf("finish CLI output line: %w", err)
	}
	r.lineOpen = false
	return nil
}

func (*TextRenderer) Prompt(w io.Writer, prompt string) error {
	if w == nil {
		return errors.New("CLI prompt writer is nil")
	}
	if prompt == "" {
		return nil
	}
	if _, err := io.WriteString(w, prompt); err != nil {
		return fmt.Errorf("write CLI prompt: %w", err)
	}
	return nil
}

func (*TextRenderer) Reply(w io.Writer, out channel.Outbound) error {
	if w == nil {
		return errors.New("CLI reply writer is nil")
	}
	switch out.Action {
	case channel.ActionExit:
		return nil
	case channel.ActionReply:
	default:
		return fmt.Errorf("unsupported CLI action %q", out.Action)
	}

	if out.Text == "" {
		return nil
	}
	text := out.Text
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if _, err := io.WriteString(w, text); err != nil {
		return fmt.Errorf("write CLI reply: %w", err)
	}
	return nil
}

func (*TextRenderer) Error(w io.Writer, rendErr error) error {
	if w == nil {
		return errors.New("CLI error writer is nil")
	}
	if rendErr == nil {
		return nil
	}
	if _, err := fmt.Fprintf(w, "error: %v\n", rendErr); err != nil {
		return fmt.Errorf("write CLI error: %w", err)
	}
	return nil
}
