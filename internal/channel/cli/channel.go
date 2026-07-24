package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
)

type Dependencies struct {
	Input     io.Reader
	Output    io.Writer
	ErrOutput io.Writer
	Renderer  Renderer
	Logger    *slog.Logger
}

type Config struct {
	Prompt        string
	SessionKey    string
	MaxInputBytes int
}

type CLI struct {
	reader        *bufio.Reader
	output        io.Writer
	errOutput     io.Writer
	renderer      Renderer
	logger        *slog.Logger
	prompt        string
	sessionKey    string
	maxInputBytes int
}

func (*CLI) Name() string {
	return "cli"
}

func (c *CLI) Run(ctx context.Context, handler channel.Handler) error {
	if handler == nil {
		return errors.New("CLI handler is nil")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.renderer.Prompt(c.output, c.prompt); err != nil {
			return fmt.Errorf("render CLI prompt: %w", err)
		}

		line, err := c.readLine()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, ErrInputTooLarge):
			if renderErr := c.renderRecoverable(err); renderErr != nil {
				return renderErr
			}
			continue
		case err != nil:
			return fmt.Errorf("read CLI input: %w", err)
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		out, err := handler.Handle(ctx, channel.Inbound{
			SessionKey: c.sessionKey,
			Channel:    c.Name(),
			Text:       line,
		})

		if err != nil {
			if channel.IsRecoverable(err) {
				if renderErr := c.renderRecoverable(err); renderErr != nil {
					return renderErr
				}
				continue
			}
			return fmt.Errorf("handle CLI input: %w", err)
		}
		if out.SessionKey != "" && out.SessionKey != c.sessionKey {
			return fmt.Errorf(
				"CLI handler changed session key from %q to %q",
				c.sessionKey,
				out.SessionKey,
			)
		}
		if out.Action == channel.ActionExit {
			return nil
		}
		if err := c.renderer.Reply(c.output, out); err != nil {
			return fmt.Errorf("render CLI reply: %w", err)
		}
	}
}

func (c *CLI) renderRecoverable(err error) error {
	if renderErr := c.renderer.Error(c.errOutput, err); renderErr != nil {
		return fmt.Errorf("render recoverable CLI error: %w", renderErr)
	}
	c.logger.Debug("CLI request rejected", "err", err)
	return nil
}

func NewCLI(deps Dependencies, cfg Config) (*CLI, error) {
	if deps.Input == nil {
		return nil, errors.New("CLI input is nil")
	}
	if deps.Output == nil {
		return nil, errors.New("CLI output is nil")
	}
	if deps.ErrOutput == nil {
		return nil, errors.New("CLI error output is nil")
	}
	if deps.Renderer == nil {
		return nil, errors.New("CLI renderer is nil")
	}
	if deps.Logger == nil {
		return nil, errors.New("CLI logger is nil")
	}
	if strings.TrimSpace(cfg.SessionKey) == "" {
		return nil, errors.New("CLI session key is empty")
	}
	if cfg.Prompt == "" {
		return nil, errors.New("CLI prompt is empty")
	}
	if cfg.MaxInputBytes <= 0 {
		return nil, errors.New("CLI max input bytes must be > 0")
	}

	return &CLI{
		reader:        bufio.NewReader(deps.Input),
		output:        deps.Output,
		errOutput:     deps.ErrOutput,
		renderer:      deps.Renderer,
		logger:        deps.Logger,
		prompt:        cfg.Prompt,
		sessionKey:    cfg.SessionKey,
		maxInputBytes: cfg.MaxInputBytes,
	}, nil
}

var ErrInputTooLarge = errors.New("CLI input exceeds maximum size")

// read next line which not exceed min of maxinputBytes or 4096
// if encounter eof then return if bufferfull discard and return
// return string need delete suffix like /n /n/r.
func (c *CLI) readLine() (string, error) {
	buffer := make([]byte, 0, min(c.maxInputBytes, 4096))
	for {
		fragment, err := c.reader.ReadSlice('\n')
		if len(buffer)+len(fragment) > c.maxInputBytes {
			// err == nil means this fragment already consumed the newline.
			// ErrBufferFull means the oversized line still has unread bytes.
			if errors.Is(err, bufio.ErrBufferFull) {
				if discardErr := c.discardUntilNewline(); discardErr != nil {
					return "", discardErr
				}
			}
			return "", ErrInputTooLarge
		}
		buffer = append(buffer, fragment...)
		switch {
		case err == nil:
			return strings.TrimSuffix(strings.TrimSuffix(string(buffer), "\n"), "\r"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buffer) > 0:
			return strings.TrimSuffix(string(buffer), "\r"), nil
		default:
			return "", err
		}
	}
}

// check if next \n if next slice is intact
func (c *CLI) discardUntilNewline() error {
	for {
		_, err := c.reader.ReadSlice('\n')
		switch {
		case err == nil:
			return nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil
		default:
			return fmt.Errorf("discard oversized CLI input: %w", err)
		}
	}
}
