package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
)

type countingCloser struct {
	calls int
	err   error
}

func (c *countingCloser) Close() error {
	c.calls++
	return c.err
}

type fakeChannel struct {
	name          string
	err           error
	handler       channel.Handler
	waitForCancel bool
	started       chan struct{}
}

func (c *fakeChannel) Name() string { return c.name }
func (c *fakeChannel) Run(ctx context.Context, handler channel.Handler) error {
	c.handler = handler
	if c.started != nil {
		close(c.started)
	}
	if c.waitForCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.err
}

func TestRuntimeRun(t *testing.T) {
	application, _, _ := newTestApplication(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("one channel", func(t *testing.T) {
		transport := &fakeChannel{name: "fake"}
		runtime := &Runtime{Application: application, Logger: logger, Channels: []channel.Channel{transport}}
		if err := runtime.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if transport.handler != application {
			t.Fatal("runtime did not pass application as handler")
		}
	})

	t.Run("no channels", func(t *testing.T) {
		runtime := &Runtime{Application: application, Logger: logger}
		if err := runtime.Run(context.Background()); err == nil {
			t.Fatal("error = nil")
		}
	})

	t.Run("multiple channels", func(t *testing.T) {
		runtime := &Runtime{Application: application, Logger: logger, Channels: []channel.Channel{&fakeChannel{name: "one"}, &fakeChannel{name: "two"}}}
		if err := runtime.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("fatal cancels sibling", func(t *testing.T) {
		want := errors.New("transport failed")
		started := make(chan struct{})
		runtime := &Runtime{Application: application, Logger: logger, Channels: []channel.Channel{&fakeChannel{name: "fatal", err: want}, &fakeChannel{name: "waiting", waitForCancel: true, started: started}}}
		if err := runtime.Run(context.Background()); !errors.Is(err, want) {
			t.Fatalf("error=%v", err)
		}
		select {
		case <-started:
		default:
			t.Fatal("sibling did not start")
		}
	})

	t.Run("duplicate names", func(t *testing.T) {
		runtime := &Runtime{Application: application, Logger: logger, Channels: []channel.Channel{&fakeChannel{name: "same"}, &fakeChannel{name: "same"}}}
		if err := runtime.Run(context.Background()); err == nil {
			t.Fatal("error=nil")
		}
	})

	t.Run("channel error", func(t *testing.T) {
		want := errors.New("transport failed")
		runtime := &Runtime{Application: application, Logger: logger, Channels: []channel.Channel{&fakeChannel{name: "fake", err: want}}}
		if err := runtime.Run(context.Background()); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRuntimeCloseIsIdempotentAndJoinsErrors(t *testing.T) {
	first := &countingCloser{err: errors.New("first")}
	second := &countingCloser{err: errors.New("second")}
	runtime := &Runtime{}
	runtime.AddCloser(first)
	runtime.AddCloser(second)

	firstErr := runtime.Close()
	secondErr := runtime.Close()
	if firstErr == nil || secondErr == nil {
		t.Fatalf("first=%v second=%v", firstErr, secondErr)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("close calls first=%d second=%d", first.calls, second.calls)
	}
}
