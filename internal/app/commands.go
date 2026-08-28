package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type UserError struct {
	Message string
}

func (e UserError) Error() string {
	return e.Message
}

func (UserError) Recoverable() bool {
	return true
}

func userErrorf(format string, args ...any) error {
	return UserError{Message: fmt.Sprintf(format, args...)}
}

type Command struct {
	Name string
	Args []string
	Raw  string
}

func ParseCommand(text string) (Command, bool, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return Command{}, false, nil
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 || fields[0] == "/" {
		return Command{}, true, userErrorf("command name is empty")
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if name == "quit" {
		name = "exit"
	}
	return Command{
		Name: name,
		Args: append([]string(nil), fields[1:]...),
		Raw:  trimmed,
	}, true, nil
}

type SessionCommands interface {
	New(context.Context, string) (*session.Session, error)
	Reset(context.Context, string) (*session.Session, error)
	Current(context.Context, string) (*session.Session, error)
	Resume(context.Context, string, string) (*session.Session, error)
	List(context.Context) ([]session.Meta, error)
}

type SessionUpdater interface {
	Update(context.Context, string, func(*session.Session) error) error
}

type ContextCommands interface {
	EnforceBudget(context.Context, contextmgr.ModelBudget, []llm.Message) ([]llm.Message, int, error)
}

type ModelCommands interface {
	Resolve(llm.ModelRef) (llm.ResolvedModel, error)
}

type CommandDependencies struct {
	Sessions   SessionCommands
	Updater    SessionUpdater
	Context    ContextCommands
	Models     ModelCommands
	Skills     SkillCommands
	Memories   MemoryCommands
	Candidates CandidateCommands
	Now        func() time.Time
}

type CommandResult struct {
	Text   string
	Action channel.Action
}

type CommandConfig struct {
	// MemoryTopK bounds /memory search. It is the same ceiling the request path
	// uses, so a user browsing recall sees what the model would have seen.
	MemoryTopK int
}

type CommandRouter struct {
	sessions   SessionCommands
	updater    SessionUpdater
	context    ContextCommands
	models     ModelCommands
	skills     SkillCommands
	memories   MemoryCommands
	candidates CandidateCommands
	memoryTopK int
	now        func() time.Time
}

func NewCommandRouter(deps CommandDependencies, cfg CommandConfig) (*CommandRouter, error) {
	if deps.Sessions == nil {
		return nil, errors.New("command router sessions is nil")
	}
	if deps.Updater == nil {
		return nil, errors.New("command router updater is nil")
	}
	if deps.Context == nil {
		return nil, errors.New("command router context is nil")
	}
	if deps.Models == nil {
		return nil, errors.New("command router models is nil")
	}
	// The knowledge dependencies are always present; a disabled layer is an empty
	// registry and memory.DisabledStore, so no command path is nil-checked.
	if deps.Skills == nil {
		return nil, errors.New("command router skills is nil")
	}
	if deps.Memories == nil {
		return nil, errors.New("command router memories is nil")
	}
	if deps.Candidates == nil {
		return nil, errors.New("command router candidates is nil")
	}
	if cfg.MemoryTopK <= 0 {
		return nil, errors.New("command router memory top-k must be > 0")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &CommandRouter{
		sessions:   deps.Sessions,
		updater:    deps.Updater,
		context:    deps.Context,
		models:     deps.Models,
		skills:     deps.Skills,
		memories:   deps.Memories,
		candidates: deps.Candidates,
		memoryTopK: cfg.MemoryTopK,
		now:        deps.Now,
	}, nil
}

func (r *CommandRouter) Execute(ctx context.Context, cc CommandContext, command Command) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	sessionKey := cc.SessionKey
	if strings.TrimSpace(sessionKey) == "" {
		return CommandResult{}, errors.New("command session key is empty")
	}
	switch command.Name {
	case "help":
		if err := requireArgCount(command, 0, "/help"); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{
			Text: strings.Join([]string{
				"/help",
				"/new",
				"/reset",
				"/sessions",
				"/resume <session-id>",
				"/history",
				"/compact",
				"/skills [list|show <name>]",
				"/remember [--kind=<kind>] [--scope=<scope>] <text>",
				"/memory list|search <text>|show <id>|forget <id>|pending|approve <id>|reject <id>",
				"/exit",
			}, "\n"),
			Action: channel.ActionReply,
		}, nil
	case "new":
		if err := requireArgCount(command, 0, "/new"); err != nil {
			return CommandResult{}, err
		}
		created, err := r.sessions.New(ctx, sessionKey)
		if err != nil {
			return CommandResult{}, fmt.Errorf("create session: %w", err)
		}
		return reply(fmt.Sprintf("new session: %s", created.ID)), nil
	case "reset":
		if err := requireArgCount(command, 0, "/reset"); err != nil {
			return CommandResult{}, err
		}
		reset, err := r.sessions.Reset(ctx, sessionKey)
		if errors.Is(err, session.ErrNoCurrentSession) {
			return CommandResult{}, userErrorf("no current session")
		}
		if err != nil {
			return CommandResult{}, fmt.Errorf("reset session: %w", err)
		}
		return reply(fmt.Sprintf("reset session: %s", reset.ID)), nil
	case "sessions":
		if err := requireArgCount(command, 0, "/sessions"); err != nil {
			return CommandResult{}, err
		}
		return r.listSessions(ctx)
	case "resume":
		return r.resumeSession(ctx, sessionKey, command.Args)
	case "history":
		if err := requireArgCount(command, 0, "/history"); err != nil {
			return CommandResult{}, err
		}
		return r.history(ctx, sessionKey)
	case "compact":
		return r.compact(ctx, sessionKey, command.Args)
	case "skills":
		return r.skillsCommand(ctx, command.Args)
	case "remember":
		return r.remember(ctx, cc, command.Args)
	case "memory":
		return r.memoryCommand(ctx, cc, command.Args)
	case "exit":
		if err := requireArgCount(command, 0, "/exit"); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{Action: channel.ActionExit}, nil
	default:
		return CommandResult{}, userErrorf("unknown command /%s; use /help", command.Name)
	}
}

func requireArgCount(command Command, count int, usage string) error {
	if len(command.Args) != count {
		return userErrorf("usage: %s", usage)
	}
	return nil
}

func reply(text string) CommandResult {
	return CommandResult{Text: text, Action: channel.ActionReply}
}

func (r *CommandRouter) listSessions(ctx context.Context) (CommandResult, error) {
	items, err := r.sessions.List(ctx)
	if err != nil {
		return CommandResult{}, fmt.Errorf("list sessions: %w", err)
	}
	if len(items) == 0 {
		return reply("no saved sessions"), nil
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, fmt.Sprintf(
			"%s  model=%s  updated=%s",
			item.ID,
			item.ModelRef.String(),
			item.UpdatedAt.Format(time.RFC3339),
		))
	}
	return reply(strings.Join(lines, "\n")), nil
}

