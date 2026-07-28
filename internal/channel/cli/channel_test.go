package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

type handlerFunc func(context.Context, channel.Request) (channel.Outbound, error)

func (fn handlerFunc) Handle(ctx context.Context, request channel.Request) (channel.Outbound, error) {
	return fn(ctx, request)
}

type recoverableTestError struct{ message string }

func (e recoverableTestError) Error() string   { return e.message }
func (recoverableTestError) Recoverable() bool { return true }

func newTestCLI(t *testing.T, input string, maxInputBytes int) (*CLI, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var output, errOutput bytes.Buffer
	client, err := NewCLI(Dependencies{
		Input:     strings.NewReader(input),
		Output:    &output,
		ErrOutput: &errOutput,
		Renderer:  NewTextRenderer(),
		Events:    NewTextRenderer(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, Config{Prompt: "> ", SessionKey: "cli:default", MaxInputBytes: maxInputBytes})
	if err != nil {
		t.Fatal(err)
	}
	return client, &output, &errOutput
}

func TestReadLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "line feed", input: "hello\n", want: "hello"},
		{name: "CRLF", input: "hello\r\n", want: "hello"},
		{name: "EOF final line", input: "hello", want: "hello"},
		{name: "larger than scanner default", input: strings.Repeat("x", 70*1024) + "\n", want: strings.Repeat("x", 70*1024)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _, _ := newTestCLI(t, test.input, 80*1024)
			got, err := client.readLine()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("line length = %d, want %d", len(got), len(test.want))
			}
		})
	}
}

func TestReadLineOversizedDiscardsOnlyCurrentLine(t *testing.T) {
	client, _, _ := newTestCLI(t, "123456789\nnext\n", 5)
	if _, err := client.readLine(); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("first error = %v", err)
	}
	line, err := client.readLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "next" {
		t.Fatalf("line = %q", line)
	}
}

func TestNewCLIRejectsInvalidDependenciesAndConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	validDeps := Dependencies{
		Input: strings.NewReader(""), Output: io.Discard, ErrOutput: io.Discard,
		Renderer: NewTextRenderer(), Events: NewTextRenderer(), Logger: logger,
	}
	validConfig := Config{Prompt: "> ", SessionKey: "cli:default", MaxInputBytes: 1}
	tests := []struct {
		name string
		deps Dependencies
		cfg  Config
	}{
		{name: "nil input", deps: Dependencies{Output: io.Discard, ErrOutput: io.Discard, Renderer: NewTextRenderer(), Events: NewTextRenderer(), Logger: logger}, cfg: validConfig},
		{name: "nil output", deps: Dependencies{Input: strings.NewReader(""), ErrOutput: io.Discard, Renderer: NewTextRenderer(), Events: NewTextRenderer(), Logger: logger}, cfg: validConfig},
		{name: "nil error output", deps: Dependencies{Input: strings.NewReader(""), Output: io.Discard, Renderer: NewTextRenderer(), Events: NewTextRenderer(), Logger: logger}, cfg: validConfig},
		{name: "nil renderer", deps: Dependencies{Input: strings.NewReader(""), Output: io.Discard, ErrOutput: io.Discard, Events: NewTextRenderer(), Logger: logger}, cfg: validConfig},
		{name: "nil events", deps: Dependencies{Input: strings.NewReader(""), Output: io.Discard, ErrOutput: io.Discard, Renderer: NewTextRenderer(), Logger: logger}, cfg: validConfig},
		{name: "nil logger", deps: Dependencies{Input: strings.NewReader(""), Output: io.Discard, ErrOutput: io.Discard, Renderer: NewTextRenderer(), Events: NewTextRenderer()}, cfg: validConfig},
		{name: "empty session key", deps: validDeps, cfg: Config{Prompt: "> ", MaxInputBytes: 1}},
		{name: "empty prompt", deps: validDeps, cfg: Config{SessionKey: "cli:default", MaxInputBytes: 1}},
		{name: "invalid size", deps: validDeps, cfg: Config{Prompt: "> ", SessionKey: "cli:default"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCLI(test.deps, test.cfg); err == nil {
				t.Fatal("error = nil")
			}
		})
	}
}

func TestCLIRunContinuesAfterRecoverableError(t *testing.T) {
	client, output, errOutput := newTestCLI(t, "bad\ngood\n/exit\n", 1024)
	calls := 0
	err := client.Run(context.Background(), handlerFunc(func(_ context.Context, request channel.Request) (channel.Outbound, error) {
		in := request.Inbound
		calls++
		switch in.Text {
		case "bad":
			return channel.Outbound{}, recoverableTestError{message: "try again"}
		case "good":
			return channel.Outbound{SessionKey: in.SessionKey, Text: "ok", Action: channel.ActionReply}, nil
		case "/exit":
			return channel.Outbound{SessionKey: in.SessionKey, Action: channel.ActionExit}, nil
		default:
			return channel.Outbound{}, errors.New("unexpected input")
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
	if !strings.Contains(output.String(), "ok\n") {
		t.Fatalf("output = %q", output.String())
	}
	if got := errOutput.String(); !strings.Contains(got, "try again") {
		t.Fatalf("error output = %q", got)
	}
}

func TestCLIRunReturnsFatalHandlerError(t *testing.T) {
	client, _, _ := newTestCLI(t, "hello\n", 1024)
	want := errors.New("provider failed")
	err := client.Run(context.Background(), handlerFunc(func(context.Context, channel.Request) (channel.Outbound, error) {
		return channel.Outbound{}, want
	}))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestCLIRunStreamsEventsWithoutRepeatingFinalReply(t *testing.T) {
	client, output, _ := newTestCLI(t, "hello\n", 1024)
	err := client.Run(context.Background(), handlerFunc(func(ctx context.Context, request channel.Request) (channel.Outbound, error) {
		if err := request.Events.Emit(ctx, runstream.Event{Type: runstream.EventRunStarted, Sequence: 1}); err != nil {
			return channel.Outbound{}, err
		}
		if err := request.Events.Emit(ctx, runstream.Event{Type: runstream.EventTurnStarted, Sequence: 2, Turn: 1}); err != nil {
			return channel.Outbound{}, err
		}
		if err := request.Events.Emit(ctx, runstream.Event{Type: runstream.EventTextDelta, Sequence: 3, Turn: 1, Text: "answer"}); err != nil {
			return channel.Outbound{}, err
		}
		if err := request.Events.Emit(ctx, runstream.Event{Type: runstream.EventRunCompleted, Sequence: 4}); err != nil {
			return channel.Outbound{}, err
		}
		return channel.Outbound{SessionKey: request.Inbound.SessionKey, Text: "answer", Action: channel.ActionExit}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(output.String(), "answer"); got != 1 {
		t.Fatalf("answer count=%d output=%q", got, output.String())
	}
}
