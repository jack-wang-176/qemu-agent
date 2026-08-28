package pipelineapi

// completion.go - Contract for model completion calls.
//
// Engines use this port instead of importing a provider registry or a concrete
// completer. The engine owns prompt construction and output validation.

import "context"

// Completion performs one model completion call.
type Completion interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error)
}

// CompletionRequest is the engine-produced model input.
type CompletionRequest struct {
	// Prompt is the prompt assembled by the engine.
	Prompt string

	// Schema is an optional JSON Schema string for structured output.
	Schema string

	// Sources records prompt inputs for internal auditing and events.
	Sources []SourceRef

	// MaxTokens bounds generated output tokens.
	MaxTokens int

	// Temperature controls sampling; zero requests deterministic sampling.
	Temperature float64
}

// CompletionResult is the raw result returned by the model provider.
type CompletionResult struct {
	// Content is the raw generated text.
	Content string

	// TokensUsed is the total prompt and completion token count when available.
	TokensUsed int

	// FinishReason describes why generation stopped.
	FinishReason string
}
