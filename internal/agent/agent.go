package agent

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

/* config of model*/
type Config struct {
	Model    string
	MaxTurns int
	Stream   bool
}

/* simple dependencies injection struct*/
type Dependencies struct {
	Provider llm.Provider
	Tools    *tools.Manager
	Store    session.Store
	Context  *contextmgr.CompactorManager
}

/* generated all component together.*/
type Agent struct {
	provider llm.Provider
	tools    *tools.Manager
	ctxmgr   *contextmgr.CompactorManager
	store    session.Store
	model    string
	maxTurns int
	stream   bool
	logger   *slog.Logger
}

/* option deal extre config of agent. */
type Option func(*Agent) error

/* add slog to agent. */
func WithLogger(logger *slog.Logger) Option {
	return func(agent *Agent) error {
		if logger == nil {
			return errors.New("logger is nil")
		}
		agent.logger = logger
		return nil
	}
}

func New(deps Dependencies, cfg Config, options ...Option) (*Agent, error) {
	if deps.Provider == nil {
		return nil, errors.New("provider is nil")
	}
	if deps.Tools == nil {
		return nil, errors.New("tools manager is nil")
	}
	if deps.Store == nil {
		return nil, errors.New("session store is nil")
	}
	if deps.Context == nil {
		return nil, errors.New("context manager is nil")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("model is empty")
	}
	if cfg.MaxTurns <= 0 {
		return nil, errors.New("max turns must be positive")
	}

	result := &Agent{
		provider: deps.Provider,
		tools:    deps.Tools,
		store:    deps.Store,
		ctxmgr:   deps.Context,
		model:    cfg.Model,
		maxTurns: cfg.MaxTurns,
		stream:   cfg.Stream,
		logger: slog.New(
			slog.NewTextHandler(io.Discard, nil),
		),
	}

	for _, option := range options {
		if option == nil {
			return nil, errors.New("agent option is nil")
		}
		if err := option(result); err != nil {
			return nil, fmt.Errorf("apply agent option: %w", err)
		}
	}
	return result, nil
}
