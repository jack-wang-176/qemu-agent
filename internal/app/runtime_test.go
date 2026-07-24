package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
)

type fakeChannel struct {
	name    string
	err     error
	handler channel.Handler
}

func (c *fakeChannel) Name() string { return c.name }
func (c *fakeChannel) Run(_ context.Context, handler channel.Handler) error {
	c.handler = handler
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
		runtime := &Runtime{Application: application, Logger: logger, Channels: []channel.Channel{&fakeChannel{}, &fakeChannel{}}}
		if err := runtime.Run(context.Background()); err == nil {
			t.Fatal("error = nil")
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
