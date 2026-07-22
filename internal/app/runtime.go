package app

import (
	"log/slog"
)

type Runtime struct {
	Application *Application
	Logger      *slog.Logger
}

func (r *Runtime) Close() error {
	return nil
}
