package build

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/modelingagent"
	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/modelingworkflow"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type ModelingModelResolver interface {
	llm.ModelResolver
	ResolveName(string) (llm.ResolvedModel, error)
}

type ModelingWorkflowInput struct {
	Config       config.Config
	Modeling     modelingapi.Service
	Models       ModelingModelResolver
	DefaultModel llm.ModelRef
	Context      modelingagent.ContextManager
	Prompts      modelingagent.PromptAssembler
	Store        session.Store
	Logger       *slog.Logger
	NewID        func() string
	Now          func() time.Time
}

type ModelingWorkflowComponents struct {
	Workflow modelingworkflow.Service
	Runner   *modelingagent.Runner
}

// BuildModelingWorkflow closes the professional modeling object graph without
// installing it into Application. The production entry-point switch remains a
// separate composition-root change.
func BuildModelingWorkflow(in ModelingWorkflowInput) (ModelingWorkflowComponents, error) {
	if err := in.Config.ValidateProfessionalModelingV1(); err != nil {
		return ModelingWorkflowComponents{}, fmt.Errorf("build modeling workflow config: %w", err)
	}
	switch {
	case in.Modeling == nil:
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: modeling service is nil")
	case in.Models == nil:
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: model resolver is nil")
	case in.Context == nil:
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: context manager is nil")
	case in.Prompts == nil:
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: prompt assembler is nil")
	case in.Store == nil:
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: session store is nil")
	case in.Logger == nil:
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: logger is nil")
	}

	resolved, err := resolveModelingModel(in)
	if err != nil {
		return ModelingWorkflowComponents{}, fmt.Errorf("build modeling workflow model: %w", err)
	}
	if resolved.Definition.MaxContext <= 0 {
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: model max context must be positive")
	}
	if resolved.Provider == nil {
		return ModelingWorkflowComponents{}, errors.New("build modeling workflow: resolved model provider is nil")
	}

	binding, err := modelingworkflow.NewFileStore(filepath.Join(in.Config.Modeling.Dir, "bindings"))
	if err != nil {
		return ModelingWorkflowComponents{}, fmt.Errorf("build modeling workflow binding store: %w", err)
	}
	now := in.Now
	if now == nil {
		now = time.Now
	}
	newID := in.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	dialogue := modelingagent.NewDialogue(modelingagent.DialogueConfig{
		Resolver: in.Models, Context: in.Context, Prompts: in.Prompts,
		Model: resolved.Definition.Ref, MaxBytes: in.Config.Prompt.MaxInjectedBytes,
		MemoryTopK: in.Config.Memory.TopK, Now: now,
	})
	presenter := modelingworkflow.NewTextPresenter()
	controller, err := modelingworkflow.NewController(modelingworkflow.Dependencies{
		Modeling: in.Modeling, Binding: binding, Interpreter: &dialogue,
		Presenter: presenter, NewID: newID, Now: now, Logger: in.Logger,
	}, modelingworkflow.Config{})
	if err != nil {
		return ModelingWorkflowComponents{}, fmt.Errorf("build modeling workflow controller: %w", err)
	}
	runner := modelingagent.NewRunner(modelingagent.Dependencies{
		Workflow: controller, Store: in.Store, Context: in.Context,
		Logger: in.Logger, NewID: newID, Now: now,
		Model: resolved.Definition.Ref, MaxContext: resolved.Definition.MaxContext,
	})
	return ModelingWorkflowComponents{Workflow: controller, Runner: &runner}, nil
}

func resolveModelingModel(in ModelingWorkflowInput) (llm.ResolvedModel, error) {
	if name := strings.TrimSpace(in.Config.Modeling.Model); name != "" {
		return in.Models.ResolveName(name)
	}
	return in.Models.Resolve(in.DefaultModel)
}
