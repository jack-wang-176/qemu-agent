package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
)

type Runtime struct {
	Application *Application
	Logger      *slog.Logger
	Channels    []channel.Channel
	closers     []io.Closer
	closeOnce   sync.Once
	closeErr    error
}

// Run starts the configured channel. I2 intentionally supports one channel;
// multi-channel coordination is introduced when another transport is added.
func (r *Runtime) Run(ctx context.Context) error {
	if r == nil {
		return errors.New("runtime is nil")
	}
	if r.Application == nil {
		return errors.New("runtime application is nil")
	}
	if r.Logger == nil {
		return errors.New("runtime logger is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch len(r.Channels) {
	case 0:
		return errors.New("runtime has no channels")
	case 1:
	default:
		return fmt.Errorf("runtime supports one channel, got %d", len(r.Channels))
	}
	selected := r.Channels[0]
	if selected == nil {
		return errors.New("runtime channel is nil")
	}
	r.Logger.InfoContext(ctx, "channel started", "channel", selected.Name())
	if err := selected.Run(ctx, r.Application); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("run %s channel: %w", selected.Name(), err)
	}
	return nil
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		for index := len(r.closers) - 1; index >= 0; index-- {
			if err := r.closers[index].Close(); err != nil {
				r.closeErr = errors.Join(r.closeErr, fmt.Errorf("close runtime resource %d: %w", index, err))
			}
		}
	})
	return r.closeErr
}

// AddCloser registers a process-lifetime resource. Resources are closed in
// reverse order, allowing future builders to attach databases or exporters.
func (r *Runtime) AddCloser(closer io.Closer) {
	if closer != nil {
		r.closers = append(r.closers, closer)
	}
}
