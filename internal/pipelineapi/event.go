package pipelineapi

// event.go - Contract for operation-level engine events.
//
// Engines publish operation events only. Adapters map them to CLI, Agent, MCP,
// or test transports. Event delivery is not an authoritative data channel.

import "context"

// EventPublisher publishes operation-level engine events.
type EventPublisher interface {
	Publish(ctx context.Context, evt Event) error
}

// EventKind identifies an operation lifecycle event.
type EventKind string

const (
	EventOperationStarted   EventKind = "operation_started"
	EventOperationProgress  EventKind = "operation_progress"
	EventOperationCompleted EventKind = "operation_completed"
)

// Progress describes bounded operation progress.
type Progress struct {
	Text    string // Bounded and free of control characters.
	Current int
	Total   int
}

// ResultSummary is the bounded payload of a successful completed event.
type ResultSummary struct {
	Status    OperationStatus
	Summary   string // Bounded summary; never artifact content.
	Artifacts []ArtifactDescriptor
	Evidence  []ArtifactDescriptor
}

// Event is one operation-level engine event. modelingapp maps it to the stable
// caller-facing modelingapi event contract.
type Event struct {
	Kind      EventKind
	ProjectID ProjectID
	Operation OperationName
	Progress  *Progress      // Progress events only.
	Result    *ResultSummary // Successful completed events only.
	Error     *EngineError   // Failed completed events only.
}

// EngineError is an internal error summary. modelingapp maps it to a safe public error.
type EngineError struct {
	Category string // Stable internal error category.
	Message  string // Internal diagnostic text; never exposed directly.
}
