package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/app/build"
	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/channel/cli"
	"github.com/jack-wang-176/qemu-agent/internal/channel/telegram"
	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/obs"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

type BuildInput struct {
	Config       config.Config
	SystemPrompt string
	LogOutput    io.Writer
	CLI          CLIAdapters
	HTTPClient   *http.Client
}

type CLIAdapters struct {
	Input     io.Reader
	Output    io.Writer
	ErrOutput io.Writer
}

// memoryTopK is the recall ceiling shared by the loop and /memory search, so a
// user browsing their notes sees the same window the model would have seen. The
// config value is only validated when memory is enabled, so a disabled install
// falls back to the default rather than to zero, which every consumer rejects.
func memoryTopK(cfg config.Config) int {
	if cfg.Memory.TopK > 0 {
		return cfg.Memory.TopK
	}
	return config.DefaultMemoryTopK
}

// defaultCompleterMaxTokens is the output budget for a background completion when
// the model definition does not state one. It is generous because the largest
// thing a background call produces is a Reg-IR for a register-heavy device, and a
// truncated JSON reply fails schema validation rather than degrading gracefully.
const defaultCompleterMaxTokens = 8192

// completerBudget picks the output ceiling for a non-conversational model call.
//
// ModelDefinition.MaxOutput is optional: zero means "no explicit limit, let the
// provider apply its own default", and the agent loop passes it straight through
// on that understanding. A Completer cannot do the same — it requires a positive
// budget — so an unset definition needs an explicit value here rather than an
// error at startup, which is what a plain pass-through produced.
func completerBudget(resolved llm.ResolvedModel) int {
	if resolved.Definition.MaxOutput > 0 {
		return resolved.Definition.MaxOutput
	}
	// Stay under the context window when it is small enough for 8192 to be an
	// unreasonable share of it; a completion cannot exceed the window anyway.
	if context := resolved.Definition.MaxContext; context > 0 && context <= defaultCompleterMaxTokens {
		return context / 2
	}
	return defaultCompleterMaxTokens
}

