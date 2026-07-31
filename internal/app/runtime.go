package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
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

type channelResult struct {
	name string
	err  error
}

// Run supervises all configured channels. A normal channel stop does not stop
// siblings; the first fatal error cancels the shared child context.
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
	if len(r.Channels) == 0 {
		return errors.New("runtime has no channels")
	}
	names := make(map[string]struct{}, len(r.Channels))
	for index, item := range r.Channels {
		if item == nil {
			return fmt.Errorf("runtime channel %d is nil", index)
		}
		name := strings.TrimSpace(item.Name())
		if name == "" {
			return fmt.Errorf("runtime channel %d name is empty", index)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("runtime channel name %q is duplicated", name)
		}
		names[name] = struct{}{}
	}
	if ctx.Err() != nil {
		return nil
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan channelResult, len(r.Channels))
	for _, item := range r.Channels {
		go r.runChannel(childCtx, item, results)
	}

	var fatal error
	for range r.Channels {
		result := <-results
		if result.err == nil || (errors.Is(result.err, context.Canceled) && childCtx.Err() != nil) {
			continue
		}
		fatal = errors.Join(fatal, fmt.Errorf("run %s channel: %w", result.name, result.err))
		cancel()
	}
	return fatal
}

func (r *Runtime) runChannel(ctx context.Context, item channel.Channel, results chan<- channelResult) {
	name := item.Name()
	r.Logger.InfoContext(ctx, "channel started", "channel", name)
	result := channelResult{name: name}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.err = fmt.Errorf("panic: %v", recovered)
		}
		results <- result
	}()
	result.err = item.Run(ctx, r.Application)
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
