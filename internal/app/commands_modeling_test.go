package app

// commands_modeling_test.go pins the properties of the /modeling family that are
// security properties rather than conveniences: where the scope comes from, what
// never appears in a reply, and which commands refuse to run without a human.
//
// The doubles below are deliberately dumb. The state machine has its own tests in
// internal/modeling; what is under test here is the command layer's contract with
// it, so a fake that records what it was called with proves more than a real
// pipeline would.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

// fakeModeling records the scope of every call and answers from a fixed project.
// Scope is recorded rather than asserted inline because several tests care about
// a different aspect of the same recording.
type fakeModeling struct {
	project modeling.Project
	body    []byte
	scopes  []modeling.Scope
	err     error
	request modeling.RunRequest
	reset   modeling.Stage
	reads   int
}

func (f *fakeModeling) Create(_ context.Context, title string, scope modeling.Scope) (modeling.Project, error) {
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return modeling.Project{}, f.err
	}
	project := f.project
	project.Title = title
	return project, nil
}

func (f *fakeModeling) List(_ context.Context, query modeling.Query) ([]modeling.Project, error) {
	f.scopes = append(f.scopes, modeling.Scope{WorkspaceID: query.WorkspaceID, UserID: query.UserID})
	if f.err != nil {
		return nil, f.err
	}
	return []modeling.Project{f.project}, nil
}

func (f *fakeModeling) Show(_ context.Context, _ string, scope modeling.Scope) (modeling.Project, error) {
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return modeling.Project{}, f.err
	}
	return f.project, nil
}

func (f *fakeModeling) Advance(_ context.Context, req modeling.RunRequest) (modeling.RunResult, error) {
	f.scopes = append(f.scopes, req.Scope)
	f.request = req
	if f.err != nil {
		return modeling.RunResult{}, f.err
	}
	// Emit a stage event through the emitter the command handed over, so a test
	// can check the adapter's encoding without a pipeline.
	_ = req.Events.StageEvent(context.Background(), modeling.StageEvent{
		Kind: modeling.EventStageCompleted, Project: f.project.ID, Stage: modeling.StagePlan,
		Text: "planned", OK: true,
	})
	return modeling.RunResult{Project: f.project, Stage: modeling.StagePlan, Summary: "planned"}, nil
}

func (f *fakeModeling) Reset(_ context.Context, _ string, stage modeling.Stage, scope modeling.Scope) (modeling.Project, error) {
	f.scopes = append(f.scopes, scope)
	f.reset = stage
	if f.err != nil {
		return modeling.Project{}, f.err
	}
	project := f.project
	project.Current = stage
	project.Status = modeling.StatusPending
	return project, nil
}

func (f *fakeModeling) Read(_ context.Context, _ string, _ modeling.ArtifactRef, scope modeling.Scope) ([]byte, error) {
	f.scopes = append(f.scopes, scope)
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return f.body, nil
}

// fakeApply records whether Apply was reached at all: the interactive gate is
// only meaningful if a denied command never calls it.
type fakeApply struct {
	plan    modeling.ApplyPlan
	result  modeling.ApplyResult
	planErr error
	err     error
	calls   int
}

func (f *fakeApply) Plan(context.Context, string, modeling.Scope) (modeling.ApplyPlan, error) {
	return f.plan, f.planErr
}

func (f *fakeApply) Apply(context.Context, string, modeling.Scope) (modeling.ApplyResult, error) {
	f.calls++
	return f.result, f.err
}

// testModelingProject is a project mid-pipeline: it has artifacts of three kinds
// and one evidence file, which is enough for show/diff/evidence to have something
// to get wrong.
func testModelingProject() modeling.Project {
	created := time.Unix(1000, 0).UTC()
	return modeling.Project{
		ID: "mp-0123456789abcdef", Title: "acme uart", WorkspaceID: testWorkspaceID, UserID: testUserID,
		Current: modeling.StageEmit, Status: modeling.StatusBlocked,
		Artifacts: map[modeling.Stage][]modeling.ArtifactRef{
			modeling.StageExtract: {{
				ID: "aaaaaaaaaaaaaaaa", Stage: modeling.StageExtract, Name: "reg-ir.json",
				Kind: modeling.KindRegIR, Bytes: 42, Digest: strings.Repeat("a", 64), Created: created,
			}},
			modeling.StageEmit: {{
				ID: "bbbbbbbbbbbbbbbb", Stage: modeling.StageEmit, Name: "device.c",
				Kind: modeling.KindCode, Bytes: 99, Digest: strings.Repeat("b", 64), Created: created,
			}, {
				ID: "cccccccccccccccc", Stage: modeling.StageEmit, Name: "device.diff",
				Kind: modeling.KindDiff, Bytes: 12, Digest: strings.Repeat("c", 64), Created: created,
			}},
		},
		Evidence: []modeling.ArtifactRef{{
			ID: "dddddddddddddddd", Stage: modeling.StageVerify, Name: "evidence.json",
			Kind: modeling.KindEvidence, Bytes: 7, Digest: strings.Repeat("d", 64), Created: created,
		}},
		UpdatedAt: created,
	}
}

