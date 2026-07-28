package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
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
	answer, err = a.runWorking(ctx, working, input, events)
	if err != nil {
		return "", failRun(ctx, events, err)
	}
	if err := s.CanReplaceFrom(working); err != nil {
		return "", failRun(ctx, events, fmt.Errorf("validate session commit: %w", err))
	}
	if err := a.store.Save(ctx, working); err != nil {
		return "", failRun(ctx, events, fmt.Errorf("save completed run: %w", err))
	}
	if err := s.ReplaceFrom(working); err != nil {
		return "", failRun(ctx, events, fmt.Errorf("commit completed run: %w", err))
	}
	if err := events.Emit(ctx, runstream.Event{Type: runstream.EventRunCompleted}); err != nil {
		return "", fmt.Errorf("%w after session commit: %v", ErrEventDelivery, err)
	}
	return answer, nil
}

func (a *Agent) runWorking(ctx context.Context, s *session.Session, input RunInput, events *emitter) (string, error) {
	resolved, err := a.models.Resolve(s.ModelRef)
	if err != nil {
		return "", fmt.Errorf("resolve session model %q: %w", s.ModelRef.String(), err)
	}
	definition := resolved.Definition
	tools := a.catalog.Schemas()
	if len(tools) > 0 && !definition.Tools {
		return "", fmt.Errorf("model %q does not support tools", definition.Ref.String())
	}
	s.AddUser(input.Text)
	for turn := 1; turn <= a.maxTurns; turn++ {
		if err := events.Emit(ctx, runstream.Event{Type: runstream.EventTurnStarted, Turn: turn}); err != nil {
			return "", fmt.Errorf("%w: %v", ErrEventDelivery, err)
		}
		original := s.MessageCopy()
		trimmed, used, err := a.ctxmgr.EnforceBudget(ctx, contextmgr.ModelBudget{Ref: definition.Ref, MaxContext: definition.MaxContext}, original)
		if err != nil {
			return "", fmt.Errorf("enforce context budget: %w", err)
		}
		s.MessageReplace(trimmed, used)
		response, err := a.completeTurn(ctx, resolved, llm.Request{
			Model: definition.Ref.Model, Messages: s.MessageCopy(), Tools: tools, MaxTokens: definition.MaxOutput,
		}, events, turn)
		if err != nil {
			return "", fmt.Errorf("turn %d provider: %w", turn, err)
		}
		s.AddAssistant(response.Message)
		if response.Usage.TotalToken > 0 {
			s.TokenUsage += int(response.Usage.TotalToken)
		}
		if len(response.Message.ToolCalls) == 0 {
			return response.Message.Content, nil
		}
		for _, call := range response.Message.ToolCalls {
			if err := events.Emit(ctx, runstream.Event{Type: runstream.EventToolStarted, Turn: turn, ToolCallID: call.ID, ToolName: call.Name}); err != nil {
				return "", fmt.Errorf("%w: %v", ErrEventDelivery, err)
			}
			result, execErr := a.executor.Execute(ctx, security.Invocation{ID: a.newID(), TraceID: s.TraceID, SessionID: s.ID, SessionKey: input.SessionKey, Channel: input.Channel, Interactive: input.Interactive, ToolName: call.Name, Arguments: call.Args, RequestedAt: a.now()})
			ok := execErr == nil
			kind, summary := publicError(execErr)
			if err := events.Emit(ctx, runstream.Event{Type: runstream.EventToolCompleted, Turn: turn, ToolCallID: call.ID, ToolName: call.Name, ToolOK: &ok, ErrorKind: kind, Summary: summary}); err != nil {
				return "", errors.Join(execErr, fmt.Errorf("%w: %v", ErrEventDelivery, err))
			}
			out := result.Output
			if execErr != nil {
				out = toolResultText(call.Name, execErr)
			}
			s.AddToolResult(out, call.ID)
		}
	}
	return "", fmt.Errorf("reached max turns (%d)", a.maxTurns)
}

func failRun(ctx context.Context, events *emitter, cause error) error {
	kind, summary := publicError(cause)
	emitErr := events.Emit(ctx, runstream.Event{Type: runstream.EventRunFailed, ErrorKind: kind, Summary: summary})
	if emitErr != nil {
		emitErr = fmt.Errorf("%w: %v", ErrEventDelivery, emitErr)
	}
	return errors.Join(cause, emitErr)
}

type RunInput struct {
	Text        string
	SessionKey  string
	Channel     string
	Interactive bool
	Events      runstream.EventSink
}