func (r *CommandRouter) resumeSession(ctx context.Context, key string, args []string) (CommandResult, error) {
	if len(args) != 1 {
		return CommandResult{}, userErrorf("usage: /resume <session-id>")
	}
	resumed, err := r.sessions.Resume(ctx, key, args[0])
	if errors.Is(err, fs.ErrNotExist) {
		return CommandResult{}, userErrorf("session %q does not exist", args[0])
	}
	if errors.Is(err, llm.ErrModelNotFound) {
		return CommandResult{}, userErrorf("session %q uses an unregistered model; restore its model configuration before resuming", args[0])
	}
	if err != nil {
		return CommandResult{}, fmt.Errorf("resume session %q: %w", args[0], err)
	}
	return reply(fmt.Sprintf("resumed session: %s", resumed.ID)), nil
}

func (r *CommandRouter) history(ctx context.Context, key string) (CommandResult, error) {
	current, err := r.sessions.Current(ctx, key)
	if errors.Is(err, session.ErrNoCurrentSession) {
		return CommandResult{}, userErrorf("no current session")
	}
	if err != nil {
		return CommandResult{}, fmt.Errorf("get current session: %w", err)
	}
	if len(current.Messages) == 0 {
		return reply("session history is empty"), nil
	}
	lines := make([]string, 0, len(current.Messages))
	for _, message := range current.Messages {
		content := truncateRunes(message.Content, 500)
		content = strings.ReplaceAll(content, "\n", "\\n")
		lines = append(lines, fmt.Sprintf("%s: %s", message.Role, content))
	}
	return reply(strings.Join(lines, "\n")), nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func (r *CommandRouter) compact(ctx context.Context, key string, args []string) (CommandResult, error) {
	if len(args) != 0 {
		return CommandResult{}, userErrorf("usage: /compact")
	}
	var before, after int
	err := r.updater.Update(ctx, key, func(sess *session.Session) error {
		before = len(sess.Messages)
		resolved, err := r.models.Resolve(sess.ModelRef)
		if err != nil {
			return fmt.Errorf("resolve compact model: %w", err)
		}
		messages, usage, err := r.context.EnforceBudget(ctx, contextmgr.ModelBudget{Ref: resolved.Definition.Ref, MaxContext: resolved.Definition.MaxContext}, sess.MessageCopy())
		if err != nil {
			return fmt.Errorf("compact session context: %w", err)
		}
		sess.MessageReplace(messages, usage)
		after = len(messages)
		return nil
	})
	if errors.Is(err, session.ErrNoCurrentSession) {
		return CommandResult{}, userErrorf("no current session")
	}
	if err != nil {
		return CommandResult{}, err
	}
	return reply(fmt.Sprintf("compacted messages: %d -> %d", before, after)), nil
}
