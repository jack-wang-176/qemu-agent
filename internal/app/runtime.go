package app

import (
	"errors"
	"io"
	"log/slog"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
)

type Runtime struct {
	Application *Application
	Logger      *slog.Logger
	Channels    []channel.Channel
	closers     []io.Closer
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var result error
	for index := len(r.closers) - 1; index >= 0; index-- {
		result = errors.Join(result, r.closers[index].Close())
	}
	return result
}

// AddCloser registers a process-lifetime resource. Resources are closed in
// reverse order, allowing future builders to attach databases or exporters.
func (r *Runtime) AddCloser(closer io.Closer) {
	if closer != nil {
		r.closers = append(r.closers, closer)
	}
}
