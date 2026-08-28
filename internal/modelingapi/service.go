package modelingapi

// service.go defines the stable product use-case contract.
//
// Design principles :
//   - Service is the stable product boundary for CLI, Agent tools, and MCP.
//   - Callers can create, inspect, advance, read artifacts, apply, and read evidence.
//   - Callers do not know how StageInput, stores, renderers, or the Pipeline work.
//   - A5 modelingapp implements this contract; A1 only declares it.
//   - Store paths, internal Project structs, StageInput, and Draft never escape.

import "context"

// Service is the stable product contract for modeling use cases.
//
// Workflow and future adapter mapping:
//
//	start         -> Create
//	inspect       -> Show
//	continue      -> Advance
//	read_artifact -> Show plus ReadArtifact
//	evidence      -> Evidence, then ReadArtifact when content is requested
//
// List, Reset, PlanApply, and Apply remain stable use cases for future trusted
// adapters. They are not exposed by the v1 production conversation workflow.
// Diff does not need a separate method because it is an artifact.
type Service interface {
	Capabilities(ctx context.Context, call CallContext) (Capabilities, error)
	Create(ctx context.Context, call CallContext, req CreateRequest) (ProjectView, error)
	List(ctx context.Context, call CallContext, req ListRequest) (ProjectPage, error)
	Show(ctx context.Context, call CallContext, req ShowRequest) (ProjectView, error)
	Advance(ctx context.Context, call CallContext, req AdvanceRequest) (OperationResult, error)
	Reset(ctx context.Context, call CallContext, req ResetRequest) (ProjectView, error)
	ReadArtifact(ctx context.Context, call CallContext, req ReadArtifactRequest) (ArtifactContent, error)
	PlanApply(ctx context.Context, call CallContext, req PlanApplyRequest) (ApplyPreview, error)
	Apply(ctx context.Context, call CallContext, req ApplyRequest) (OperationResult, error)
	Evidence(ctx context.Context, call CallContext, req EvidenceRequest) (EvidencePage, error)
}

// MutationKindOf reports whether a Service method mutates state.
//
// This helper describes the contract but does not enforce it. Adapters must call
// CallContext.Validate before dispatch so mutating methods require IdempotencyKey.
func MutationKindOf(method string) MutationKind {
	switch method {
	case "Capabilities", "List", "Show", "ReadArtifact", "PlanApply", "Evidence":
		return ReadOnly
	default:
		return Mutating
	}
}
