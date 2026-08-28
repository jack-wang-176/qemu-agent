package pipelineapi

// engine.go - Contract for a replaceable pipeline engine.
//
// The engine contract contains pipeline operations only. It is independent of
// CLI commands, MCP requests, Agent tool calls, and the current modeling package.
// modelingapp bridges this internal contract to the stable modelingapi product API.

import (
	"context"
	"time"
)

// Scope is the trusted authorization tuple for repository and engine access.
// It is derived from modelingapi.CallContext and never from untrusted request DTOs.
type Scope struct {
	WorkspaceID string
	UserID      string
}

// InvocationContext carries trusted request correlation and caller metadata.
// It contains no paths, secrets, or transport-specific request objects.
type InvocationContext struct {
	RequestID   string
	TraceID     string
	SessionID   string
	SessionKey  string
	Channel     string
	Interactive bool
}

// ProjectID is an opaque project identifier.
type ProjectID string

// ArtifactID is an opaque artifact identifier.
type ArtifactID string

// OperationName is the name of a discoverable operation.
type OperationName string

// Engine is a replaceable pipeline engine.
//
// modelingapp executes concrete operations through Engine. The current adapter wraps
// modeling.Pipeline, while future pipelines implement the same interface.
type Engine interface {
	// Describe returns engine capabilities without requiring a concrete project.
	Describe(ctx context.Context, req DescribeRequest) (Description, error)

	// Inspect reads an existing project snapshot without executing an operation.
	Inspect(ctx context.Context, req InspectRequest) (EngineView, error)

	// Execute runs an operation for the provided project.
	// It returns artifacts, status changes, and execution details. Operation events
	// are published through ExecuteRequest.Ports.Event.
	Execute(ctx context.Context, req ExecuteRequest) (ExecuteResult, error)
}

// DescribeRequest is the input to Describe.
type DescribeRequest struct {
	Scope Scope
}

// Description is the result returned by Describe.
type Description struct {
	EngineName    string
	EngineVersion string
	APIVersion    string // For example, "v1"; aligned with modelingapi.Capabilities.
	Operations    []OperationDescriptor
	ArtifactKinds []string
	// Feature support is determined by the engine.
	SupportsApply    bool
	SupportsEvidence bool
	SupportsCancel   bool
	SupportsProgress bool
}

// OperationDescriptor describes a discoverable operation.
type OperationDescriptor struct {
	Name            OperationName
	DisplayName     string
	Description     string
	RequiresSources bool
	Mutating        bool
	MayBlock        bool
}

// InspectRequest is the input to Inspect.
type InspectRequest struct {
	Scope     Scope
	ProjectID ProjectID
	Revision  int // A non-positive value selects the newest revision.
}

// EngineView is the internal engine projection returned by Inspect.
// modelingapp maps it to the stable caller-facing modelingapi.ProjectView.
type EngineView struct {
	ProjectID        ProjectID
	Title            string
	Revision         int
	Status           ProjectStatus
	CurrentOperation OperationName
	Recommended      []OperationDescriptor
	Artifacts        []ArtifactDescriptor
	EvidenceCount    int
	LastError        string // Internal error category, never a public error message.
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ExecuteRequest is the input to Execute.
//
// The pipeline declares logical operations; it does not build CLI or MCP invocations.
// Runtime infrastructure is injected through Ports by modelingapp or an adapter.
type ExecuteRequest struct {
	Project          ProjectSnapshot
	Operation        OperationName
	Instruction      string
	Sources          []SourceRef
	ExpectedRevision int
	Invocation       InvocationContext
	Ports            RuntimePorts
}

// ProjectSnapshot identifies the revision from which Execute must start.
type ProjectSnapshot struct {
	ID       ProjectID
	Scope    Scope
	Revision int
}

// SourceRef identifies an external input and its optional digest.
type SourceRef struct {
	Kind   string
	Value  string
	Digest string
}

// ExecuteResult is the result returned by Execute.
type ExecuteResult struct {
	Project   EngineView
	Operation OperationName
	Status    OperationStatus
	Artifacts []ArtifactDescriptor
	Evidence  []ArtifactDescriptor
	Summary   string
	Blocked   bool
	Reason    string
	Apply     *ApplyOutcome
}

// ApplyOutcome preserves successful and partial source-tree landing results.
// A non-nil outcome may be returned together with an error.
type ApplyOutcome struct {
	Written  []string
	Skipped  []string
	Partial  bool
	Reason   string
	Evidence []ArtifactDescriptor
}

// ApplyPreview binds approval to a specific project revision and diff.
type ApplyPreview struct {
	ID              string
	ProjectID       ProjectID
	ProjectRevision int
	Diff            ArtifactDescriptor
	Files           []FileChange
	Summary         string
	ExpiresAt       time.Time
}

// FileChange describes one path-relative source-tree change.
type FileChange struct {
	Path      string
	Kind      string
	OldBytes  int64
	NewBytes  int64
	OldDigest string
	NewDigest string
}

// ProjectStatus is the engine-facing project state.
type ProjectStatus string

const (
	StatusPending ProjectStatus = "pending"
	StatusRunning ProjectStatus = "running"
	StatusBlocked ProjectStatus = "blocked"
	StatusDone    ProjectStatus = "done" // modelingapp maps this internal value to completed.
)

// OperationStatus is the outcome of one operation invocation.
type OperationStatus string

const (
	OpSucceeded OperationStatus = "succeeded"
	OpBlocked   OperationStatus = "blocked"
	OpFailed    OperationStatus = "failed"
)

// ArtifactDescriptor is the engine-facing, content-free artifact projection.
type ArtifactDescriptor struct {
	ID        ArtifactID
	Operation OperationName
	Name      string
	Kind      string
	Bytes     int64
	Digest    string
	CreatedAt time.Time
}
