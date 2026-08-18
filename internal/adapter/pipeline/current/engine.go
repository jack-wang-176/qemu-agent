package current

// engine.go — current Engine Adapter.
//
// It adapts the existing modeling pipeline to pipelineapi.Engine.
// It does not change Stage implementations; it only maps operation to Stage,
// requests to RunRequest, projects/artifacts to stable descriptors, and
// StageEvent to pipelineapi.Event.
//
// Design rules:
//   - operation maps to one of the five current stages;
//   - pipelineapi requests map to modeling.RunRequest;
//   - current projects and artifacts map to stable descriptors;
//   - current StageEvent values map to pipelineapi.Event;
//   - Stage implementations remain unchanged, and adapted artifacts must be
//     identical to artifacts from direct Pipeline execution.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// Engine adapts the current modeling dependencies to pipelineapi.Engine.
type Engine struct {
	deps Dependencies
}

// NewEngine constructs a current Engine from its narrow modeling dependencies.
func NewEngine(deps Dependencies) (*Engine, error) {
	if deps.Engine == nil {
		return nil, mapCurrentError(errors.New("current engine: engine dependency is nil"))
	}
	return &Engine{deps: deps}, mapCurrentError(nil)
}

var _ pipelineapi.Engine = (*Engine)(nil)

// Describe returns the capabilities exposed by the current Pipeline.
func (e *Engine) Describe(ctx context.Context, req pipelineapi.DescribeRequest) (pipelineapi.Description, error) {
	// The current Pipeline exposes five operations, one for each Stage.
	ops := []pipelineapi.OperationDescriptor{
		{Name: "plan", DisplayName: "Plan", Description: "Read the request and skills, produce a modeling plan", RequiresSources: true, Mutating: true, MayBlock: false},
		{Name: "extract", DisplayName: "Extract", Description: "Pull the register IR out of a datasheet/header", RequiresSources: true, Mutating: true, MayBlock: false},
		{Name: "infer", DisplayName: "Infer", Description: "Fill in behaviour: reset values, side effects, IRQs", RequiresSources: false, Mutating: true, MayBlock: false},
		{Name: "emit", DisplayName: "Emit", Description: "Generate code into staging", RequiresSources: false, Mutating: true, MayBlock: true},
		{Name: "verify", DisplayName: "Verify", Description: "Build + qtest, produce Evidence", RequiresSources: false, Mutating: true, MayBlock: true},
	}
	return pipelineapi.Description{
		EngineName:       "current-pipeline",
		EngineVersion:    "1.0",
		APIVersion:       "v1",
		Operations:       ops,
		ArtifactKinds:    []string{"plan", "regir", "code", "diff", "evidence"},
		SupportsApply:    true,
		SupportsEvidence: true,
		SupportsCancel:   false,
		SupportsProgress: true,
	}, mapCurrentError(nil)
}

// Inspect reads the current snapshot of an existing project without executing
// an operation.
func (e *Engine) Inspect(ctx context.Context, req pipelineapi.InspectRequest) (pipelineapi.EngineView, error) {
	scope := toModelingScope(req.Scope)
	project, err := e.deps.Engine.Show(ctx, string(req.ProjectID), scope)
	if err != nil {
		return pipelineapi.EngineView{}, mapCurrentError(errors.Join(fmt.Errorf("current engine inspect: %w", err)))
	}
	return toEngineView(project), mapCurrentError(nil)
}

// Execute runs one operation for the supplied project snapshot.
func (e *Engine) Execute(ctx context.Context, req pipelineapi.ExecuteRequest) (pipelineapi.ExecuteResult, error) {
	if err := req.Validate(); err != nil {
		return pipelineapi.ExecuteResult{}, mapCurrentError(err)
	}
	stage, err := operationToStage(req.Operation)
	if err != nil {
		return pipelineapi.ExecuteResult{}, mapCurrentError(err)
	}
	scope := toModelingScope(req.Project.Scope)

	sources, err := sourcesToStrings(req.Sources)
	if err != nil {
		return pipelineapi.ExecuteResult{}, mapCurrentError(err)
	}
	runReq := modeling.RunRequest{
		ProjectID: string(req.Project.ID),
		Scope:     scope,
		Stage:     stage,
		Request:   req.Instruction,
		Sources:   sources,
		Events:    newEventAdapter(req.Ports.Event, req.Project.ID, req.Operation),
	}
	runResult, err := e.deps.Engine.Advance(ctx, runReq)
	if err != nil {
		return pipelineapi.ExecuteResult{}, mapCurrentError(errors.Join(fmt.Errorf("current engine execute: %w", err)))
	}

	return toExecuteResult(runResult), mapCurrentError(nil)
}
