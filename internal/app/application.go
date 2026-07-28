package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type Runner interface {
	Run(ctx context.Context, s *session.Session, input agent.RunInput) (string, error)
}

type Application struct {
	runner    Runner
	sessions  SessionRegistry
	commands  CommandHandler
	logger    *slog.Logger
	toolRunMu sync.Mutex
}

type Dependencies struct {
	Runner   Runner
	Sessions SessionRegistry
	Commands CommandHandler
	Logger   *slog.Logger
}

type CommandHandler interface {
	Execute(context.Context, string, Command) (CommandResult, error)
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
	if deps.Logger == nil {
		return nil, errors.New("application logger is nil")
	}

	return &Application{
		runner:   deps.Runner,
		sessions: deps.Sessions,
		commands: deps.Commands,
		logger:   deps.Logger,
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
		Inbound: channel.Inbound{Channel: "cli", SessionKey: key, Text: input},
		Events:  runstream.NopSink{},
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
		result, err := a.commands.Execute(ctx, in.SessionKey, command)
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
	// CLI approval shares the channel reader with the REPL. Serialize agent runs
	// so two sessions cannot concurrently consume approval/input lines.
	a.toolRunMu.Lock()
	defer a.toolRunMu.Unlock()
	//inject session and run
	err = a.sessions.WithSession(ctx, in.SessionKey, func(sess *session.Session) error {
		result, err := a.runner.Run(ctx, sess, agent.RunInput{
			Text:        in.Text,
			SessionKey:  in.SessionKey,
			Channel:     in.Channel,
			Interactive: in.Channel == "cli" && !strings.HasPrefix(in.SessionKey, "oneshot:"),
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
	return channel.Outbound{
		SessionKey: in.SessionKey,
		Text:       answer,
		Action:     channel.ActionReply,
	}, nil
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
