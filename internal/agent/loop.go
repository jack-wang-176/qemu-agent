package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

/* a certain run of agent,for a certain input.*/
func (a *Agent) Run(ctx context.Context, s *session.Session, input RunInput) (answer string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil {
		return "", errors.New("session is nil")
	}
	if err := validateRunInput(input); err != nil {
		return "", err
	}
	events, err := newEmitter(input, s, a.now)
	if err != nil {
		return "", err
	}
	if err := events.Emit(ctx, runstream.Event{Type: runstream.EventRunStarted}); err != nil {
		return "", fmt.Errorf("%w: %v", ErrEventDelivery, err)
	}
	working := s.Clone()
	answer, projections, err := a.runWorking(ctx, working, input, events)
	if err != nil {
		return "", failRun(ctx, events, err)
	}
	// The working copy holds what the model saw this run; the persisted copy
	// holds what the next run is allowed to replay. They differ only where a
	// tool declared a transient payload.
	persisted := working.Clone()
	if err := a.applyProjections(persisted, projections); err != nil {
		return "", failRun(ctx, events, err)
	}
	if err := s.CanReplaceFrom(persisted); err != nil {
		return "", failRun(ctx, events, fmt.Errorf("validate session commit: %w", err))
	}
	if err := a.store.Save(ctx, persisted); err != nil {
		return "", failRun(ctx, events, fmt.Errorf("save completed run: %w", err))
	}
	if err := s.ReplaceFrom(persisted); err != nil {
		return "", failRun(ctx, events, fmt.Errorf("commit completed run: %w", err))
	}
	if err := events.Emit(ctx, runstream.Event{Type: runstream.EventRunCompleted}); err != nil {
		return "", fmt.Errorf("%w after session commit: %v", ErrEventDelivery, err)
	}
	return answer, nil
}

// toolProjection is one pending rewrite: the tool message identified by CallID
// must be stored as Text instead of the text the model received.
type toolProjection struct {
	CallID   string
	ToolName string
	Text     string
}

// applyProjections rewrites the persisted transcript. A missing target is
// tolerated: EnforceBudget may have compacted the tool message away inside the
// same run, and dropping the whole run over an already-deleted message would be
// worse than losing one receipt. A duplicate call id stays fatal because it
// means the transcript itself is inconsistent.
func (a *Agent) applyProjections(target *session.Session, projections []toolProjection) error {
	for _, projection := range projections {
		err := target.ReplaceToolResult(projection.CallID, projection.Text)
		switch {
		case err == nil:
		case errors.Is(err, session.ErrToolResultNotFound):
			a.logger.Warn("tool result projection skipped",
				"tool", projection.ToolName, "tool_call_id", projection.CallID, "reason", "message not in transcript")
		default:
			return fmt.Errorf("project tool result for %q: %w", projection.ToolName, err)
		}
	}
	return nil
}

