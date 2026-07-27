package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

/* a certain run of agent,for a certain input.*/
func (a *Agent) Run(ctx context.Context, s *session.Session, input RunInput) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil {
		return "", errors.New("session is nil")
	}
	resolved, err := a.models.Resolve(s.ModelRef)
	if err != nil {
		return "", fmt.Errorf("resolve session model %q: %w", s.ModelRef.String(), err)
	}
	definition, provider := resolved.Definition, resolved.Provider
	if a.stream && !definition.Streaming {
		return "", fmt.Errorf("model %q does not support streaming", definition.Ref.String())
	}
	if strings.TrimSpace(input.Text) == "" {
		return "", errors.New("input is empty")
	}
	if a.maxTurns <= 0 {
		return "", errors.New("max turns must be positive")
	}
	/* input in.*/
	s.AddUser(input.Text)
	for turn := 1; turn <= a.maxTurns; turn++ {
		original := s.MessageCopy()
		trimmed, used, err := a.ctxmgr.EnforceBudget(ctx, contextmgr.ModelBudget{Ref: definition.Ref, MaxContext: definition.MaxContext}, original)
		if err != nil {
			a.logger.WarnContext(ctx, "enforce context budget", "err", err)
		} else {
			/* token limit hit replaced compacted.*/
			s.MessageReplace(trimmed, used)
		}
		/* call model.*/
		tools := a.catalog.Schemas()
		if len(tools) > 0 && !definition.Tools {
			return "", fmt.Errorf("model %q does not support tools", definition.Ref.String())
		}
		response, err := provider.Complete(ctx, llm.Request{
			Model: definition.Ref.Model, Messages: s.MessageCopy(), Tools: tools, MaxTokens: definition.MaxOutput,
		})
		if err != nil {
			return "", fmt.Errorf("turn %d provider: %w", turn, err)
		}
		if response == nil {
			return "", errors.New("provider returned nil response")
		}
		s.AddAssistant(response.Message)
		/* save session*/
		if err := a.store.Save(ctx, s); err != nil {
			return "", fmt.Errorf("save assistant response: %w", err)
		}
		if len(response.Message.ToolCalls) == 0 {
			return response.Message.Content, nil
		}
		/* execute tool a d all tool msg*/
		for _, call := range response.Message.ToolCalls {
			result, execErr := a.executor.Execute(ctx, security.Invocation{ID: a.newID(), TraceID: s.TraceID, SessionID: s.ID, SessionKey: input.SessionKey, Channel: input.Channel, Interactive: input.Interactive, ToolName: call.Name, Arguments: call.Args, RequestedAt: a.now()})
			out := result.Output
			if execErr != nil {
				out = fmt.Sprintf("ERROR: tool %q failed: %v", call.Name, execErr)
			}
			s.AddToolResult(out, call.ID)
		}
		if err := a.store.Save(ctx, s); err != nil {
			return "", fmt.Errorf("save tool results: %w", err)
		}
	}
	return "", fmt.Errorf("reached max turns (%d)", a.maxTurns)
}

type RunInput struct {
	Text        string
	SessionKey  string
	Channel     string
	Interactive bool
}
