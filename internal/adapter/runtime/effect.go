package runtime

// effect.go — Effect Port Adapter (A4).
//
// Wraps the existing modeling.ToolRunner (backed by security.Executor) as
// pipelineapi.Effect. The Engine declares only a logical effect name and
// arguments; trusted identity is injected by the application/adapter layer.
//
// Design rules (v1-04, section six): reuse security.Executor in this phase;
// the Engine never constructs a security caller and the adapter performs mapping.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// EffectAdapter wraps modeling.ToolRunner.
type EffectAdapter struct {
	tools modeling.ToolRunner
}

// NewEffectAdapter constructs the effect port adapter.
func NewEffectAdapter(tools modeling.ToolRunner) *EffectAdapter {
	return &EffectAdapter{tools: tools}
}

// Invoke implements pipelineapi.Effect.
//
// EffectRequest carries Name, Args, and Caller. ToolRunner.Run accepts a name
// and map, so the adapter decodes JSON arguments and maps its result to EffectResult.
//
// EffectRequest.Args is JSON bytes and is decoded before invoking ToolRunner.
func (a *EffectAdapter) Invoke(ctx context.Context, req pipelineapi.EffectRequest) (pipelineapi.EffectResult, error) {
	if a == nil || a.tools == nil {
		return pipelineapi.EffectResult{}, errors.New("runtime adapter: effect dependency is nil")
	}
	// Decode JSON arguments into the map expected by ToolRunner.
	args, err := parseArgsToMap(req.Args)
	if err != nil {
		return pipelineapi.EffectResult{
			Status: pipelineapi.EffectFailed,
			Err:    fmt.Sprintf("effect adapter: parse args: %v", err),
		}, nil
	}

	result, err := a.tools.Run(ctx, req.Name, args)
	if err != nil {
		return pipelineapi.EffectResult{
			Status: pipelineapi.EffectFailed,
			Err:    fmt.Sprintf("effect adapter: tool run: %v", err),
		}, nil
	}

	// ExecutionResult exposes model and persistent output only. Errors were
	// handled above, so a returned result is considered successful.
	return pipelineapi.EffectResult{
		Status: pipelineapi.EffectSucceeded,
		Output: []byte(result.ModelOutput),
	}, nil
}

// Ensure EffectAdapter satisfies pipelineapi.Effect at compile time.
var _ pipelineapi.Effect = (*EffectAdapter)(nil)
