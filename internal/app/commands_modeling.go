package app

// commands_modeling.go is the /modeling command family: the human entry point to
// the staged device-modeling pipeline. It follows the same four-step shape as
// commands_knowledge.go — parse, call a narrow interface, map errors, render —
// and deliberately contains no business logic: the state machine lives in
// internal/modeling, and this file may not reach around it.
//
// Three rules shape everything below and are worth stating once:
//
//   - Scope comes from CommandContext, never from an argument. There is no
//     --workspace and no --user flag, so a user cannot name somebody else's
//     project; an id from another scope is reported as "no such project".
//   - Errors are reported as categories, never as messages. A stage error may
//     quote a datasheet line, a provider URL or tool stdout, so every failure
//     here goes through modeling.Category before it becomes a reply.
//   - The command layer does not import os. Artifact bytes are read through
//     ModelingCommands.Read, which re-verifies the digest, and nothing here
//     knows where the artifact store keeps its files.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// ModelingCommands is the whole modeling capability as the command layer sees
// it: modeling.Runner. What it lacks is the point — there is no Save, no
// ArtifactStore.Stage and no Commit, so a command cannot change project state
// except by asking the pipeline to run a stage.
type ModelingCommands interface {
	Create(ctx context.Context, title string, scope modeling.Scope) (modeling.Project, error)
	List(ctx context.Context, query modeling.Query) ([]modeling.Project, error)
	Show(ctx context.Context, id string, scope modeling.Scope) (modeling.Project, error)
	Advance(ctx context.Context, req modeling.RunRequest) (modeling.RunResult, error)
	Reset(ctx context.Context, id string, stage modeling.Stage, scope modeling.Scope) (modeling.Project, error)
	Read(ctx context.Context, id string, ref modeling.ArtifactRef, scope modeling.Scope) ([]byte, error)
}

// ApplyCommands is separate from ModelingCommands because it is the only
// interface in the project whose implementation writes into a QEMU source tree.
// Keeping it apart means a build that has no tree to write to wires
// modeling.DisabledApplier here and leaves the read side fully functional.
type ApplyCommands interface {
	Plan(ctx context.Context, id string, scope modeling.Scope) (modeling.ApplyPlan, error)
	Apply(ctx context.Context, id string, scope modeling.Scope) (modeling.ApplyResult, error)
}

const (
	// modelingDiffLimit is how much of a diff is rendered inline. Beyond it the
	// reply names the artifact instead: a diff cut in half is worse than no diff,
	// because a reviewer cannot tell that the rest exists.
	modelingDiffLimit = 3000
	// modelingListLimit keeps /modeling list deliverable on a chat channel.
	modelingListLimit = 20
	// modelingTitleLimit bounds the one free-text field a project has.
	modelingTitleLimit = 120
	// modelingCommandLimit and modelingTailLimit bound what /modeling evidence
	// renders per record. The command is built from a template so it is short by
	// construction; the tail is already capped at 4 KiB by the verify stage, and
	// this second, smaller cap is what keeps a chat reply readable.
	modelingCommandLimit = 200
	modelingTailLimit    = 600
	// modelingUsage is repeated in the unknown-subcommand error so a mistyped
	// command teaches the whole family rather than just failing.
	modelingUsage = "usage: /modeling new <title>|list|show <id>|advance <id> [--stage=<stage>] [--source=<path>] [request]|diff <id>|apply <id>|evidence <id>|reset <id> <stage> --confirm=<id>"
)

