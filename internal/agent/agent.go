package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// Config contains Agent loop behavior only. Model selection belongs to Session.
type Config struct {
	MaxTurns int
	Stream   bool
}

type ToolCatalog interface {
	Schemas() []llm.ToolSchema
}

type SecureToolExecutor interface {
	Execute(context.Context, security.Invocation) (security.Result, error)
}

type ContextManager interface {
	EnforceBudget(ctx context.Context, budget contextmgr.ModelBudget, msgs []llm.Message) ([]llm.Message, int, error)
}

// Dependencies contains runtime capabilities required by Agent.
type Dependencies struct {
	Models   llm.ModelResolver
	Catalog  ToolCatalog
	Executor SecureToolExecutor
	Store    session.Store
	Context  ContextManager
	Logger   *slog.Logger
	NewID    func() string
	Now      func() time.Time
}

type Agent struct {
	models   llm.ModelResolver
	catalog  ToolCatalog
	executor SecureToolExecutor
	ctxmgr   ContextManager
	store    session.Store
	maxTurns int
	stream   bool
	logger   *slog.Logger
	newID    func() string
	now      func() time.Time
}

func New(deps Dependencies, cfg Config) (*Agent, error) {
	if deps.Models == nil {
		return nil, errors.New("model resolver is nil")
	}
	if deps.Catalog == nil {
		return nil, errors.New("tool catalog is nil")
	}
	if deps.Executor == nil {
		return nil, errors.New("tool executor is nil")
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
	if deps.NewID == nil {
		return nil, errors.New("invocation id generator is nil")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}

	return &Agent{
		models:   deps.Models,
		catalog:  deps.Catalog,
		executor: deps.Executor,
		store:    deps.Store,
		ctxmgr:   deps.Context,
		maxTurns: cfg.MaxTurns,
		stream:   cfg.Stream,
		logger:   deps.Logger,
		newID:    deps.NewID,
		now:      deps.Now,
	}, nil
}