// newModelingRouter builds a router whose only live dependencies are the two
// modeling seams; everything else is the same double the knowledge tests use.
func newModelingRouter(t *testing.T, pipeline ModelingCommands, applier ApplyCommands) *CommandRouter {
	t.Helper()
	store := &memoryStore{sessions: make(map[string]*session.Session)}
	factory, err := session.NewDefaultFactory(session.Defaults{ModelRef: llm.ModelRef{Provider: "ollama", Model: "test-model"}, SystemPrompt: "system"}, func() string { return "trace" })
	if err != nil {
		t.Fatal(err)
	}
	models := newTestModels(t)
	registry, err := session.NewRegistry(store, factory, models, "ollama")
	if err != nil {
		t.Fatal(err)
	}
	deps := testCommandDependencies(t, registry, testContextManager{}, models)
	deps.Modeling = pipeline
	deps.Apply = applier
	router, err := NewCommandRouter(deps, CommandConfig{MemoryTopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func runModeling(t *testing.T, router *CommandRouter, cc CommandContext, input string) (CommandResult, error) {
	t.Helper()
	return router.Execute(context.Background(), cc, parseForTest(t, input))
}

// TestModelingCommandsRejectForeignScope is the family's central rule: the scope
// is derived from CommandContext, so there is no argument a caller can use to
// name another workspace, and an id outside the caller's scope is reported as a
// missing project rather than as a permission error.
func TestModelingCommandsRejectForeignScope(t *testing.T) {
	pipeline := &fakeModeling{project: testModelingProject()}
	router := newModelingRouter(t, pipeline, &fakeApply{})
	cc := testCommandContext("cli:default")

	for _, input := range []string{
		"/modeling new acme uart",
		"/modeling list",
		"/modeling show mp-0123456789abcdef",
		"/modeling advance mp-0123456789abcdef",
	} {
		if _, err := runModeling(t, router, cc, input); err != nil {
			t.Fatalf("Execute(%q) error = %v", input, err)
		}
	}
	if len(pipeline.scopes) == 0 {
		t.Fatal("no call recorded")
	}
	for _, scope := range pipeline.scopes {
		if scope.WorkspaceID != testWorkspaceID || scope.UserID != testUserID {
			t.Fatalf("scope = %#v, want the command context's identity", scope)
		}
	}

	// A project the store will not show is a missing project, worded so that it
	// says nothing about whether the id exists somewhere else.
	pipeline.err = fmt.Errorf("load project: %w", modeling.ErrNotFound)
	_, err := runModeling(t, router, cc, "/modeling show mp-ffffffffffffffff")
	if err == nil || !channel.IsRecoverable(err) {
		t.Fatalf("error = %v", err)
	}
	if got := err.Error(); got != "no such modeling project" {
		t.Fatalf("message = %q", got)
	}

	// Without a workspace there is no scope to derive, so nothing runs.
	before := len(pipeline.scopes)
	pipeline.err = nil
	if _, err := runModeling(t, router, CommandContext{SessionKey: "cli:default"}, "/modeling list"); err == nil {
		t.Fatal("error = nil for a command without a workspace")
	}
	if len(pipeline.scopes) != before {
		t.Fatal("a command without a workspace reached the pipeline")
	}
}

// TestApplyRequiresInteractive keeps the review gate honest: on a channel where
// nobody can approve a write, apply must refuse before the applier is touched.
func TestApplyRequiresInteractive(t *testing.T) {
	applier := &fakeApply{
		plan: modeling.ApplyPlan{
			ProjectID: "mp-0123456789abcdef",
			Files:     []modeling.FileChange{{Path: "hw/misc/acme.c", Action: modeling.ApplyCreate}},
		},
		result: modeling.ApplyResult{ProjectID: "mp-0123456789abcdef", Written: []string{"hw/misc/acme.c"}},
	}
	router := newModelingRouter(t, &fakeModeling{project: testModelingProject()}, applier)

	_, err := runModeling(t, router, testCommandContext("tg:1"), "/modeling apply mp-0123456789abcdef")
	if err == nil || !channel.IsRecoverable(err) {
		t.Fatalf("error = %v", err)
	}
	if applier.calls != 0 {
		t.Fatalf("apply calls = %d on a non-interactive channel", applier.calls)
	}

	interactive := testCommandContext("cli:default")
	interactive.Interactive = true
	result, err := runModeling(t, router, interactive, "/modeling apply mp-0123456789abcdef")
	if err != nil {
		t.Fatalf("interactive apply error = %v", err)
	}
	if applier.calls != 1 || !strings.Contains(result.Text, "hw/misc/acme.c") {
		t.Fatalf("calls = %d, text = %q", applier.calls, result.Text)
	}

	// A partial apply is a failure that still has to report both lists, because
	// there is no rollback and only that report tells an operator what is on disk.
	applier.err = fmt.Errorf("write hw/misc/acme.c: %w", modeling.ErrApplyPartial)
	applier.result = modeling.ApplyResult{
		ProjectID: "mp-0123456789abcdef", Written: []string{"hw/misc/acme.c"},
		Skipped: []string{"hw/misc/meson.build"}, Partial: true, Reason: "apply_partial",
	}
	_, err = runModeling(t, router, interactive, "/modeling apply mp-0123456789abcdef")
	if err == nil {
		t.Fatal("partial apply error = nil")
	}
	if !strings.Contains(err.Error(), "wrote hw/misc/acme.c") || !strings.Contains(err.Error(), "skipped hw/misc/meson.build") {
		t.Fatalf("partial report = %q", err.Error())
	}
}

// TestResetRequiresConfirmation pins the one destructive read-side command: the
// user has to repeat the project id, which is the token they cannot type by
// habit.
func TestResetRequiresConfirmation(t *testing.T) {
	pipeline := &fakeModeling{project: testModelingProject()}
	router := newModelingRouter(t, pipeline, &fakeApply{})
	cc := testCommandContext("cli:default")

	for _, input := range []string{
		"/modeling reset mp-0123456789abcdef extract",
		"/modeling reset mp-0123456789abcdef extract --confirm=mp-ffffffffffffffff",
		"/modeling reset mp-0123456789abcdef extract mp-0123456789abcdef",
		"/modeling reset mp-0123456789abcdef nosuchstage --confirm=mp-0123456789abcdef",
	} {
		_, err := runModeling(t, router, cc, input)
		if err == nil || !channel.IsRecoverable(err) {
			t.Fatalf("Execute(%q) error = %v", input, err)
		}
	}
	if pipeline.reset != "" {
		t.Fatalf("reset reached the pipeline as %q", pipeline.reset)
	}

	if _, err := runModeling(t, router, cc, "/modeling reset mp-0123456789abcdef extract --confirm=mp-0123456789abcdef"); err != nil {
		t.Fatalf("confirmed reset error = %v", err)
	}
	if pipeline.reset != modeling.StageExtract {
		t.Fatalf("reset stage = %q", pipeline.reset)
	}
}

// TestShowDoesNotIncludeArtifactBodies keeps the status view a status view. Show
// is the command every channel runs most often, so inlining generated code there
// would publish model output about an untrusted datasheet by default.
func TestShowDoesNotIncludeArtifactBodies(t *testing.T) {
	project := testModelingProject()
	project.LastError = "schema_invalid"
	pipeline := &fakeModeling{project: project, body: []byte("SECRET-DIFF-BODY")}
	router := newModelingRouter(t, pipeline, &fakeApply{})

	result, err := runModeling(t, router, testCommandContext("cli:default"), "/modeling show mp-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, "SECRET-DIFF-BODY") {
		t.Fatalf("show inlined an artifact body: %q", result.Text)
	}
	for _, want := range []string{"device.diff", "kind=code", "bytes=99", "schema_invalid", "/modeling evidence"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("show output %q lacks %q", result.Text, want)
		}
	}
	// Show must not need Read at all; bytes it never asked for cannot leak.
	if pipeline.reads != 0 {
		t.Fatalf("show read %d artifact(s)", pipeline.reads)
	}
}

// TestErrorsAreMappedWithoutEcho is the "no payload in a reply" check. Every
// stage error here carries text that must not reach a channel — a datasheet line,
// a provider URL, tool stdout — and the reply may only name a category.
func TestErrorsAreMappedWithoutEcho(t *testing.T) {
	secrets := []string{"0xdeadbeef UART_CTRL", "https://provider.example/v1", "/private/tmp/datasheet.pdf"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"disabled", fmt.Errorf("%s: %w", secrets[0], modeling.ErrDisabled), "modeling is disabled"},
		{"not found", fmt.Errorf("%s: %w", secrets[2], modeling.ErrNotFound), "no such modeling project"},
		{"conflict", fmt.Errorf("%s: %w", secrets[0], modeling.ErrConflict), "project changed"},
		{"schema", fmt.Errorf("%s: %w", secrets[0], modeling.ErrSchemaInvalid), "schema_invalid"},
		{"model", fmt.Errorf("%s: %w", secrets[1], modeling.ErrModelFailed), "model_failed"},
		{"tool", fmt.Errorf("%s: %w", secrets[2], modeling.ErrToolDenied), "tool_denied"},
		{"unclassified", errors.New(secrets[0]), "stage_failed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pipeline := &fakeModeling{project: testModelingProject(), err: testCase.err}
			router := newModelingRouter(t, pipeline, &fakeApply{})
			_, err := runModeling(t, router, testCommandContext("cli:default"), "/modeling advance mp-0123456789abcdef")
			if err == nil || !channel.IsRecoverable(err) {
				t.Fatalf("error = %v", err)
			}
			message := err.Error()
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("message %q lacks %q", message, testCase.want)
			}
			for _, secret := range secrets {
				if strings.Contains(message, secret) {
					t.Fatalf("message %q echoed %q", message, secret)
				}
			}
		})
	}
}