func validateBuildInput(input BuildInput) error {
	if input.LogOutput == nil {
		return errors.New("build log output is nil")
	}
	if input.Config.Channel.CLIEnabled && input.CLI.Input == nil {
		return errors.New("build CLI input is nil")
	}
	if input.Config.Channel.CLIEnabled && input.CLI.Output == nil {
		return errors.New("build CLI output is nil")
	}
	if input.Config.Channel.CLIEnabled && input.CLI.ErrOutput == nil {
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
	// Skills are scanned before the executor exists so a broken skill file or a
	// skill requiring a tool this build does not register fails startup instead
	// of a live turn.
	skillRegistry, err := build.BuildSkillRegistry(input.Config.Skills)
	if err != nil {
		return nil, fmt.Errorf("build skill registry: %w", err)
	}
	if err := build.RegisterSkillTool(manager, skillRegistry, input.Config.Skills); err != nil {
		return nil, fmt.Errorf("build skill tool: %w", err)
	}
	var consoleReader *bufio.Reader
	var interactiveApprover security.Approver = security.DenyAllApprover{}
	if input.Config.Channel.CLIEnabled {
		consoleReader = bufio.NewReader(input.CLI.Input)
		cliApprover, err := security.NewCLIApprover(consoleReader, input.CLI.ErrOutput)
		if err != nil {
			return nil, fmt.Errorf("build CLI approver: %w", err)
		}
		interactiveApprover = cliApprover
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
	approver := security.RoutingApprover{Interactive: interactiveApprover, Fallback: security.DenyAllApprover{}}
	executor, err := security.NewExecutor(security.ExecutorDependencies{Catalog: manager, Policy: policy, Approver: approver, Audit: auditSink, Redactor: redactor}, input.Config.Security.ApprovalTimeout)
	if err != nil {
		return nil, fmt.Errorf("build secure tool executor: %w", err)
	}

	// Modeling needs two tool roots, not one, because its two halves reach into two
	// different trees:
	//
	//   - the pipeline reads datasheets, which the operator drops in Paths.Workspace,
	//     and shells out to ninja with an absolute `cd <BuildDir>`. A workspace root
	//     serves both: the bash tool only sets the child's working directory, it does
	//     not confine the command, so verify reaches the build tree regardless.
	//   - the applier writes hw/... files, which only exist under QemuRoot.
	//
	// Handing the pipeline the QEMU-rooted catalog would make extract refuse every
	// datasheet; handing the applier the workspace-rooted one would make it write
	// device code into the workspace. So the agent's own executor — already rooted at
	// Paths.Workspace — is reused for the pipeline, and only the applier gets a new
	// one. Both share the policy, the approver, the redactor and the audit sink, so a
	// write into QEMU is judged and logged exactly like any other write.
	//
	// The applier's executor stays nil when QemuRoot is unset; BuildModeling turns
	// that into a disabled applier while leaving the pipeline usable.
	var modelingExecutor build.ToolExecutor
	var applyExecutor build.ToolExecutor
	if input.Config.Modeling.Enabled {
		modelingExecutor = executor
		if root := strings.TrimSpace(input.Config.Modeling.QemuRoot); root != "" {
			modelingManager, err := build.BuildModelingToolManager(root, input.Config.Tools)
			if err != nil {
				return nil, fmt.Errorf("build modeling tool manager: %w", err)
			}
			created, err := security.NewExecutor(security.ExecutorDependencies{Catalog: modelingManager, Policy: policy, Approver: approver, Audit: auditSink, Redactor: redactor}, input.Config.Security.ApprovalTimeout)
			if err != nil {
				return nil, fmt.Errorf("build modeling tool executor: %w", err)
			}
			applyExecutor = created
		}
	}

	contextManager, err := build.BuildContextManager(
		input.Config.Context,
		resolvedDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("build context manager: %w", err)
	}

	// The knowledge layer is assembled once here. Everything it returns is
	// non-nil even when memory is disabled, so neither the agent loop nor a
	// command has to branch on configuration at request time.
	var completer memory.Completer
	if input.Config.Memory.Enabled && input.Config.Memory.AutoExtract {
		providerCompleter, err := build.NewProviderCompleter(resolvedDefault, completerBudget(resolvedDefault))
		if err != nil {
			return nil, fmt.Errorf("build extraction completer: %w", err)
		}
		completer = providerCompleter
	}
	knowledge, err := build.BuildKnowledge(build.KnowledgeInput{
		Config:    input.Config,
		Skills:    skillRegistry,
		Completer: completer,
		Logger:    logger,
		NewID:     uuid.NewString,
	})
	if err != nil {
		return nil, fmt.Errorf("build knowledge layer: %w", err)
	}

	// The modeling stages get their own completer, resolved from Modeling.Model when
	// the operator set one. Modeling work is long, and its cost profile differs from
	// a chat turn, so an operator may want a different model for it than for the
	// conversation; an empty value reuses the session default. It is built only when
	// modeling is enabled, so a disabled build performs no model resolution at all.
	var modelingCompleter modeling.Completer
	if input.Config.Modeling.Enabled {
		resolvedModeling := resolvedDefault
		if name := strings.TrimSpace(input.Config.Modeling.Model); name != "" {
			resolved, err := models.ResolveName(name)
			if err != nil {
				return nil, fmt.Errorf("resolve modeling model: %w", err)
			}
			resolvedModeling = resolved
		}
		providerCompleter, err := build.NewProviderCompleter(resolvedModeling, completerBudget(resolvedModeling))
		if err != nil {
			return nil, fmt.Errorf("build modeling completer: %w", err)
		}
		modelingCompleter = providerCompleter
	}

	// The modeling layer, like the knowledge layer, returns non-nil implementations
	// even when disabled: with QEMU_AGENT_MODELING_ENABLED unset the router still
	// gets a working pair that answers "modeling is disabled", so /modeling exists
	// and explains itself rather than being absent.
	modelingLayer, err := build.BuildModeling(build.ModelingInput{
		Config:        input.Config,
		Logger:        logger,
		Executor:      modelingExecutor,
		ApplyExecutor: applyExecutor,
		Completer:     modelingCompleter,
	})
	if err != nil {
		return nil, fmt.Errorf("build modeling layer: %w", err)
	}

	commands, err := NewCommandRouter(CommandDependencies{
		Sessions:   registry,
		Updater:    registry,
		Context:    contextManager,
		Models:     models,
		Skills:     skillRegistry,
		Memories:   knowledge.Store,
		Candidates: knowledge.Candidates,
		Modeling:   modelingLayer.Runner,
		Apply:      modelingLayer.Applier,
	}, CommandConfig{MemoryTopK: memoryTopK(input.Config)})
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
			Prompts:  knowledge.Assembler,
			Logger:   logger,
			NewID:    uuid.NewString,
		},
		agent.Config{
			MaxTurns:             input.Config.Agent.MaxTurns,
			Stream:               input.Config.Agent.Stream,
			MemoryTopK:           memoryTopK(input.Config),
			PromptReservedTokens: input.Config.Prompt.ReservedContextTokens,
			PromptMaxBytes:       input.Config.Prompt.MaxInjectedBytes,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}

	application, err := NewApplication(Dependencies{
		Runner:      runner,
		Sessions:    registry,
		Commands:    commands,
		Extractor:   knowledge.Extractor,
		Candidates:  knowledge.Candidates,
		WorkspaceID: knowledge.WorkspaceID,
		Logger:      logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build application: %w", err)
	}

	channels := make([]channel.Channel, 0, 2)
	if input.Config.Channel.CLIEnabled {
		cliChannel, err := cli.NewCLI(cli.Dependencies{
			Input: consoleReader, Output: input.CLI.Output, ErrOutput: input.CLI.ErrOutput,
			Renderer: cli.NewTextRenderer(), Events: cli.NewTextRenderer(), Logger: logger,
		}, cli.Config{Prompt: input.Config.Channel.CLIPrompt, SessionKey: input.Config.Channel.CLISessionKey, MaxInputBytes: input.Config.Channel.MaxInputBytes})
		if err != nil {
			return nil, fmt.Errorf("build CLI channel: %w", err)
		}
		channels = append(channels, cliChannel)
	}
	if input.Config.Channel.Telegram.Enabled {
		tgCfg := input.Config.Channel.Telegram
		client, err := telegram.NewHTTPClient(tgCfg.Token, input.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("build Telegram client: %w", err)
		}
		factory, err := telegram.NewEventSinkFactory(client, telegram.SinkConfig{EditInterval: tgCfg.EditInterval, ChunkSize: tgCfg.MessageChunkSize})
		if err != nil {
			return nil, fmt.Errorf("build Telegram event sink: %w", err)
		}
		tgChannel, err := telegram.New(telegram.Dependencies{Client: client, Events: factory, Logger: logger}, telegram.Config{
			AllowedUserIDs: tgCfg.AllowedUserIDs, AllowGroupChats: tgCfg.AllowGroupChats,
			PollTimeout: tgCfg.PollTimeout, RetryMinBackoff: tgCfg.RetryMinBackoff, RetryMaxBackoff: tgCfg.RetryMaxBackoff,
			MaxConcurrency: tgCfg.MaxConcurrency, MaxInputBytes: tgCfg.MaxInputBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("build Telegram channel: %w", err)
		}
		channels = append(channels, tgChannel)
	}

	logger.Info("application built", "config", input.Config.Summary())
	logger.Info("skills loaded", "enabled", input.Config.Skills.Enabled, "count", skillRegistry.Len())

	runtime := &Runtime{
		Application: application,
		Logger:      logger,
		Channels:    channels,
	}
	runtime.AddCloser(auditSink)
	keepAudit = true
	return runtime, nil
}
