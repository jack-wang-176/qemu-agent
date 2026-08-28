package pipelineapi

// effect.go - Contract for controlled side effects.
//
// Engines declare logical effects and arguments. Adapters inject trusted caller
// identity and map the request to an audited executor.

import "context"

// Effect invokes a controlled side effect.
type Effect interface {
	Invoke(ctx context.Context, req EffectRequest) (EffectResult, error)
}

// EffectRequest declares one logical effect.
type EffectRequest struct {
	// Name is a logical effect name, not an executable path.
	Name string

	// Args contains structured effect arguments, normally encoded as JSON.
	Args []byte

	// Caller is trusted caller metadata supplied by modelingapp.
	Caller Caller
}

// Caller is the engine-facing caller identity. Adapters map it to their concrete
// security identity without exposing security package types here.
type Caller struct {
	WorkspaceID string
	UserID      string
	TraceID     string
}

// EffectResult is the domain outcome of one effect invocation.
type EffectResult struct {
	// Status is the effect outcome.
	Status EffectStatus

	// Output is structured internal output, normally encoded as JSON.
	Output []byte

	// Err is an internal failure description and must never be exposed directly.
	Err string
}

// EffectStatus describes an effect outcome.
type EffectStatus string

const (
	// EffectSucceeded means the effect completed successfully.
	EffectSucceeded EffectStatus = "succeeded"
	// EffectBlocked means the effect requires an external condition or approval.
	EffectBlocked EffectStatus = "blocked"
	// EffectFailed means the effect completed with a domain failure.
	EffectFailed EffectStatus = "failed"
)