// TestOversizeDiffFallsBackToPath pins the "rather lose the whole thing than half
// of it" rule: a diff too large for a channel is named, not truncated, because a
// reviewer cannot see that a truncated diff is incomplete.
func TestOversizeDiffFallsBackToPath(t *testing.T) {
	project := testModelingProject()
	small := project.Artifacts[modeling.StageEmit]
	pipeline := &fakeModeling{project: project, body: []byte("--- a/hw\n+++ b/hw\n")}
	router := newModelingRouter(t, pipeline, &fakeApply{})

	result, err := runModeling(t, router, testCommandContext("cli:default"), "/modeling diff mp-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "+++ b/hw") {
		t.Fatalf("small diff was not inlined: %q", result.Text)
	}

	// The same project with an oversize diff must name the artifact instead, and
	// must not read its bytes at all.
	oversize := append([]modeling.ArtifactRef(nil), small...)
	oversize[len(oversize)-1].Bytes = modelingDiffLimit + 1
	project.Artifacts[modeling.StageEmit] = oversize
	pipeline.project = project
	pipeline.body = []byte("MUST-NOT-APPEAR")

	result, err = runModeling(t, router, testCommandContext("cli:default"), "/modeling diff mp-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.reads != 1 {
		t.Fatalf("reads = %d; an oversize diff must not be read at all", pipeline.reads)
	}
	if strings.Contains(result.Text, "MUST-NOT-APPEAR") {
		t.Fatalf("oversize diff was still inlined: %q", result.Text)
	}
	for _, want := range []string{"too large", "device.diff", "cccccccccccccccc"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("fallback %q lacks %q", result.Text, want)
		}
	}

	// A project without a diff yet is a usage error, not an empty reply.
	project.Artifacts = map[modeling.Stage][]modeling.ArtifactRef{}
	pipeline.project = project
	if _, err := runModeling(t, router, testCommandContext("cli:default"), "/modeling diff mp-0123456789abcdef"); err == nil {
		t.Fatal("error = nil for a project with no diff")
	}
}

