package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type Runner interface {
	Run(ctx context.Context, s *session.Session, input string) (string, error)
}

type Application struct {
	runner       Runner
	store        session.Store
	defaultModel string
	systemPrompt string
	logger       *slog.Logger
}

type Dependencies struct {
	Runner Runner
	Store  session.Store
	Logger *slog.Logger
}

type Config struct {
	DefaultModel string
	SystemPrompt string
}

func NewApplication(deps Dependencies, cfg Config) (*Application, error) {
	if deps.Runner == nil {
		return nil, errors.New("application runner is nil")
	}
	if deps.Store == nil {
		return nil, errors.New("application session store is nil")
	}
	if deps.Logger == nil {
		return nil, errors.New("application logger is nil")
	}
	if strings.TrimSpace(cfg.DefaultModel) == "" {
		return nil, errors.New("application default model is empty")
	}

	return &Application{
		runner:       deps.Runner,
		store:        deps.Store,
		defaultModel: cfg.DefaultModel,
		systemPrompt: cfg.SystemPrompt,
		logger:       deps.Logger,
	}, nil
}

// RunOnce creates and executes one independent session.
func (a *Application) RunOnce(ctx context.Context, traceID string, input string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(traceID) == "" {
		return "", errors.New("trace id is empty")
	}
	if strings.TrimSpace(input) == "" {
		return "", errors.New("input is empty")
	}
	sess := session.NewSession(traceID, a.systemPrompt, a.defaultModel)
	if err := a.store.Save(ctx, sess); err != nil {
		return "", fmt.Errorf("save initial session: %w", err)
	}
	a.logger.DebugContext(ctx, "run one-shot session", "session_id", sess.ID, "trace_id", traceID)
	answer, err := a.runner.Run(ctx, sess, input)
	if err != nil {
		return "", fmt.Errorf("run agent for trace %q: %w", traceID, err)
	}
	return answer, nil
}
