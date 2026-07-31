package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

/* a certain run of agent,for a certain input.*/
func (a *Agent) Run(ctx context.Context, s *session.Session, input string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil {
		return "", errors.New("session is nil")
	}
	if strings.TrimSpace(s.Model) == "" {
		return "", errors.New("session model is empty")
	}
	if strings.TrimSpace(input) == "" {
		return "", errors.New("input is empty")
	}
	if a.maxTurns <= 0 {
		return "", errors.New("max turns must be positive")
	}
	/* input in.*/
	s.AddUser(input)
	for turn := 1; turn <= a.maxTurns; turn++ {
		original := s.MessageCopy()
		trimmed, used, err := a.ctxmgr.EnforceBudget(ctx, s.Model, original)
		if err != nil {
			a.logger.WarnContext(ctx, "enforce context budget", "err", err)
		} else {
			/* token limit hit replaced compacted.*/
			s.MessageReplace(trimmed, used)
		}
		/* call model.*/
		response, err := a.provider.Complete(ctx, llm.Request{
			Model: s.Model, Messages: s.MessageCopy(), Tools: a.tools.Schemas(),
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
			out, execErr := a.tools.Execute(ctx, call.Name, call.Args)
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
