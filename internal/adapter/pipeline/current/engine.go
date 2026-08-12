package current

// engine.go — current Engine Adapter (A3).
//
// 把现有 modeling.Pipeline 包装为 pipelineapi.Engine。
// 不改 Stage 实现；只做 operation↔Stage、request↔RunRequest、
// Project/Artifact↔稳定 descriptor、StageEvent↔pipelineapi.Event 的映射。
//
// 设计原则（v1-04 第十部分 A3）：
//   - operation ↔ 当前 Stage（plan/extract/infer/emit/verify 五个 operation）；
//   - pipelineapi request ↔ modeling.RunRequest；
//   - 当前 Project/Artifact ↔ 稳定 descriptor；
//   - 当前 StageEvent ↔ pipelineapi.Event。
//   - 禁止改 Stage 实现；Gate：Adapter 调用产生的 Artifact 与直接调用
//     当前 Pipeline 完全相同。

import (
	"context"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// Engine 把 modeling.Pipeline 适配为 pipelineapi.Engine。
type Engine struct {
	pipe *modeling.Pipeline
}

// NewEngine 用现有 Pipeline 构造 current Engine Adapter。
func NewEngine(pipe *modeling.Pipeline) *Engine {
	return &Engine{pipe: pipe}
}

// Describe 返回 current Pipeline 的能力描述。
func (e *Engine) Describe(ctx context.Context, req pipelineapi.DescribeRequest) (pipelineapi.Description, error) {
	// 当前 Pipeline 暴露五个 operation（与 Stage 一一对应）。
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
	}, nil
}

// Inspect 读取一个已存在 Project 的当前快照，不执行 operation。
func (e *Engine) Inspect(ctx context.Context, req pipelineapi.InspectRequest) (pipelineapi.EngineView, error) {
	scope := toModelingScope(req.Scope)
	project, err := e.pipe.Show(ctx, string(req.ProjectID), scope)
	if err != nil {
		return pipelineapi.EngineView{}, fmt.Errorf("current engine inspect: %w", err)
	}
	return toEngineView(project), nil
}

// Execute 在给定 Project 上执行一次 operation。
func (e *Engine) Execute(ctx context.Context, req pipelineapi.ExecuteRequest) (pipelineapi.ExecuteResult, error) {
	if err := req.Validate(); err != nil {
		return pipelineapi.ExecuteResult{}, err
	}
	stage, err := operationToStage(req.Operation)
	if err != nil {
		return pipelineapi.ExecuteResult{}, err
	}
	scope := toModelingScope(req.Project.Scope)

	runReq := modeling.RunRequest{
		ProjectID: string(req.Project.ID),
		Scope:     scope,
		Stage:     stage,
		Request:   req.Instruction,
		Sources:   sourcesToStrings(req.Sources),
		Events:    newEventAdapter(req.Ports.Event, req.Project.ID, req.Operation),
	}
	runResult, err := e.pipe.Advance(ctx, runReq)
	if err != nil {
		return pipelineapi.ExecuteResult{}, fmt.Errorf("current engine execute: %w", err)
	}

	return toExecuteResult(runResult), nil
}
