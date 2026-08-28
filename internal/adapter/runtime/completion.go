package runtime

// completion.go — Completion Port Adapter (A4).
//
// Wraps the existing modeling.Completer (or an llm single-shot call) as
// pipelineapi.Completion. Stage prompts and output schemas remain unchanged in
// this first phase; later pipeline work may add profiles, memory/skill
// projections, or a different structured-output strategy.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// CompletionAdapter wraps modeling.Completer (single-shot Complete(system, user)).
type CompletionAdapter struct {
	completer modeling.Completer
}

// NewCompletionAdapter constructs the completion port adapter.
func NewCompletionAdapter(completer modeling.Completer) *CompletionAdapter {
	return &CompletionAdapter{completer: completer}
}

// Complete implements pipelineapi.Completion.
//
// pipelineapi.CompletionRequest carries Prompt and Schema. The existing
// modeling.Completer accepts only (system, user), so this adapter appends the
// schema instruction to the user prompt and leaves system empty. Token usage
// and finish reason have no source in the current completer and remain defaults.
func (a *CompletionAdapter) Complete(ctx context.Context, req pipelineapi.CompletionRequest) (pipelineapi.CompletionResult, error) {
	if a == nil || a.completer == nil {
		return pipelineapi.CompletionResult{}, errors.New("runtime adapter: completion dependency is nil")
	}
	user := req.Prompt
	if req.Schema != "" {
		// The current Pipeline completer has no explicit schema parameter, so the
		// adapter appends the schema as a structured-output instruction.
		user = fmt.Sprintf("%s\n\nRespond strictly according to this JSON schema:\n%s", user, req.Schema)
	}
	content, err := a.completer.Complete(ctx, "", user)
	if err != nil {
		return pipelineapi.CompletionResult{}, fmt.Errorf("completion adapter: %w", err)
	}
	return pipelineapi.CompletionResult{
		Content:      content,
		TokensUsed:   0, // The current completer does not report token usage.
		FinishReason: "stop",
	}, nil
}

// Ensure CompletionAdapter satisfies pipelineapi.Completion at compile time.
var _ pipelineapi.Completion = (*CompletionAdapter)(nil)
