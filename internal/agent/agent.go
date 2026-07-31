package agent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

// Config contains Agent loop behavior only. Model selection belongs to Session.
type Config struct {
	MaxTurns int
	Stream   bool
}

type ToolExecutor interface {
	Schemas() []llm.ToolSchema
	Execute(ctx context.Context, name, args string) (string, error)
}

type ContextManager interface {
	EnforceBudget(ctx context.Context, model string, msgs []llm.Message) ([]llm.Message, int, error)
}

// Dependencies contains runtime capabilities required by Agent.
type Dependencies struct {
	Provider llm.Provider
	Tools    ToolExecutor
	Store    session.Store
	Context  ContextManager
	Logger   *slog.Logger
}

type Agent struct {
	provider llm.Provider
	tools    ToolExecutor
	ctxmgr   ContextManager
	store    session.Store
	maxTurns int
	stream   bool
	logger   *slog.Logger
}

func New(deps Dependencies, cfg Config) (*Agent, error) {
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
	if deps.Logger == nil {
		return nil, errors.New("logger is nil")
	}
	if cfg.MaxTurns <= 0 {
		return nil, errors.New("max turns must be positive")
	}
	if cfg.Stream && !deps.Provider.Capability().Streaming {
		return nil, errors.New("configured provider does not support streaming")
	}

	return &Agent{
		provider: deps.Provider,
		tools:    deps.Tools,
		store:    deps.Store,
		ctxmgr:   deps.Context,
		maxTurns: cfg.MaxTurns,
		stream:   cfg.Stream,
		logger:   deps.Logger,
	}, nil
}