// modelingCommand dispatches the family. It resolves the scope once, before any
// subcommand runs, so there is exactly one place that decides who is asking.
func (r *CommandRouter) modelingCommand(ctx context.Context, cc CommandContext, args []string) (CommandResult, error) {
	if len(args) == 0 {
		return CommandResult{}, userErrorf("%s", modelingUsage)
	}
	scope, err := r.modelingScope(cc)
	if err != nil {
		return CommandResult{}, err
	}
	// Attach the caller identity for the whole family, not only for the subcommands
	// that currently run tools. A stage reaches security.Executor through an adapter
	// that has no other way to learn the channel or whether anybody can answer an
	// approval prompt, and that adapter fails closed when the identity is missing.
	// Attaching it once here means a subcommand that starts using a tool later
	// cannot be the thing that discovers the omission.
	//
	// Note what is *not* taken from here: the authorization scope above is built
	// separately from cc.UserID. The session key travels only as an audit
	// correlation value and never becomes an identity decision.
	ctx = security.WithCaller(ctx, security.Caller{
		TraceID:     cc.TraceID,
		SessionKey:  cc.SessionKey,
		Channel:     cc.Channel,
		Interactive: cc.Interactive,
	})
	rest := args[1:]
	switch strings.ToLower(args[0]) {
	case "new":
		return r.modelingNew(ctx, scope, rest)
	case "list":
		if len(rest) != 0 {
			return CommandResult{}, userErrorf("usage: /modeling list")
		}
		return r.modelingList(ctx, scope)
	case "show":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /modeling show <id>")
		}
		return r.modelingShow(ctx, scope, rest[0])
	case "advance":
		return r.modelingAdvance(ctx, cc, scope, rest)
	case "diff":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /modeling diff <id>")
		}
		return r.modelingDiff(ctx, scope, rest[0])
	case "apply":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /modeling apply <id>")
		}
		return r.modelingApply(ctx, cc, scope, rest[0])
	case "evidence":
		if len(rest) != 1 {
			return CommandResult{}, userErrorf("usage: /modeling evidence <id>")
		}
		return r.modelingEvidence(ctx, scope, rest[0])
	case "reset":
		return r.modelingReset(ctx, scope, rest)
	default:
		return CommandResult{}, userErrorf("unknown /modeling subcommand %q; %s", args[0], modelingUsage)
	}
}

// modelingScope is the only place a modeling authorization tuple is built. A
// project may be owned (a chat user) or workspace-wide (the CLI, which has no
// user identity), which is why an empty UserID is legal here while it is not in
// writeScope: memory has a private visibility to protect, a project has an owner
// only if one existed when it was created.
func (r *CommandRouter) modelingScope(cc CommandContext) (modeling.Scope, error) {
	if strings.TrimSpace(cc.WorkspaceID) == "" {
		return modeling.Scope{}, userErrorf("modeling commands need a workspace; none is configured")
	}
	return modeling.Scope{WorkspaceID: cc.WorkspaceID, UserID: cc.UserID}, nil
}

// modelingNew creates a project at the first stage. The title is the only free
// text a project stores, so it is bounded here rather than trusted: it is
// rendered by /modeling list and written into a file name's neighbourhood.
func (r *CommandRouter) modelingNew(ctx context.Context, scope modeling.Scope, args []string) (CommandResult, error) {
	title := strings.Join(args, " ")
	if strings.TrimSpace(title) == "" {
		return CommandResult{}, userErrorf("usage: /modeling new <title>")
	}
	if len([]rune(title)) > modelingTitleLimit {
		return CommandResult{}, userErrorf("title is too long; keep it under %d characters", modelingTitleLimit)
	}
	project, err := r.modeling.Create(ctx, title, scope)
	if err != nil {
		return CommandResult{}, r.modelingError("create project", err)
	}
	return reply(fmt.Sprintf("created modeling project %s at stage %s (%s)", project.ID, project.Current, project.Status)), nil
}

// modelingList renders this scope's projects in the store's fixed order, so two
// invocations on two channels agree.
func (r *CommandRouter) modelingList(ctx context.Context, scope modeling.Scope) (CommandResult, error) {
	projects, err := r.modeling.List(ctx, modeling.Query{
		WorkspaceID: scope.WorkspaceID, UserID: scope.UserID, Limit: modelingListLimit,
	})
	if err != nil {
		return CommandResult{}, r.modelingError("list projects", err)
	}
	if len(projects) == 0 {
		return reply("no modeling projects"), nil
	}
	lines := make([]string, 0, len(projects))
	for _, project := range projects {
		lines = append(lines, fmt.Sprintf("%s  %s/%s  updated=%s  %s",
			project.ID, project.Current, project.Status,
			project.UpdatedAt.Format(time.RFC3339),
			truncateRunes(project.Title, listContentLimit)))
	}
	return reply(strings.Join(lines, "\n")), nil
}

