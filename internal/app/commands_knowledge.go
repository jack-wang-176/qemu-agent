package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/memory"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/skills"
)

// SkillCommands is the read-only view the router needs. It is deliberately the
// registry's own metadata API: a management command must not become a second way
// to inject a full skill body into a conversation.
type SkillCommands interface {
	List(context.Context) ([]skills.Meta, error)
	Load(context.Context, string) (skills.Skill, error)
}

// MemoryCommands is memory.Store minus Touch. Usage accounting belongs to the
// request path, not to somebody browsing their own notes.
type MemoryCommands interface {
	Save(context.Context, memory.Memory) (memory.Memory, error)
	Get(context.Context, string, memory.Scope) (memory.Memory, error)
	List(context.Context, memory.Query) ([]memory.Memory, error)
	Delete(context.Context, string, memory.Scope) error
	Search(context.Context, memory.Query) ([]memory.Match, error)
}

// CandidateCommands is the review side of the auto-extractor.
type CandidateCommands interface {
	ListPending(context.Context, string, string) ([]memory.Candidate, error)
	Get(context.Context, string, memory.Scope) (memory.Candidate, error)
	Resolve(context.Context, string, memory.Scope, memory.CandidateStatus, string) (memory.Candidate, error)
}

// CommandContext carries request identity plus the two request-scoped
// capabilities a command may need. The router must never derive a user from the
// session key: keys are formatted per channel, and one parsing mistake would
// decide which user's private memories a command may touch.
//
// Interactive and Events are here for the same reason they are on the agent's
// RunInput: a command that runs for minutes (/modeling advance) has to be able
// to report progress, and a command that applies a patch has to know whether
// anybody can be asked for approval. Neither may be inferred from the channel
// name — the transport declares its capabilities, the command layer only reads
// them.
type CommandContext struct {
	SessionKey  string
	UserID      string
	WorkspaceID string
	// TraceID correlates a command's events and audit entries with the request
	// that produced them. It is generated per request, never taken from input.
	TraceID string
	// Interactive is channel.Capabilities.InteractiveApproval: true only when
	// somebody is on the other end who can answer an approval prompt.
	Interactive bool
	// Channel is how the request arrived ("cli", "telegram"). It is carried for
	// audit identity: a background subsystem that executes a tool on this
	// request's behalf has to record which channel asked, and it cannot derive
	// that from anything else it holds.
	Channel string
	// Events is never nil — the application injects runstream.NopEmitter when no
	// sink is attached — so command code contains no `if cc.Events != nil`.
	Events runstream.Emitter
}

const (
	// listContentLimit keeps a listing readable and, on Telegram, deliverable.
	listContentLimit = 120
	showContentLimit = 1000
	skillBodyLimit   = 400
)

func (r *CommandRouter) skillsCommand(ctx context.Context, args []string) (CommandResult, error) {
	switch {
	case len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "list")):
		metas, err := r.skills.List(ctx)
		if err != nil {
			return CommandResult{}, fmt.Errorf("list skills: %w", err)
		}
		if len(metas) == 0 {
			return reply("no skills available"), nil
		}
		lines := make([]string, 0, len(metas))
		for _, meta := range metas {
			// Path and checksum stay out: the list is user-facing, and a directory
			// layout is exactly the kind of detail a prompt-injected model would
			// like to learn.
			line := fmt.Sprintf("%s  v%s  %s", meta.Name, meta.Version, meta.Description)
			if len(meta.RequiredTools) > 0 {
				line += "  requires: " + strings.Join(meta.RequiredTools, ",")
			}
			lines = append(lines, line)
		}
		return reply(strings.Join(lines, "\n")), nil
	case len(args) == 2 && strings.EqualFold(args[0], "show"):
		skill, err := r.skills.Load(ctx, args[1])
		if err != nil {
			var notFound skills.SkillNotFoundError
			if errors.As(err, &notFound) {
				return CommandResult{}, userErrorf("skill %q does not exist; use /skills list", args[1])
			}
			return CommandResult{}, fmt.Errorf("load skill: %w", err)
		}
		meta := skill.Meta
		lines := []string{
			"name: " + meta.Name,
			"version: " + meta.Version,
			"description: " + meta.Description,
		}
		if len(meta.RequiredTools) > 0 {
			lines = append(lines, "required tools: "+strings.Join(meta.RequiredTools, ","))
		}
		// A preview only. The full body is loaded through use_skill so it goes
		// through the tool policy, the audit log and the prompt budget.
		lines = append(lines, "", truncateRunes(skill.Body, skillBodyLimit))
		return reply(strings.Join(lines, "\n")), nil
	default:
		return CommandResult{}, userErrorf("usage: /skills [list|show <name>]")
	}
}

