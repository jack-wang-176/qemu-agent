package app

import (
	"fmt"
	"io"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/app/build"
	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/obs"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type BuildInput struct {
	Config       config.Config
	SystemPrompt string
	LogOutput    io.Writer
}

func Build(input BuildInput) (*Runtime, error) {
	if err := input.Config.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	logger, err := obs.NewLogger(
		input.Config.Log,
		input.LogOutput,
	)
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}

	provider, err := build.BuildProvider(input.Config)
	if err != nil {
		return nil, fmt.Errorf("build provider: %w", err)
	}

	store := session.NewFileStore(input.Config.Paths.SessionDir)

	manager, err := build.BuildToolManager(input.Config.Paths, input.Config.Tools)
	if err != nil {
		return nil, fmt.Errorf("build tool manager: %w", err)
	}

	contextManager, err := build.BuildContextManager(
		input.Config.Agent,
		input.Config.Context,
		provider,
	)
	if err != nil {
		return nil, fmt.Errorf("build context manager: %w", err)
	}

	runner, err := agent.New(
		agent.Dependencies{
			Provider: provider,
			Tools:    manager,
			Store:    store,
			Context:  contextManager,
			Logger:   logger,
		},
		agent.Config{
			MaxTurns: input.Config.Agent.MaxTurns,
			Stream:   input.Config.Agent.Stream,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}

	application, err := NewApplication(
		Dependencies{
			Runner: runner,
			Store:  store,
			Logger: logger,
		},
		Config{
			DefaultModel: input.Config.Agent.Model,
			SystemPrompt: input.SystemPrompt,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build application: %w", err)
	}

	logger.Info("application built", "config", input.Config.Summary())
	return &Runtime{
		Application: application,
		Logger:      logger,
	}, nil
}
