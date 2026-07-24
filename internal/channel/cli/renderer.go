package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
)

// define out msg deal
type Renderer interface {
	Prompt(io.Writer, string) error
	// deal action.
	Reply(io.Writer, channel.Outbound) error
	// output err.
	Error(io.Writer, error) error
}

// bearer and concrete impletention of renderer
type TextRenderer struct{}

func NewTextRenderer() *TextRenderer {
	return &TextRenderer{}
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
