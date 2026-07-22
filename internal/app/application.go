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
	defaultModel string
	systemPrompt string
	logger       *slog.Logger
}

func NewApplication(runner Runner, defaultModel string, systemPrompt string, logger *slog.Logger) (*Application, error) {
	if defaultModel == "" {
		return nil, fmt.Errorf("lack defaultModel for newapplication")
	}
	if systemPrompt == "" {
		return nil, fmt.Errorf("lack systemPrompt for newapplication")
	}
	if logger == nil {
		return nil, fmt.Errorf("lack logger for newapplication")
	}

	return &Application{
		runner:       runner,
		defaultModel: defaultModel,
		systemPrompt: systemPrompt,
		logger:       logger,
	}, nil
}

/*run a single time.*/
func (a *Application) RunOnce(ctx context.Context, sessionKey string, input string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(sessionKey) == "" {
		return "", errors.New("session key is empty")
	}
	if strings.TrimSpace(input) == "" {
		return "", errors.New("input is empty")
	}
	sess := session.NewSession(sessionKey, a.systemPrompt, a.defaultModel)
	answer, err := a.runner.Run(ctx, sess, input)
	if err != nil {
		return "", fmt.Errorf(" run agent for session key %q: %w", sessionKey, err)
	}
	return answer, nil
}