// modelingShow is the status view: where the project is, what it produced and
// which category the last failure had. It lists artifact *references* only —
// names, kinds and sizes — because a body is what /modeling diff is for, and a
// status view that inlined generated code would put model output about an
// untrusted datasheet into every channel that asks for status.
func (r *CommandRouter) modelingShow(ctx context.Context, scope modeling.Scope, id string) (CommandResult, error) {
	project, err := r.modeling.Show(ctx, id, scope)
	if err != nil {
		return CommandResult{}, r.modelingError("show project", err)
	}
	lines := []string{
		"project: " + project.ID,
		"title: " + truncateRunes(project.Title, listContentLimit),
		fmt.Sprintf("stage: %s (%s)", project.Current, project.Status),
		"updated: " + project.UpdatedAt.Format(time.RFC3339),
	}
	if project.LastError != "" {
		// LastError is already a category; the store's Validate refuses anything
		// else, so rendering it verbatim cannot leak a message.
		lines = append(lines, "last error: "+project.LastError)
	}
	if project.Status == modeling.StatusRunning {
		// A project found running is either genuinely mid-stage or the record of a
		// crash. Saying so here is what turns "stuck" into "re-run it".
		lines = append(lines, "note: this stage is marked running; if no run is in flight, /modeling advance re-runs it")
	}
	lines = append(lines, "artifacts:")
	total := 0
	for _, stage := range modeling.StageOrder {
		for _, ref := range project.Artifacts[stage] {
			total++
			lines = append(lines, fmt.Sprintf("  %s/%s  kind=%s  bytes=%d  id=%s", stage, ref.Name, ref.Kind, ref.Bytes, ref.ID))
		}
	}
	if total == 0 {
		lines[len(lines)-1] = "artifacts: none"
	}
	if len(project.Evidence) > 0 {
		lines = append(lines, fmt.Sprintf("evidence: %d file(s); use /modeling evidence %s", len(project.Evidence), project.ID))
	}
	return reply(strings.Join(lines, "\n")), nil
}

// advanceArgs is one parsed /modeling advance. Only `--flag=value` forms are
// accepted, the same choice parseRemember makes: shell-style quoting would
// silently change how the request text is split.
type advanceArgs struct {
	ProjectID string
	Stage     modeling.Stage // empty means "the project's current stage"
	Sources   []string
	Request   string
}

func parseAdvance(args []string) (advanceArgs, error) {
	if len(args) == 0 {
		return advanceArgs{}, userErrorf("usage: /modeling advance <id> [--stage=<stage>] [--source=<path>] [request]")
	}
	parsed := advanceArgs{ProjectID: args[0]}
	index := 1
flags:
	for ; index < len(args); index++ {
		arg := args[index]
		switch {
		case strings.HasPrefix(arg, "--stage="):
			stage, err := modeling.ParseStage(strings.TrimPrefix(arg, "--stage="))
			if err != nil {
				return advanceArgs{}, userErrorf("unknown stage; use plan|extract|infer|emit|verify")
			}
			parsed.Stage = stage
		case strings.HasPrefix(arg, "--source="):
			source := strings.TrimSpace(strings.TrimPrefix(arg, "--source="))
			if source == "" {
				return advanceArgs{}, userErrorf("--source= needs a path")
			}
			// The path is passed through as data. A stage reads it with the audited
			// read tool, which is what confines it to the workspace — this layer
			// deliberately does not touch the filesystem to check it.
			parsed.Sources = append(parsed.Sources, source)
		case strings.HasPrefix(arg, "--"):
			return advanceArgs{}, userErrorf("unknown flag %q; use --stage= or --source=", arg)
		default:
			break flags
		}
	}
	parsed.Request = strings.Join(args[index:], " ")
	return parsed, nil
}

