package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type Runner interface {
	Run(ctx context.Context, s *session.Session, input agent.RunInput) (string, error)
}

// CandidateSink is the write side of the review queue. The extractor proposes,
// this stores; nothing here can put a line into a prompt.
type CandidateSink interface {
	Add(context.Context, memory.Candidate) (memory.Candidate, error)
}

type Application struct {
	runner      Runner
	sessions    SessionRegistry
	commands    CommandHandler
	extractor   memory.Extractor
	candidates  CandidateSink
	workspaceID string
	logger      *slog.Logger
}

type Dependencies struct {
	Runner   Runner
	Sessions SessionRegistry
	Commands CommandHandler
	// Extractor and Candidates drive the optional post-run proposal hook. They
	// live here rather than in Agent so that one request produces one answer:
	// long-term knowledge writes are an application concern, not part of the
	// loop that has to return a reply.
	Extractor  memory.Extractor
	Candidates CandidateSink
	// WorkspaceID is the stable scope every request is attributed to. It is a
	// derived id, never a filesystem path, because it ends up in stored memory
	// scopes and would otherwise leak the operator's directory layout.
	WorkspaceID string
	Logger      *slog.Logger
}

type CommandHandler interface {
	Execute(context.Context, CommandContext, Command) (CommandResult, error)
}

type SessionRegistry interface {
	WithSession(ctx context.Context, key string, fn func(*session.Session) error) error
	NewWithTrace(ctx context.Context, key, traceID string) (*session.Session, error)
}

func NewApplication(deps Dependencies) (*Application, error) {
	if deps.Runner == nil {
		return nil, errors.New("application runner is nil")
	}
	if deps.Sessions == nil {
		return nil, errors.New("application session registry is nil")
	}
	if deps.Commands == nil {
		return nil, errors.New("application command handler is nil")
	}
	if deps.Extractor == nil {
		return nil, errors.New("application extractor is nil")
	}
	if deps.Candidates == nil {
		return nil, errors.New("application candidate sink is nil")
	}
	if strings.TrimSpace(deps.WorkspaceID) == "" {
		return nil, errors.New("application workspace id is empty")
	}
	if deps.Logger == nil {
		return nil, errors.New("application logger is nil")
	}

	return &Application{
		runner:      deps.Runner,
		sessions:    deps.Sessions,
		commands:    deps.Commands,
		extractor:   deps.Extractor,
		candidates:  deps.Candidates,
		workspaceID: deps.WorkspaceID,
		logger:      deps.Logger,
	}, nil
}

// RunOnce creates and executes one independent session.
func (a *Application) RunOnce(ctx context.Context, traceID string, input string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(traceID) == "" {
		return "", errors.New("trace id is empty")
	}
	if strings.TrimSpace(input) == "" {
		return "", errors.New("input is empty")
	}
	key := "oneshot:" + traceID
	_, isCommand, err := ParseCommand(input)
	if err != nil {
		return "", err
	}
	if !isCommand {
		if _, err := a.sessions.NewWithTrace(ctx, key, traceID); err != nil {
			return "", fmt.Errorf("create one-shot session: %w", err)
		}
	}
	a.logger.DebugContext(ctx, "run one-shot session", "session_key", key, "trace_id", traceID)
	out, err := a.Handle(ctx, channel.Request{
		Inbound:      channel.Inbound{Channel: "cli", SessionKey: key, Text: input},
		Capabilities: channel.Capabilities{InteractiveApproval: false},
		Events:       runstream.NopSink{},
	})
	if err != nil {
		return "", fmt.Errorf("run agent for trace %q: %w", traceID, err)
	}
	return out.Text, nil
}

