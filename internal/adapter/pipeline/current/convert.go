package current

// convert.go - modeling <-> pipelineapi mapping helpers.
//
// This layer performs value mapping only:
//   operation   <-> Stage
//   Project     <-> EngineView
//   ArtifactRef <-> ArtifactDescriptor
//   RunResult   <-> ExecuteResult
//   StageEvent  <-> pipelineapi.Event (through eventAdapter)
//
// It does not change Stage implementations or introduce business logic.

import (
	"fmt"
	"sort"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// operationToStage maps a pipelineapi.OperationName to modeling.Stage.
//
// The current five operations map one-to-one to stages:
//
//	plan / extract / infer / emit / verify
//
// An empty operation must not reach Execute. The modelingapp layer must choose
// a current or recommended operation before calling the Engine.
func operationToStage(op pipelineapi.OperationName) (modeling.Stage, error) {
	switch string(op) {
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

// stageToOperation maps a modeling stage back to the operation exposed as
// EngineView.CurrentOperation.
func stageToOperation(s modeling.Stage) pipelineapi.OperationName {
	return pipelineapi.OperationName(string(s))
}

// toModelingScope maps the trusted pipeline scope to the current modeling scope.
// Both values preserve WorkspaceID and UserID; the adapter must not drop owner
// identity while crossing the current implementation boundary.
func toModelingScope(s pipelineapi.Scope) modeling.Scope {
	return modeling.Scope{WorkspaceID: s.WorkspaceID, UserID: s.UserID}
}

// toEngineView maps a modeling.Project to pipelineapi.EngineView.
//
// EngineView preserves engine-internal semantics such as the Stage enum and
// done state. modelingapp maps it to the stable modelingapi.ProjectView.
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

// refsToDescriptors flattens Project.Artifacts (map[Stage][]ArtifactRef) and
// Evidence into one ArtifactDescriptor list.
//
// EngineView.Artifacts does not group entries by stage; this is an engine-view
// simplification. modelingapp can regroup entries when needed.
func refsToDescriptors(p modeling.Project) []pipelineapi.ArtifactDescriptor {
	var out []pipelineapi.ArtifactDescriptor
	stages := append([]modeling.Stage(nil), modeling.StageOrder...)
	for stage := range p.Artifacts {
		if !containsStage(stages, stage) {
			stages = append(stages, stage)
		}
	}
	sort.Slice(stages, func(i, j int) bool {
		return stageOrderIndex(stages[i]) < stageOrderIndex(stages[j])
	})
	for _, stage := range stages {
		refs := p.Artifacts[stage]
		for _, r := range refs {
			out = append(out, refToDescriptor(r, stage))
		}
	}
	for _, r := range p.Evidence {
		out = append(out, refToDescriptor(r, r.Stage))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Operation != out[j].Operation {
			return stageOrderIndex(modeling.Stage(out[i].Operation)) < stageOrderIndex(modeling.Stage(out[j].Operation))
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func containsStage(stages []modeling.Stage, want modeling.Stage) bool {
	for _, stage := range stages {
		if stage == want {
			return true
		}
	}
	return false
}

func stageOrderIndex(stage modeling.Stage) int {
	for index, known := range modeling.StageOrder {
		if known == stage {
			return index
		}
	}
	return len(modeling.StageOrder)
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

// toExecuteResult maps a modeling.RunResult to pipelineapi.ExecuteResult.
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

// sourcesToStrings converts pipelineapi.SourceRef values to the []string
// expected by modeling.RunRequest.Sources.
//
// The current Pipeline treats sources as logical workspace paths. Kind and
// Digest are not forwarded to Pipeline at this adapter stage.
func sourcesToStrings(srcs []pipelineapi.SourceRef) ([]string, error) {
	if len(srcs) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(srcs))
	for _, s := range srcs {
		// The current Pipeline recognizes only workspace_path sources.
		if s.Kind != "workspace_path" {
			return nil, fmt.Errorf("current engine: unsupported source kind %q", s.Kind)
		}
		if s.Digest != "" {
			return nil, fmt.Errorf("current engine: source digest verification is unsupported")
		}
		out = append(out, s.Value)
	}
	return out, nil
}