// modelingAdvance runs one stage. This is the only long-running command in the
// project, which is why it is also the only one that opens an event stream: a
// minutes-long stage that reported nothing until it finished would be
// indistinguishable from a hang on every channel.
func (r *CommandRouter) modelingAdvance(ctx context.Context, cc CommandContext, scope modeling.Scope, args []string) (CommandResult, error) {
	parsed, err := parseAdvance(args)
	if err != nil {
		return CommandResult{}, err
	}
	if err := modeling.ValidateProjectID(parsed.ProjectID); err != nil {
		// A malformed id and an unknown id are the same answer on purpose: ids
		// must not be probeable.
		return CommandResult{}, r.modelingError("advance project", err)
	}

	// The stream wraps the whole run in run_started/run_completed so a renderer
	// sees the same envelope it sees around an agent turn.
	stream := newStageStream(cc.Events)
	stream.start(ctx)
	result, runErr := r.modeling.Advance(ctx, modeling.RunRequest{
		ProjectID: parsed.ProjectID, Scope: scope, Stage: parsed.Stage,
		Request: parsed.Request, Sources: parsed.Sources, Events: stream,
	})
	stream.finish(ctx, runErr)
	if runErr != nil {
		return CommandResult{}, r.modelingError("advance project", runErr)
	}

	lines := []string{fmt.Sprintf("stage %s of %s: %s", result.Stage, result.Project.ID, result.Project.Status)}
	if result.Blocked {
		// Blocked is the normal end of Emit, so it is worded as a next step rather
		// than as a failure.
		lines = append(lines, "waiting for you: "+result.Reason)
	}
	if result.Summary != "" {
		lines = append(lines, result.Summary)
	}
	for _, ref := range result.Refs {
		lines = append(lines, fmt.Sprintf("  %s/%s  kind=%s  bytes=%d", ref.Stage, ref.Name, ref.Kind, ref.Bytes))
	}
	if !result.Blocked && result.Project.Status != modeling.StatusDone {
		lines = append(lines, fmt.Sprintf("next: /modeling advance %s", result.Project.ID))
	}
	return reply(strings.Join(lines, "\n")), nil
}

// modelingDiff prints the diff artifact Emit committed. It reads the same bytes
// an apply will write — the diff is a first-class artifact, not a rendering — so
// what a reviewer approves is what lands.
func (r *CommandRouter) modelingDiff(ctx context.Context, scope modeling.Scope, id string) (CommandResult, error) {
	project, err := r.modeling.Show(ctx, id, scope)
	if err != nil {
		return CommandResult{}, r.modelingError("show project", err)
	}
	ref, ok := findArtifact(project, modeling.KindDiff)
	if !ok {
		return CommandResult{}, userErrorf("project %s has no diff yet; run /modeling advance until the emit stage completes", project.ID)
	}
	// An oversize diff is reported as a reference, never truncated: half a diff
	// reads as a complete one, and a reviewer would approve the part they saw.
	if ref.Bytes > int64(modelingDiffLimit) {
		return reply(fmt.Sprintf(
			"diff %s is %d bytes, too large to show here; read artifact %s/%s (id=%s, digest=%s) from the project's artifact store",
			ref.Name, ref.Bytes, ref.Stage, ref.Name, ref.ID, ref.Digest)), nil
	}
	body, err := r.modeling.Read(ctx, project.ID, ref, scope)
	if err != nil {
		return CommandResult{}, r.modelingError("read diff", err)
	}
	return reply(string(body)), nil
}

// modelingEvidence lists what Verify proved. It stays a listing: build and qtest
// output is megabytes and lives in the artifact files, while the project keeps
// only the bounded tail.
func (r *CommandRouter) modelingEvidence(ctx context.Context, scope modeling.Scope, id string) (CommandResult, error) {
	project, err := r.modeling.Show(ctx, id, scope)
	if err != nil {
		return CommandResult{}, r.modelingError("show project", err)
	}
	if len(project.Evidence) == 0 {
		return reply(fmt.Sprintf("project %s has no evidence yet; the verify stage produces it", project.ID)), nil
	}
	lines := make([]string, 0, len(project.Evidence)+1)
	lines = append(lines, fmt.Sprintf("evidence of %s (stage %s/%s):", project.ID, project.Current, project.Status))
	for _, ref := range project.Evidence {
		lines = append(lines, fmt.Sprintf("  %s  bytes=%d  id=%s  created=%s",
			ref.Name, ref.Bytes, ref.ID, ref.Created.Format(time.RFC3339)))
	}
	// evidence.json is the one evidence artifact this layer understands, so its
	// records are decoded into readable lines. A decode failure is not fatal: the
	// listing above is still the useful part, and the artifact is still on disk.
	lines = append(lines, verifyRecordLines(ctx, r, project, scope)...)
	if project.LastError != "" {
		lines = append(lines, "outcome: "+project.LastError)
	}
	return reply(strings.Join(lines, "\n")), nil
}

