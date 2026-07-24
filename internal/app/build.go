package app

import (
	"errors"
	"fmt"
	"io"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/app/build"
	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/channel/cli"
	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/obs"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type BuildInput struct {
	Config       config.Config
	SystemPrompt string
	LogOutput    io.Writer
	CLI          CLIAdapters
}

type CLIAdapters struct {
	Input     io.Reader
	Output    io.Writer
	ErrOutput io.Writer
}

func validateBuildInput(input BuildInput) error {
	if input.LogOutput == nil {
		return errors.New("build log output is nil")
	}
	if input.CLI.Input == nil {
		return errors.New("build CLI input is nil")
	}
	if input.CLI.Output == nil {
		return errors.New("build CLI output is nil")
	}
	if input.CLI.ErrOutput == nil {
		return errors.New("build CLI error output is nil")
	}
	return input.Config.Validate()
}

func Build(input BuildInput) (*Runtime, error) {
	if err := validateBuildInput(input); err != nil {
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
	registry, err := build.BuildSessionRegistry(input.Config.Agent, store, input.SystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("build session registry: %w", err)
	}

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

	commands, err := NewCommandRouter(CommandDependencies{
		Sessions: registry,
		Updater:  registry,
		Context:  contextManager,
	})
	if err != nil {
		return nil, fmt.Errorf("build command router: %w", err)
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

	application, err := NewApplication(Dependencies{
		Runner:   runner,
		Sessions: registry,
		Commands: commands,
		Logger:   logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build application: %w", err)
	}

	cliChannel, err := cli.NewCLI(cli.Dependencies{
		Input:     input.CLI.Input,
		Output:    input.CLI.Output,
		ErrOutput: input.CLI.ErrOutput,
		Renderer:  cli.NewTextRenderer(),
		Logger:    logger,
	}, cli.Config{
		Prompt:        input.Config.Channel.CLIPrompt,
		SessionKey:    input.Config.Channel.CLISessionKey,
		MaxInputBytes: input.Config.Channel.MaxInputBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("build CLI channel: %w", err)
	}

	logger.Info("application built", "config", input.Config.Summary())
	return &Runtime{
		Application: application,
		Logger:      logger,
		Channels:    []channel.Channel{cliChannel},
	}, nil
}