type rememberArgs struct {
	Kind       memory.Kind
	Visibility memory.Visibility
	Content    string
}

// parseRemember accepts only `--flag=value` forms. Shell-style quoting is not
// implemented on purpose: it would silently change how content is split, and the
// sanitizer collapses runs of whitespace anyway, so joining the remaining fields
// loses nothing that would be stored.
func parseRemember(args []string) (rememberArgs, error) {
	parsed := rememberArgs{Kind: memory.KindFact, Visibility: memory.VisibilityPrivate}
	index := 0
flags:
	for ; index < len(args); index++ {
		arg := args[index]
		switch {
		case strings.HasPrefix(arg, "--kind="):
			kind, err := memory.ParseKind(strings.TrimPrefix(arg, "--kind="))
			if err != nil {
				return rememberArgs{}, userErrorf("unknown kind; use fact, preference, decision or constraint")
			}
			parsed.Kind = kind
		case strings.HasPrefix(arg, "--scope="):
			visibility, err := memory.ParseVisibility(strings.TrimPrefix(arg, "--scope="))
			if err != nil {
				return rememberArgs{}, userErrorf("unknown scope; use private or workspace")
			}
			parsed.Visibility = visibility
		case strings.HasPrefix(arg, "--"):
			return rememberArgs{}, userErrorf("unknown flag %q; usage: /remember [--kind=<kind>] [--scope=<scope>] <text>", arg)
		default:
			break flags
		}
	}
	parsed.Content = strings.Join(args[index:], " ")
	if strings.TrimSpace(parsed.Content) == "" {
		return rememberArgs{}, userErrorf("usage: /remember [--kind=<kind>] [--scope=<scope>] <text>")
	}
	return parsed, nil
}

func (r *CommandRouter) remember(ctx context.Context, cc CommandContext, args []string) (CommandResult, error) {
	parsed, err := parseRemember(args)
	if err != nil {
		return CommandResult{}, err
	}
	scope, err := r.writeScope(cc, parsed.Visibility)
	if err != nil {
		return CommandResult{}, err
	}
	// Content is handed over raw: the store sanitizes, normalizes, derives the
	// keywords and computes the fingerprint itself, so there is exactly one place
	// where a secret can be caught and one definition of "the same fact".
	saved, err := r.memories.Save(ctx, memory.Memory{
		Kind: parsed.Kind, Scope: scope, Content: parsed.Content, Source: "explicit-command",
	})
	switch {
	case errors.Is(err, memory.ErrDuplicate):
		return reply("memory already exists: " + saved.ID), nil
	case errors.Is(err, memory.ErrSensitiveContent):
		// The offending text is never echoed, not even back to its author: this
		// message also lands in channel logs and Telegram history.
		return CommandResult{}, userErrorf("memory was not saved because it may contain sensitive content")
	case errors.Is(err, memory.ErrPromptControl):
		return CommandResult{}, userErrorf("memory was not saved because it looks like an instruction to the model")
	case errors.Is(err, memory.ErrEmptyContent):
		return CommandResult{}, userErrorf("memory text is empty")
	case errors.Is(err, memory.ErrDisabled):
		return CommandResult{}, userErrorf("memory is disabled")
	case err != nil:
		return CommandResult{}, fmt.Errorf("save memory: %w", err)
	}
	return reply(fmt.Sprintf("saved memory: %s (%s, %s)", saved.ID, saved.Kind, saved.Scope.Visibility)), nil
}