// verifyRecordLines renders the verify stage's own report. Only the command, the
// exit code and the bounded tail are shown — the full build log stays in its
// artifact, because it is routinely megabytes and a chat channel is not a log
// viewer.
func verifyRecordLines(ctx context.Context, r *CommandRouter, project modeling.Project, scope modeling.Scope) []string {
	ref, ok := findEvidenceArtifact(project, modeling.ArtifactEvidenceName)
	if !ok {
		return nil
	}
	body, err := r.modeling.Read(ctx, project.ID, ref, scope)
	if err != nil {
		// The category, not the error: this reply goes to a channel.
		return []string{"  (evidence.json could not be read: " + modeling.Category(err) + ")"}
	}
	device, records, err := modeling.DecodeEvidence(body)
	if err != nil {
		return []string{"  (evidence.json was written by another version: " + modeling.Category(err) + ")"}
	}
	lines := make([]string, 0, len(records)*2+1)
	lines = append(lines, "verified device: "+device)
	for _, record := range records {
		outcome := "failed"
		if record.OK {
			outcome = "ok"
		}
		lines = append(lines, fmt.Sprintf("  %s: %s (exit %d) at %s",
			record.Kind, outcome, record.ExitCode, record.At.Format(time.RFC3339)))
		lines = append(lines, "    command: "+truncateRunes(record.Command, modelingCommandLimit))
		if tail := strings.TrimSpace(record.Tail); tail != "" && !record.OK {
			// Only a failure's tail is worth the space: on success nobody reads it.
			lines = append(lines, "    tail: "+truncateRunes(tail, modelingTailLimit))
		}
	}
	return lines
}

// findEvidenceArtifact looks one evidence artifact up by name. Evidence refs are
// kept in their own list on the project, so this does not go through
// findArtifact's kind-based search.
func findEvidenceArtifact(project modeling.Project, name string) (modeling.ArtifactRef, bool) {
	var found modeling.ArtifactRef
	ok := false
	for _, ref := range project.Evidence {
		if ref.Name == name {
			found, ok = ref, true
		}
	}
	return found, ok
}

// modelingApply is the one irreversible command in the family, and the only path
// that changes a QEMU source tree.
//
// Two gates stand in front of it. Interactive is required because the whole
// safety argument of the pipeline is "a human read the diff": on a channel where
// nobody can be asked, an approval prompt would either block forever or be
// auto-denied, and neither is a review. The second gate is inside the applier —
// every write goes through security.Executor, so the policy and the audit log
// apply to a generated device exactly as they do to a bash call.
func (r *CommandRouter) modelingApply(ctx context.Context, cc CommandContext, scope modeling.Scope, id string) (CommandResult, error) {
	if !cc.Interactive {
		return CommandResult{}, userErrorf("apply needs an interactive channel: somebody has to read the diff and approve each write")
	}
	plan, err := r.apply.Plan(ctx, id, scope)
	if err != nil {
		return CommandResult{}, r.modelingError("plan apply", err)
	}
	// Say what is about to happen before it happens. This goes out as a progress
	// event rather than as the reply, because the reply arrives after the
	// approval prompts it is meant to prepare the reviewer for.
	stream := newStageStream(cc.Events)
	stream.start(ctx)
	stream.progress(ctx, modeling.StageEmit, planSummary(plan))
	result, applyErr := r.apply.Apply(ctx, id, scope)
	stream.finish(ctx, applyErr)

	// A partial apply is reported in full even though it failed: the exact list of
	// written and skipped files is the only thing that lets an operator finish or
	// revert by hand, and there is deliberately no automatic rollback.
	if errors.Is(applyErr, modeling.ErrApplyPartial) {
		return CommandResult{}, userErrorf("apply stopped after a failure (%s)\n%s",
			modeling.Category(applyErr), applyReport(result))
	}
	if applyErr != nil {
		return CommandResult{}, r.modelingError("apply project", applyErr)
	}
	return reply(applyReport(result)), nil
}