// Handle really deal run behavior
func (a *Application) Handle(ctx context.Context, request channel.Request) (channel.Outbound, error) {
	in := request.Inbound
	if err := validateInbound(in); err != nil {
		return channel.Outbound{}, err
	}
	command, isCommand, err := ParseCommand(in.Text)
	if err != nil {
		return channel.Outbound{}, err
	}
	if isCommand {
		result, err := a.commands.Execute(ctx, a.commandContext(in), command)
		if err != nil {
			return channel.Outbound{}, err
		}
		return channel.Outbound{
			SessionKey: in.SessionKey,
			Text:       result.Text,
			Action:     result.Action,
		}, nil
	}
	var answer string
	//inject session and run
	err = a.sessions.WithSession(ctx, in.SessionKey, func(sess *session.Session) error {
		result, err := a.runner.Run(ctx, sess, agent.RunInput{
			Text:        in.Text,
			SessionKey:  in.SessionKey,
			Channel:     in.Channel,
			UserID:      in.UserID,
			WorkspaceID: a.workspaceID,
			Interactive: request.Capabilities.InteractiveApproval,
			Events:      runstream.NormalizeSink(request.Events),
		})
		answer = result
		return err
	})
	if err != nil {
		return channel.Outbound{}, fmt.Errorf(
			"handle %s session %q: %w",
			in.Channel, in.SessionKey, err,
		)
	}
	// Only after the run committed: a proposal derived from an exchange that
	// never made it into the transcript would be a memory of something the user
	// never saw answered.
	a.proposeMemories(ctx, in, answer)
	return channel.Outbound{
		SessionKey: in.SessionKey,
		Text:       answer,
		Action:     channel.ActionReply,
	}, nil
}

func (a *Application) commandContext(in channel.Inbound) CommandContext {
	return CommandContext{
		SessionKey:  in.SessionKey,
		UserID:      in.UserID,
		WorkspaceID: a.workspaceID,
	}
}

// proposeMemories is the post-run hook. It is best effort by design: the answer
// is already correct and already delivered, so an extractor or queue failure is
// logged and dropped rather than turned into a request error. Only categories
// are logged — never the exchange, never the proposed content.
func (a *Application) proposeMemories(ctx context.Context, in channel.Inbound, answer string) {
	if strings.TrimSpace(answer) == "" {
		return
	}
	// Private when the channel knows who is speaking, workspace otherwise. An
	// unattributable proposal must not default to being shared with everyone,
	// but the CLI has no user at all, so workspace is the only scope it can use.
	scope := memory.Scope{WorkspaceID: a.workspaceID, Visibility: memory.VisibilityWorkspace}
	if userID := strings.TrimSpace(in.UserID); userID != "" {
		scope.UserID = userID
		scope.Visibility = memory.VisibilityPrivate
	}
	candidates, err := a.extractor.Extract(ctx, memory.Turn{User: in.Text, Assistant: answer}, scope)
	if err != nil {
		a.logger.WarnContext(ctx, "extract memory candidates failed", "session_key", in.SessionKey, "error_kind", errorKind(err))
		return
	}
	for _, candidate := range candidates {
		if _, err := a.candidates.Add(ctx, candidate); err != nil {
			if errors.Is(err, memory.ErrDuplicate) || errors.Is(err, memory.ErrDisabled) {
				continue
			}
			a.logger.WarnContext(ctx, "store memory candidate failed", "session_key", in.SessionKey, "error_kind", errorKind(err))
		}
	}
}

// errorKind reduces an error to something safe to log. The message itself may
// quote model output or memory content, which is exactly what must not reach a
// log file.
func errorKind(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, memory.ErrSensitiveContent):
		return "sensitive-content"
	case errors.Is(err, memory.ErrPromptControl):
		return "prompt-control"
	case errors.Is(err, memory.ErrEmptyContent):
		return "empty-content"
	case errors.Is(err, memory.ErrDuplicate):
		return "duplicate"
	case errors.Is(err, memory.ErrDisabled):
		return "disabled"
	default:
		return "other"
	}
}

func validateInbound(in channel.Inbound) error {
	if strings.TrimSpace(in.Channel) == "" {
		return errors.New("channel is empty")
	}
	if strings.TrimSpace(in.SessionKey) == "" {
		return errors.New("session key is empty")
	}
	if strings.TrimSpace(in.Text) == "" {
		return errors.New("text is empty")
	}
	return nil
}