func (r *CommandRouter) memoryCommand(ctx context.Context, cc CommandContext, args []string) (CommandResult, error) {
	if len(args) == 0 {
		return CommandResult{}, userErrorf("usage: /memory list|search <text>|show <id>|forget <id>|pending|approve <id>|reject <id>")
	}
	if strings.TrimSpace(cc.WorkspaceID) == "" {
		return CommandResult{}, userErrorf("memory commands need a workspace; none is configured")
	}
	rest := args[1:]
	switch strings.ToLower(args[0]) {
	case "list":
		if len(rest) != 0 {
			return CommandResult{}, userErrorf("usage: /memory list")
		}
		items, err := r.memories.List(ctx, memory.Query{WorkspaceID: cc.WorkspaceID, UserID: cc.UserID})
		if err != nil {
			return CommandResult{}, r.memoryError("list memories", err)
		}
		return reply(formatMemories(items)), nil
	case "search":
		text := strings.Join(rest, " ")
		if strings.TrimSpace(text) == "" {
			return CommandResult{}, userErrorf("usage: /memory search <text>")
		}
		matches, err := r.memories.Search(ctx, memory.Query{
			Text: text, WorkspaceID: cc.WorkspaceID, UserID: cc.UserID, TopK: r.memoryTopK, Now: r.now(),
		})
		if err != nil {
			return CommandResult{}, r.memoryError("search memories", err)
		}
		if len(matches) == 0 {
			return reply("no memories matched"), nil
		}
		lines := make([]string, 0, len(matches))
		for _, match := range matches {
			lines = append(lines, fmt.Sprintf("%s  [%s]  score=%.3f  %s",
				match.Memory.ID, match.Memory.Kind, match.Score, truncateRunes(match.Memory.Content, listContentLimit)))
		}
		return reply(strings.Join(lines, "\n")), nil
	case "show":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /memory show <id>")
		}
		item, err := r.memories.Get(ctx, rest[0], r.readScope(cc))
		if err != nil {
			return CommandResult{}, r.notFoundOr("get memory", rest[0], err)
		}
		return reply(strings.Join([]string{
			"id: " + item.ID,
			"kind: " + string(item.Kind),
			"scope: " + string(item.Scope.Visibility),
			"source: " + item.Source,
			"used: " + fmt.Sprint(item.UseCount),
			"",
			truncateRunes(item.Content, showContentLimit),
		}, "\n")), nil
	case "forget":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /memory forget <id>")
		}
		if err := r.memories.Delete(ctx, rest[0], r.readScope(cc)); err != nil {
			// Unauthorized and missing collapse into the same message on purpose:
			// distinguishing them would let anyone enumerate other users' ids.
			return CommandResult{}, r.notFoundOr("forget memory", rest[0], err)
		}
		return reply("forgot memory: " + rest[0]), nil
	case "pending":
		if len(rest) != 0 {
			return CommandResult{}, userErrorf("usage: /memory pending")
		}
		items, err := r.candidates.ListPending(ctx, cc.WorkspaceID, cc.UserID)
		if err != nil {
			return CommandResult{}, r.memoryError("list candidates", err)
		}
		if len(items) == 0 {
			return reply("no pending memory proposals"), nil
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			lines = append(lines, fmt.Sprintf("%s  [%s]  %s", item.ID, item.Kind, truncateRunes(item.Content, listContentLimit)))
		}
		return reply(strings.Join(lines, "\n")), nil
	case "approve":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /memory approve <candidate-id>")
		}
		return r.approveCandidate(ctx, cc, rest[0])
	case "reject":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /memory reject <candidate-id>")
		}
		return r.rejectCandidate(ctx, cc, rest[0])
	default:
		return CommandResult{}, userErrorf("unknown /memory subcommand %q", args[0])
	}
}

