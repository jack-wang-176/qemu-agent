// The adapter from modeling.ToolRunner to security.Executor.
//
// It exists because the two sides deliberately speak different languages. The
// modeling package wants a tool call to look like any other audited side effect:
// a name, some arguments, a result. The security package wants an Invocation with
// a full audit identity attached. Neither should be bent towards the other — a
// stage that could build an Invocation could also claim to be interactive — so
// the translation lives here, in the wiring, where the identity is available.
package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// modelingTools turns modeling's Run(ctx, name, args) into an audited execution.
type modelingTools struct {
	executor ToolExecutor
	newID    func() string
	now      func() time.Time
}

var _ modeling.ToolRunner = (*modelingTools)(nil)

// newModelingTools builds the adapter. The nil checks are the caller's job:
// BuildModeling refuses a nil executor before it gets here, so a zero-value
// adapter cannot be constructed by accident.
func newModelingTools(executor ToolExecutor, newID func() string) *modelingTools {
	return &modelingTools{executor: executor, newID: newID, now: time.Now}
}

// Run executes one tool call on behalf of a modeling stage or the applier.
func (m *modelingTools) Run(ctx context.Context, name string, args map[string]any) (tools.ExecutionResult, error) {
	// 1: the identity. This is a hard refusal rather than a default, because every
	// alternative is worse: an assumed non-interactive caller silently denies the
	// writes that apply exists to perform, and an assumed interactive one sends an
	// approval prompt to whatever terminal this process happens to own. A missing
	// caller means the request path failed to attach one, which is a bug in the
	// wiring, and it should look like one.
	caller, ok := security.CallerFrom(ctx)
	if !ok {
		return tools.ExecutionResult{}, errors.New("modeling tool call has no caller identity")
	}

	// 2: the arguments. Tools take JSON, and encoding here rather than in the
	// stages means a stage never builds a string that has to be parsed as a
	// command — it passes a map, and this is the only place that serialises it.
	encoded, err := json.Marshal(args)
	if err != nil {
		// The map came from a stage, and its values can include a fragment of a
		// datasheet or of a model reply, so the underlying error is dropped: it
		// would quote the offending value.
		return tools.ExecutionResult{}, fmt.Errorf("encode arguments for tool %q", name)
	}

	// 3: the execution. Policy, approval and audit all happen inside Execute; this
	// adapter adds nothing and bypasses nothing.
	result, err := m.executor.Execute(ctx, security.Invocation{
		ID:          m.newID(),
		TraceID:     caller.TraceID,
		SessionID:   caller.SessionID,
		SessionKey:  caller.SessionKey,
		Channel:     caller.Channel,
		Interactive: caller.Interactive,
		ToolName:    name,
		Arguments:   string(encoded),
		RequestedAt: m.now(),
	})
	if err != nil {
		// Returned unwrapped: the modeling package classifies tool failures with
		// errors.Is against the security package's sentinels, and a denial has to
		// stay recognisable as a denial all the way up to the command reply.
		return tools.ExecutionResult{}, err
	}
	return tools.ExecutionResult{
		ModelOutput:      result.Output,
		PersistentOutput: result.PersistentOutput,
	}, nil
}