func (a *Agent) runWorking(ctx context.Context, s *session.Session, input RunInput, events *emitter) (string, []toolProjection, error) {
	var projections []toolProjection
	resolved, err := a.models.Resolve(s.ModelRef)
	if err != nil {
		return "", nil, fmt.Errorf("resolve session model %q: %w", s.ModelRef.String(), err)
	}
	definition := resolved.Definition
	tools := a.catalog.Schemas()
	if len(tools) > 0 && !definition.Tools {
		return "", nil, fmt.Errorf("model %q does not support tools", definition.Ref.String())
	}
	// The history has to shrink by what the overlay and the answer will occupy.
	// Computing it once per run, before the first turn, turns a misconfigured
	// reservation into one clear error instead of a truncated prompt.
	historyBudget := definition.MaxContext - definition.MaxOutput - a.reservedTokens
	if historyBudget <= 0 {
		return "", nil, fmt.Errorf("model %q has no history budget: context %d, output %d, reserved %d",
			definition.Ref.String(), definition.MaxContext, definition.MaxOutput, a.reservedTokens)
	}
	s.AddUser(input.Text)
	// Retrieval happens once. Re-ranking between turns would let the recalled set
	// change while the model is mid-task, and a tool result from turn one would
	// silently steer what turn two remembers.
	snapshot, err := a.prompts.Prepare(ctx, prompt.ContextQuery{
		Text:        input.Text,
		WorkspaceID: input.WorkspaceID,
		UserID:      input.UserID,
		TopK:        a.memoryTopK,
		Now:         a.now(),
	})
	if err != nil {
		return "", nil, fmt.Errorf("prepare prompt context: %w", err)
	}
	for turn := 1; turn <= a.maxTurns; turn++ {
		if err := events.Emit(ctx, runstream.Event{Type: runstream.EventTurnStarted, Turn: turn}); err != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrEventDelivery, err)
		}
		original := s.MessageCopy()
		trimmed, used, err := a.ctxmgr.EnforceBudget(ctx, contextmgr.ModelBudget{Ref: definition.Ref, MaxContext: historyBudget}, original)
		if err != nil {
			return "", nil, fmt.Errorf("enforce context budget: %w", err)
		}
		// Only the compaction result goes back into the session. plan.Messages
		// below carries the overlay and must never be written back, or the next
		// request would replay this request's recall as if the user had said it.
		s.MessageReplace(trimmed, used)
		plan, err := a.prompts.Build(ctx, prompt.Input{
			Persistent: s.MessageCopy(),
			Snapshot:   snapshot,
			MaxBytes:   a.promptBytes,
		})
		if err != nil {
			return "", nil, fmt.Errorf("build prompt for turn %d: %w", turn, err)
		}
		response, err := a.completeTurn(ctx, resolved, llm.Request{
			Model: definition.Ref.Model, Messages: plan.Messages, Tools: tools, MaxTokens: definition.MaxOutput,
		}, events, turn)
		if err != nil {
			return "", nil, fmt.Errorf("turn %d provider: %w", turn, err)
		}
		s.AddAssistant(response.Message)
		if response.Usage.TotalToken > 0 {
			s.TokenUsage += int(response.Usage.TotalToken)
		}
		if len(response.Message.ToolCalls) == 0 {
			return response.Message.Content, projections, nil
		}
		for _, call := range response.Message.ToolCalls {
			if err := events.Emit(ctx, runstream.Event{Type: runstream.EventToolStarted, Turn: turn, ToolCallID: call.ID, ToolName: call.Name}); err != nil {
				return "", nil, fmt.Errorf("%w: %v", ErrEventDelivery, err)
			}
			result, execErr := a.executor.Execute(ctx, security.Invocation{ID: a.newID(), TraceID: s.TraceID, SessionID: s.ID, SessionKey: input.SessionKey, Channel: input.Channel, Interactive: input.Interactive, ToolName: call.Name, Arguments: call.Args, RequestedAt: a.now()})
			ok := execErr == nil
			kind, summary := publicError(execErr)
			if err := events.Emit(ctx, runstream.Event{Type: runstream.EventToolCompleted, Turn: turn, ToolCallID: call.ID, ToolName: call.Name, ToolOK: &ok, ErrorKind: kind, Summary: summary}); err != nil {
				return "", nil, errors.Join(execErr, fmt.Errorf("%w: %v", ErrEventDelivery, err))
			}
			out := result.Output
			if execErr != nil {
				out = toolResultText(call.Name, execErr)
			}
			s.AddToolResult(out, call.ID)
			// Record the rewrite instead of applying it now: the model must keep
			// seeing the full output for the remaining turns of this run.
			if execErr == nil && result.ProjectionChanged() {
				projections = append(projections, toolProjection{CallID: call.ID, ToolName: call.Name, Text: result.PersistentOutput})
			}
		}
	}
	return "", nil, fmt.Errorf("reached max turns (%d)", a.maxTurns)
}

func failRun(ctx context.Context, events *emitter, cause error) error {
	kind, summary := publicError(cause)
	emitErr := events.Emit(ctx, runstream.Event{Type: runstream.EventRunFailed, ErrorKind: kind, Summary: summary})
	if emitErr != nil {
		emitErr = fmt.Errorf("%w: %v", ErrEventDelivery, emitErr)
	}
	return errors.Join(cause, emitErr)
}

// RunInput is one request. UserID and WorkspaceID are identity for retrieval
// only: they scope which memories may be read, and a channel that has no notion
// of a user (the CLI) leaves UserID empty, which limits recall to workspace
// items instead of leaking another user's private ones.
type RunInput struct {
	Text        string
	SessionKey  string
	Channel     string
	UserID      string
	WorkspaceID string
	Interactive bool
	Events      runstream.EventSink
}
