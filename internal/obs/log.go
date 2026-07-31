package obs

import (
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/config"
)

/* use logconfig to build slog instance. */
func NewLogger(cfg config.LogConfig, output io.Writer) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(cfg.Level))); err != nil {
		return nil, errors.New("invalid log level: " + cfg.Level)
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(output, opts)
	case "text":
		handler = slog.NewTextHandler(output, opts)
	default:
		return nil, errors.New("unsupported log format: " + cfg.Format + ", only support 'json' or 'text'")
	}

	logger := slog.New(handler)

	slog.SetDefault(logger)
	return logger, nil
}
