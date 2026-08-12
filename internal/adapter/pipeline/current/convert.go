package current

// convert.go — modeling ↔ pipelineapi 映射 helper (A3).
//
// 这一层只做值映射：
//   operation ↔ Stage
//   Project    ↔ EngineView
//   ArtifactRef ↔ ArtifactDescriptor
//   RunResult  ↔ ExecuteResult
//   StageEvent ↔ pipelineapi.Event（通过 eventAdapter）
//
// 不改 Stage 实现；不引入新业务逻辑。

import (
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// operationToStage 把 pipelineapi.OperationName 映射为 modeling.Stage。
//
// 当前 five operations 与 Stage 一一对应：
//
//	plan / extract / infer / emit / verify
//
// 空 operation 不允许进入 Execute —— 调用者应在 modelingapp 层
// 决定 "current/recommended" 后再调用 Engine。
func operationToStage(op pipelineapi.OperationName) (modeling.Stage, error) {
	s := strings.ToLower(string(op))
	switch s {
	case "plan":
		return modeling.StagePlan, nil
	case "extract":
		return modeling.StageExtract, nil
	case "infer":
		return modeling.StageInfer, nil
	case "emit":
		return modeling.StageEmit, nil
	case "verify":
		return modeling.StageVerify, nil
	default:
		return "", fmt.Errorf("current engine: unknown operation %q", op)
	}
}

// stageToOperation 是反向映射，用于把 Project.Current（Stage）暴露为 EngineView.CurrentOperation。
func stageToOperation(s modeling.Stage) pipelineapi.OperationName {
	return pipelineapi.OperationName(string(s))
}

// toModelingScope 把 pipelineapi.Scope 转为 modeling.Scope。
//
// pipelineapi.Scope 是 opaque workspace ID 字符串；
// modeling.Scope 是 {WorkspaceID, UserID} 元组。
// 当前 Adapter 用 Scope 字符串作为 WorkspaceID；UserID 暂留空（current Pipeline 不强校验 owner）。
func toModelingScope(s pipelineapi.Scope) modeling.Scope {
	return modeling.Scope{WorkspaceID: s.WorkspaceID, UserID: s.UserID}
}

// toEngineView 把 modeling.Project 映射为 pipelineapi.EngineView。
//
// 注意：EngineView 保留 Engine 内部语义（Stage enum、done 状态）；
// modelingapp 负责把 EngineView 映射为对外稳定的 modelingapi.ProjectView。
func toEngineView(p modeling.Project) pipelineapi.EngineView {
	return pipelineapi.EngineView{
		ProjectID:        pipelineapi.ProjectID(p.ID),
		Title:            p.Title,
		Revision:         p.Revision,
		Status:           projectStatusToEngine(p.Status),
		CurrentOperation: stageToOperation(p.Current),
		Artifacts:        refsToDescriptors(p),
		EvidenceCount:    len(p.Evidence),
		LastError:        p.LastError,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

func projectStatusToEngine(s modeling.Status) pipelineapi.ProjectStatus {
	switch s {
	case modeling.StatusPending:
		return pipelineapi.StatusPending
	case modeling.StatusRunning:
		return pipelineapi.StatusRunning
	case modeling.StatusBlocked:
		return pipelineapi.StatusBlocked
	case modeling.StatusDone:
		return pipelineapi.StatusDone
	default:
		return pipelineapi.StatusPending
	}
}

// refsToDescriptors 把 Project.Artifacts（map[Stage][]ArtifactRef）和
// Evidence 展平为单一 ArtifactDescriptor 列表。
//
// EngineView.Artifacts 不区分 stage —— 这是 Engine 视角的简化；
// 如需按 stage 分组，可由 modelingapp 在映射时处理。
func refsToDescriptors(p modeling.Project) []pipelineapi.ArtifactDescriptor {
	var out []pipelineapi.ArtifactDescriptor
	for stage, refs := range p.Artifacts {
		for _, r := range refs {
			out = append(out, refToDescriptor(r, stage))
		}
	}
	for _, r := range p.Evidence {
		out = append(out, refToDescriptor(r, modeling.StageVerify))
	}
	return out
}

func refToDescriptor(r modeling.ArtifactRef, stage modeling.Stage) pipelineapi.ArtifactDescriptor {
	return pipelineapi.ArtifactDescriptor{
		ID:        pipelineapi.ArtifactID(r.ID),
		Operation: stageToOperation(stage),
		Name:      r.Name,
		Kind:      string(r.Kind),
		Bytes:     r.Bytes,
		Digest:    r.Digest,
		CreatedAt: r.Created,
	}
}

// toExecuteResult 把 modeling.RunResult 映射为 pipelineapi.ExecuteResult。
func toExecuteResult(r modeling.RunResult) pipelineapi.ExecuteResult {
	return pipelineapi.ExecuteResult{
		Project:   toEngineView(r.Project),
		Operation: stageToOperation(r.Stage),
		Status:    runStatusToOperationStatus(r),
		Artifacts: refsFromRunResult(r),
		Evidence:  evidenceFromRunResult(r),
		Summary:   r.Summary,
		Blocked:   r.Blocked,
		Reason:    r.Reason,
	}
}

func runStatusToOperationStatus(r modeling.RunResult) pipelineapi.OperationStatus {
	if r.Blocked {
		return pipelineapi.OpBlocked
	}
	if r.Project.Status == modeling.StatusDone {
		return pipelineapi.OpSucceeded
	}
	if r.Project.Status == modeling.StatusBlocked && r.Project.LastError != "" {
		return pipelineapi.OpFailed
	}
	return pipelineapi.OpSucceeded
}

func refsFromRunResult(r modeling.RunResult) []pipelineapi.ArtifactDescriptor {
	out := make([]pipelineapi.ArtifactDescriptor, 0, len(r.Refs))
	for _, ref := range r.Refs {
		out = append(out, refToDescriptor(ref, r.Stage))
	}
	return out
}

func evidenceFromRunResult(r modeling.RunResult) []pipelineapi.ArtifactDescriptor {
	out := make([]pipelineapi.ArtifactDescriptor, 0, len(r.Project.Evidence))
	for _, ev := range r.Project.Evidence {
		out = append(out, refToDescriptor(ev, modeling.StageVerify))
	}
	return out
}

// sourcesToStrings 把 pipelineapi.SourceRef 列表转为 modeling.RunRequest.Sources 所需的 []string。
//
// 当前 Pipeline 把 sources 当作"逻辑 workspace path"列表使用；
// Kind 与 Digest 在 current Adapter 阶段暂不透传给 Pipeline。
func sourcesToStrings(srcs []pipelineapi.SourceRef) []string {
	if len(srcs) == 0 {
		return nil
	}
	out := make([]string, 0, len(srcs))
	for _, s := range srcs {
		// 仅 workspace_path 类型被 current Pipeline 识别。
		if s.Kind == "workspace_path" || s.Kind == "" {
			out = append(out, s.Value)
		}
	}
	return out
}