// approveCandidate is deliberately not one atomic step: the memory is written
// first and the candidate is marked afterwards. If the write fails the proposal
// stays pending and can be approved again; if the marking fails the operator
// sees an error but no memory is lost. The reverse order could acknowledge an
// approval that never stored anything.
func (r *CommandRouter) approveCandidate(ctx context.Context, cc CommandContext, id string) (CommandResult, error) {
	scope := r.readScope(cc)
	candidate, err := r.candidates.Get(ctx, id, scope)
	if err != nil {
		return CommandResult{}, r.notFoundOr("get candidate", id, err)
	}
	switch candidate.Status {
	case memory.CandidateApproved:
		// Idempotent: a repeated approve reports the memory the first one created
		// instead of writing a second copy.
		return reply(fmt.Sprintf("candidate %s is already approved as %s", candidate.ID, candidate.MemoryID)), nil
	case memory.CandidateRejected:
		return CommandResult{}, userErrorf("candidate %s was already rejected", candidate.ID)
	case memory.CandidateExpired:
		return CommandResult{}, userErrorf("candidate %s expired; ask again and it will be proposed anew", candidate.ID)
	}
	saved, err := r.memories.Save(ctx, memory.Memory{
		Kind: candidate.Kind, Scope: candidate.Scope, Content: candidate.Content, Source: "approved-candidate",
	})
	switch {
	case errors.Is(err, memory.ErrDuplicate):
		// Already remembered by other means; the proposal is still resolved so it
		// stops showing up in the review queue.
	case errors.Is(err, memory.ErrSensitiveContent), errors.Is(err, memory.ErrPromptControl):
		return CommandResult{}, userErrorf("candidate %s was not stored because its content is not safe to keep", candidate.ID)
	case err != nil:
		return CommandResult{}, fmt.Errorf("save approved candidate: %w", err)
	}
	if _, err := r.candidates.Resolve(ctx, candidate.ID, scope, memory.CandidateApproved, saved.ID); err != nil {
		return CommandResult{}, fmt.Errorf("resolve candidate %q: %w", candidate.ID, err)
	}
	return reply(fmt.Sprintf("approved candidate %s as memory %s", candidate.ID, saved.ID)), nil
}

func (r *CommandRouter) rejectCandidate(ctx context.Context, cc CommandContext, id string) (CommandResult, error) {
	resolved, err := r.candidates.Resolve(ctx, id, r.readScope(cc), memory.CandidateRejected, "")
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) || errors.Is(err, memory.ErrDisabled) {
			return CommandResult{}, userErrorf("candidate %q does not exist", id)
		}
		if resolved.ID != "" {
			return CommandResult{}, userErrorf("candidate %s is already %s", resolved.ID, resolved.Status)
		}
		return CommandResult{}, fmt.Errorf("reject candidate %q: %w", id, err)
	}
	return reply("rejected candidate: " + resolved.ID), nil
}

// readScope is the identity a read or delete is performed with. Visibility is
// private here because that is the widest request a caller may make: the store
// still returns workspace items, and a private item only if the user matches.
func (r *CommandRouter) readScope(cc CommandContext) memory.Scope {
	return memory.Scope{WorkspaceID: cc.WorkspaceID, UserID: cc.UserID, Visibility: memory.VisibilityPrivate}
}

func (r *CommandRouter) writeScope(cc CommandContext, visibility memory.Visibility) (memory.Scope, error) {
	if strings.TrimSpace(cc.WorkspaceID) == "" {
		return memory.Scope{}, userErrorf("memory commands need a workspace; none is configured")
	}
	scope := memory.Scope{WorkspaceID: cc.WorkspaceID, Visibility: visibility}
	if visibility == memory.VisibilityPrivate {
		if strings.TrimSpace(cc.UserID) == "" {
			// A channel without user identity (the CLI) cannot own a private item.
			// Silently promoting it to workspace scope would share it with everyone.
			return memory.Scope{}, userErrorf("this channel has no user identity; use --scope=workspace")
		}
		scope.UserID = cc.UserID
	}
	return scope, nil
}

func (r *CommandRouter) memoryError(action string, err error) error {
	if errors.Is(err, memory.ErrDisabled) {
		return userErrorf("memory is disabled")
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (r *CommandRouter) notFoundOr(action, id string, err error) error {
	if errors.Is(err, memory.ErrNotFound) || errors.Is(err, memory.ErrDisabled) {
		return userErrorf("%q does not exist", id)
	}
	return fmt.Errorf("%s %q: %w", action, id, err)
}

func formatMemories(items []memory.Memory) string {
	if len(items) == 0 {
		return "no memories stored"
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("%s  [%s/%s]  %s",
			item.ID, item.Kind, item.Scope.Visibility, truncateRunes(item.Content, listContentLimit)))
	}
	return strings.Join(lines, "\n")
}