// planSummary is the one-line "this is what will be written" notice. It names
// paths and actions only: the bytes are in the diff the reviewer already has.
func planSummary(plan modeling.ApplyPlan) string {
	parts := make([]string, 0, len(plan.Files))
	for _, change := range plan.Files {
		parts = append(parts, fmt.Sprintf("%s %s", change.Action, change.Path))
	}
	return truncateRunes(fmt.Sprintf("apply will touch %d file(s): %s", len(plan.Files), strings.Join(parts, ", ")), runstream.MaxEventText-1)
}

func applyReport(result modeling.ApplyResult) string {
	lines := []string{fmt.Sprintf("applied %d file(s) of project %s", len(result.Written), result.ProjectID)}
	for _, path := range result.Written {
		lines = append(lines, "  wrote "+path)
	}
	for _, path := range result.Skipped {
		lines = append(lines, "  skipped "+path)
	}
	if result.Reason != "" {
		lines = append(lines, "reason: "+result.Reason)
	}
	return strings.Join(lines, "\n")
}

// modelingReset rewinds a project to a stage and drops that stage's artifacts
// plus everything after it. The confirmation word is the project id itself: it is
// the one token a user cannot supply by muscle memory, and repeating it proves
// they know which project they are about to invalidate.
func (r *CommandRouter) modelingReset(ctx context.Context, scope modeling.Scope, args []string) (CommandResult, error) {
	if len(args) != 3 {
		return CommandResult{}, userErrorf("usage: /modeling reset <id> <stage> --confirm=<id>")
	}
	id, rawStage, confirm := args[0], args[1], args[2]
	stage, err := modeling.ParseStage(rawStage)
	if err != nil {
		return CommandResult{}, userErrorf("unknown stage; use plan|extract|infer|emit|verify")
	}
	if !strings.HasPrefix(confirm, "--confirm=") {
		return CommandResult{}, userErrorf("usage: /modeling reset <id> <stage> --confirm=<id>")
	}
	if strings.TrimPrefix(confirm, "--confirm=") != id {
		return CommandResult{}, userErrorf("reset drops the artifacts of stage %s and every later stage; repeat the project id as --confirm=%s to proceed", stage, id)
	}
	project, err := r.modeling.Reset(ctx, id, stage, scope)
	if err != nil {
		return CommandResult{}, r.modelingError("reset project", err)
	}
	return reply(fmt.Sprintf("reset %s to stage %s (%s); artifacts of %s and later stages are no longer referenced",
		project.ID, project.Current, project.Status, stage)), nil
}

// findArtifact returns the newest artifact of a kind. Search order is stage
// order, so "the diff" means the one the emit stage produced even if some other
// stage ever committed the same kind.
func findArtifact(project modeling.Project, kind modeling.Kind) (modeling.ArtifactRef, bool) {
	var found modeling.ArtifactRef
	ok := false
	for _, stage := range modeling.StageOrder {
		for _, ref := range project.Artifacts[stage] {
			if ref.Kind == kind {
				found, ok = ref, true
			}
		}
	}
	return found, ok
}

// modelingError is the single error-mapping point of the family. It returns a
// UserError in every case, including for internal failures, because the
// alternative — propagating the wrapped error to the channel — is exactly how a
// datasheet fragment or a provider response would end up in a chat log. The
// category is the payload; the project's own LastError holds the same value, so
// nothing is lost for diagnosis.
func (r *CommandRouter) modelingError(action string, err error) error {
	switch {
	case errors.Is(err, modeling.ErrDisabled):
		return userErrorf("modeling is disabled; set QEMU_AGENT_MODELING_ENABLED=true and restart to use it")
	case errors.Is(err, modeling.ErrApplyUnavailable):
		return userErrorf("apply is unavailable: this build has no QEMU source tree configured")
	case errors.Is(err, modeling.ErrNotFound):
		return userErrorf("no such modeling project")
	case errors.Is(err, modeling.ErrConflict):
		return userErrorf("project changed while this command ran; retry")
	case errors.Is(err, modeling.ErrCapacity):
		return userErrorf("this workspace has too many modeling projects; delete one first")
	}
	return userErrorf("%s failed: %s", action, modeling.Category(err))
}