// TestUnknownSubcommandIsUserError checks the parse layer: a mistyped subcommand
// or flag is recoverable and teaches the whole family, and none of it reaches the
// pipeline.
func TestUnknownSubcommandIsUserError(t *testing.T) {
	pipeline := &fakeModeling{project: testModelingProject()}
	router := newModelingRouter(t, pipeline, &fakeApply{})
	cc := testCommandContext("cli:default")

	for _, input := range []string{
		"/modeling",
		"/modeling frobnicate",
		"/modeling new",
		"/modeling list extra",
		"/modeling show",
		"/modeling advance",
		"/modeling advance mp-0123456789abcdef --stage=nosuch",
		"/modeling advance mp-0123456789abcdef --wat=1",
		"/modeling advance mp-0123456789abcdef --source=",
		"/modeling advance not-an-id",
	} {
		_, err := runModeling(t, router, cc, input)
		if err == nil || !channel.IsRecoverable(err) {
			t.Fatalf("Execute(%q) error = %v", input, err)
		}
	}
	if len(pipeline.scopes) != 0 {
		t.Fatalf("%d rejected commands reached the pipeline", len(pipeline.scopes))
	}
}

// TestAdvanceParsesFlagsAndStreamsEvents covers the two remaining behaviours of
// advance: sources and request text are passed through as data, and the run is
// wrapped in the same run_started/run_completed envelope an agent turn uses.
func TestAdvanceParsesFlagsAndStreamsEvents(t *testing.T) {
	pipeline := &fakeModeling{project: testModelingProject()}
	router := newModelingRouter(t, pipeline, &fakeApply{})
	sink := &appTestSink{}
	emitter, err := runstream.NewEmitter(runstream.EmitterOptions{
		Sink: sink, Identity: runstream.Event{SessionKey: "cli:default", Channel: "cli"},
		Now: func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	cc := testCommandContext("cli:default")
	cc.Events = emitter

	if _, err := runModeling(t, router, cc,
		"/modeling advance mp-0123456789abcdef --stage=plan --source=docs/acme.pdf model the control block"); err != nil {
		t.Fatal(err)
	}
	if pipeline.request.Stage != modeling.StagePlan {
		t.Fatalf("stage = %q", pipeline.request.Stage)
	}
	if len(pipeline.request.Sources) != 1 || pipeline.request.Sources[0] != "docs/acme.pdf" {
		t.Fatalf("sources = %#v", pipeline.request.Sources)
	}
	if pipeline.request.Request != "model the control block" {
		t.Fatalf("request = %q", pipeline.request.Request)
	}
	types := make([]runstream.EventType, 0, len(sink.events))
	for _, event := range sink.events {
		types = append(types, event.Type)
	}
	want := []runstream.EventType{runstream.EventRunStarted, runstream.EventStageCompleted, runstream.EventRunCompleted}
	if len(types) != len(want) {
		t.Fatalf("events = %v", types)
	}
	for index := range want {
		if types[index] != want[index] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
}

// TestStageStreamEncodesCompletions pins the adapter's three-way encoding, which
// is the only place modeling's vocabulary becomes wire events. It is tested
// directly because ValidateEvent rejects the wrong combination, so a mistake here
// would silently drop the notification a channel needs most.
func TestStageStreamEncodesCompletions(t *testing.T) {
	cases := []struct {
		name  string
		event modeling.StageEvent
		check func(*testing.T, runstream.Event)
	}{
		{
			name:  "failed",
			event: modeling.StageEvent{Kind: modeling.EventStageCompleted, Stage: modeling.StageExtract, OK: false, Reason: "schema_invalid"},
			check: func(t *testing.T, got runstream.Event) {
				if got.ErrorKind != "schema_invalid" || got.Summary != "schema_invalid" {
					t.Fatalf("event = %#v", got)
				}
			},
		},
		{
			name:  "blocked",
			event: modeling.StageEvent{Kind: modeling.EventStageCompleted, Stage: modeling.StageEmit, OK: true, Blocked: true, Reason: "awaiting_apply"},
			check: func(t *testing.T, got runstream.Event) {
				if got.ErrorKind != "" || got.Summary != "awaiting_apply" {
					t.Fatalf("event = %#v", got)
				}
			},
		},
		{
			name:  "done",
			event: modeling.StageEvent{Kind: modeling.EventStageCompleted, Stage: modeling.StagePlan, OK: true, Text: "planned 3 registers"},
			check: func(t *testing.T, got runstream.Event) {
				if got.ErrorKind != "" || got.Summary != "" || got.Text != "planned 3 registers" {
					t.Fatalf("event = %#v", got)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sink := &appTestSink{}
			emitter, err := runstream.NewEmitter(runstream.EmitterOptions{
				Sink: sink, Identity: runstream.Event{SessionKey: "cli:default", Channel: "cli"},
				Now: func() time.Time { return time.Unix(1000, 0) },
			})
			if err != nil {
				t.Fatal(err)
			}
			stream := newStageStream(emitter)
			if err := stream.StageEvent(context.Background(), testCase.event); err != nil {
				t.Fatalf("StageEvent error = %v", err)
			}
			if len(sink.events) != 1 {
				t.Fatalf("events = %#v", sink.events)
			}
			testCase.check(t, sink.events[0])
		})
	}

	// A sink that fails must not fail the run: events are notifications, and a
	// stage that already committed artifacts cannot be undone by a dropped message.
	stream := newStageStream(failingEmitter{})
	if err := stream.StageEvent(context.Background(), modeling.StageEvent{
		Kind: modeling.EventStageStarted, Stage: modeling.StagePlan,
	}); err != nil {
		t.Fatalf("StageEvent error = %v with a failing sink", err)
	}
	stream.finish(context.Background(), errors.New("boom"))
}

type failingEmitter struct{}

func (failingEmitter) Emit(context.Context, runstream.Event) error {
	return errors.New("sink is down")
}
