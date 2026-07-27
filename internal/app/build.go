package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/app/build"
	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/channel/cli"
	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/obs"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
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

	models, defaultRef, err := build.BuildModelRegistry(input.Config, llm.NewConfigProviderFactory(input.Config.Providers))
	if err != nil {
		return nil, fmt.Errorf("build model registry: %w", err)
	}
	resolvedDefault, err := models.Resolve(defaultRef)
	if err != nil {
		return nil, fmt.Errorf("resolve default model: %w", err)
	}
	store := session.NewFileStore(input.Config.Paths.SessionDir)
	registry, err := build.BuildSessionRegistry(defaultRef, models, store, input.SystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("build session registry: %w", err)
	}

	manager, err := build.BuildToolManager(input.Config.Paths, input.Config.Tools)
	if err != nil {
		return nil, fmt.Errorf("build tool manager: %w", err)
	}
	consoleReader := bufio.NewReader(input.CLI.Input)
	cliApprover, err := security.NewCLIApprover(consoleReader, input.CLI.ErrOutput)
	if err != nil {
		return nil, fmt.Errorf("build CLI approver: %w", err)
	}
	bashAnalyzer, err := security.NewConservativeBashAnalyzer(input.Config.Security.Mode)
	if err != nil {
		return nil, fmt.Errorf("build bash analyzer: %w", err)
	}
	policy, err := security.NewStaticPolicy(input.Config.Security.Mode, bashAnalyzer)
	if err != nil {
		return nil, fmt.Errorf("build tool policy: %w", err)
	}
	redactor, err := security.NewDefaultRedactor(input.Config.Security.MaxAuditArgBytes, input.Config.Security.MaxAuditOutBytes)
	if err != nil {
		return nil, fmt.Errorf("build audit redactor: %w", err)
	}
	auditSink, err := security.NewJSONLAuditSink(input.Config.Security.AuditPath)
	if err != nil {
		return nil, fmt.Errorf("build tool audit sink: %w", err)
	}
	keepAudit := false
	defer func() {
		if !keepAudit {
			_ = auditSink.Close()
		}
	}()
	executor, err := security.NewExecutor(security.ExecutorDependencies{Catalog: manager, Policy: policy, Approver: security.RoutingApprover{Interactive: cliApprover, Fallback: security.DenyAllApprover{}}, Audit: auditSink, Redactor: redactor}, input.Config.Security.ApprovalTimeout)
	if err != nil {
		return nil, fmt.Errorf("build secure tool executor: %w", err)
	}

	contextManager, err := build.BuildContextManager(
		input.Config.Context,
		resolvedDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("build context manager: %w", err)
	}

	commands, err := NewCommandRouter(CommandDependencies{
		Sessions: registry,
		Updater:  registry,
		Context:  contextManager,
		Models:   models,
	})
	if err != nil {
		return nil, fmt.Errorf("build command router: %w", err)
	}

	runner, err := agent.New(
		agent.Dependencies{
			Models:   models,
			Catalog:  manager,
			Executor: executor,
			Store:    store,
			Context:  contextManager,
			Logger:   logger,
			NewID:    uuid.NewString,
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
		Input:     consoleReader,
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
	runtime := &Runtime{
		Application: application,
		Logger:      logger,
		Channels:    []channel.Channel{cliChannel},
	}
	runtime.AddCloser(auditSink)
	keepAudit = true
	return runtime, nil
}