// stageStream adapts modeling.EventEmitter to runstream.Emitter, and it is the
// only place the two protocols meet.
//
// It exists because the modeling package must not know about runstream: a stage
// emits a modeling.StageEvent — a value in its own vocabulary — and this type
// decides how that becomes a wire event. It also supplies the run envelope. A
// /modeling advance has no model turns, but a renderer still expects one
// run_started and exactly one run_completed/run_failed around whatever happens,
// so the envelope is opened and closed here rather than inside the pipeline,
// which does not know it is being driven by a command.
//
// Every sink error is swallowed. That is the protocol's own rule — events are
// notifications, not a data channel — and it matters most here: a Telegram edit
// that fails must not abort a stage that has already spent two minutes and is
// about to commit artifacts.
type stageStream struct {
	events runstream.Emitter
}

var _ modeling.EventEmitter = (*stageStream)(nil)

func newStageStream(events runstream.Emitter) *stageStream {
	return &stageStream{events: runstream.NormalizeEmitter(events)}
}

// start opens the envelope. It is separate from the constructor so the caller
// controls when the run becomes visible: modelingApply, for instance, opens it
// only after Plan succeeded, so a rejected plan produces no half-run.
func (s *stageStream) start(ctx context.Context) {
	s.send(ctx, runstream.Event{Type: runstream.EventRunStarted})
}

// progress emits a command-authored line, used by apply to announce a plan. It
// takes the stage explicitly because the command layer, unlike a stage runner,
// has no ambient notion of which step it is narrating.
func (s *stageStream) progress(ctx context.Context, stage modeling.Stage, text string) {
	line := truncateRunes(text, runstream.MaxEventText)
	if line == "" {
		// stage_progress with empty text is invalid by contract; dropping it is
		// better than emitting an event a renderer is entitled to hard-fail on.
		return
	}
	s.send(ctx, runstream.Event{Type: runstream.EventStageProgress, Stage: string(stage), Text: line})
}

// StageEvent translates one pipeline notification. The three-way encoding of a
// completion is the interesting part, and it mirrors ValidateEvent's rules:
//
//	failed  -> ErrorKind and Summary both carry the category
//	blocked -> Summary carries the reason, ErrorKind stays empty
//	done    -> Text carries the summary line, nothing else
func (s *stageStream) StageEvent(ctx context.Context, event modeling.StageEvent) error {
	out := runstream.Event{Stage: string(event.Stage)}
	switch event.Kind {
	case modeling.EventStageStarted:
		out.Type = runstream.EventStageStarted
	case modeling.EventStageProgress:
		out.Type = runstream.EventStageProgress
		out.Text = truncateRunes(event.Text, runstream.MaxEventText)
		if out.Text == "" {
			return nil
		}
	case modeling.EventStageCompleted:
		out.Type = runstream.EventStageCompleted
		switch {
		case !event.OK:
			// Reason is already a category; falling back to a constant keeps the
			// event valid even if a producer forgot to set one, because the
			// alternative is a dropped completion.
			category := reasonOrDefault(event.Reason, "stage_failed")
			out.ErrorKind, out.Summary = category, category
		case event.Blocked:
			out.Summary = truncateRunes(reasonOrDefault(event.Reason, "awaiting_human"), runstream.MaxEventText)
		default:
			out.Text = truncateRunes(event.Text, runstream.MaxEventText)
		}
	default:
		// An unknown kind is dropped rather than guessed: emitting it as some other
		// type would tell a renderer something that did not happen.
		return nil
	}
	s.send(ctx, out)
	return nil
}

// finish closes the envelope exactly once per run. The failure branch carries
// the classified category in both ErrorKind and Summary because the protocol
// requires a public summary and a category is the only text this layer is
// allowed to publish.
func (s *stageStream) finish(ctx context.Context, runErr error) {
	if runErr == nil {
		s.send(ctx, runstream.Event{Type: runstream.EventRunCompleted})
		return
	}
	category := reasonOrDefault(modeling.Category(runErr), "stage_failed")
	s.send(ctx, runstream.Event{Type: runstream.EventRunFailed, ErrorKind: category, Summary: category})
}

// send is the single swallow point, so no branch above has to decide again
// whether a lost notification may fail a run.
func (s *stageStream) send(ctx context.Context, event runstream.Event) {
	_ = s.events.Emit(ctx, event)
}

func reasonOrDefault(reason, fallback string) string {
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		return trimmed
	}
	return fallback
}
