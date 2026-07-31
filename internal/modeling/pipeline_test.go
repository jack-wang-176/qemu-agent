package modeling

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStage is a StageRunner whose behaviour a test dictates. It exists so the
// pipeline can be exercised end to end without a model, a tool or a qemu tree.
type fakeStage struct {
	stage  Stage
	run    func(ctx context.Context, in StageInput) (StageOutput, error)
	inputs []StageInput
}

func (f *fakeStage) Name() Stage { return f.stage }

func (f *fakeStage) Run(ctx context.Context, in StageInput) (StageOutput, error) {
	f.inputs = append(f.inputs, in)
	if f.run == nil {
		return StageOutput{
			Artifacts: []Draft{draft(f.stage, "out.md", KindPlan, "output of "+string(f.stage))},
			Summary:   "ran " + string(f.stage),
		}, nil
	}
	return f.run(ctx, in)
}

// recordingEmitter keeps the event sequence so a test can assert on ordering.
type recordingEmitter struct {
	mu     sync.Mutex
	events []StageEvent
	err    error
}

func (r *recordingEmitter) StageEvent(_ context.Context, event StageEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return r.err
}

func (r *recordingEmitter) kinds() []EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	kinds := make([]EventKind, 0, len(r.events))
	for _, event := range r.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

// pipelineHarness bundles the three stores a pipeline needs plus the fakes a test
// wants to reach into afterwards.
type pipelineHarness struct {
	pipeline  *Pipeline
	projects  *FileProjectStore
	artifacts *FileArtifactStore
	stages    map[Stage]*fakeStage
	root      string
	clock     *time.Time
}

func newHarness(t *testing.T, timeout time.Duration, overrides map[Stage]func(context.Context, StageInput) (StageOutput, error)) *pipelineHarness {
	t.Helper()
	root := t.TempDir()
	projects := openStore(t, filepath.Join(root, "projects"), 10)
	artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 4096, 1<<20)

	now := at(1000)
	harness := &pipelineHarness{projects: projects, artifacts: artifacts, root: root, clock: &now, stages: map[Stage]*fakeStage{}}
	runners := make([]StageRunner, 0, len(StageOrder))
	for _, stage := range StageOrder {
		fake := &fakeStage{stage: stage, run: overrides[stage]}
		harness.stages[stage] = fake
		runners = append(runners, fake)
	}
	pipeline, err := NewPipeline(PipelineOptions{
		Projects: projects, Artifacts: artifacts, Stages: runners,
		Workspace: filepath.Join(root, "workspace"), Timeout: timeout,
		Now:    func() time.Time { return *harness.clock },
		Logger: testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.pipeline = pipeline
	return harness
}

// tick moves the injected clock so consecutive saves get distinct timestamps.
func (h *pipelineHarness) tick(seconds int64) {
	*h.clock = h.clock.Add(time.Duration(seconds) * time.Second)
}

func (h *pipelineHarness) newProject(t *testing.T) Project {
	t.Helper()
	project, err := h.pipeline.Create(context.Background(), "pl011 clone", Scope{WorkspaceID: "ws-1111", UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func scopeOf(project Project) Scope {
	return Scope{WorkspaceID: project.WorkspaceID, UserID: project.UserID}
}

func TestAdvanceRunsStagesInOrder(t *testing.T) {
	harness := newHarness(t, time.Minute, nil)
	project := harness.newProject(t)
	scope := scopeOf(project)

	// A stage two steps ahead is refused before anything is written.
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{
		ProjectID: project.ID, Scope: scope, Stage: StageInfer,
	}); err == nil {
		t.Fatal("skipped a stage")
	}
	stored, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != project.Revision || stored.Status != StatusPending {
		t.Fatalf("a refused advance changed the project: %#v", stored)
	}

	// Advancing without naming a stage runs the current one, five times over.
	for index, stage := range StageOrder {
		harness.tick(10)
		result, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope})
		if err != nil {
			t.Fatalf("advance %q: %v", stage, err)
		}
		if result.Stage != stage {
			t.Fatalf("advance %d ran %q", index, result.Stage)
		}
		if len(result.Refs) != 1 || result.Refs[0].Stage != stage {
			t.Fatalf("refs = %#v", result.Refs)
		}
	}
	final, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if final.Current != LastStage() || final.Status != StatusDone {
		t.Fatalf("project = %q/%q", final.Current, final.Status)
	}
	// Every stage saw only the earlier stages' refs, which is what keeps a re-run
	// reproducible.
	inferInputs := harness.stages[StageInfer].inputs
	if len(inferInputs) != 1 {
		t.Fatalf("infer ran %d times", len(inferInputs))
	}
	if _, exists := inferInputs[0].Inputs[StageInfer]; exists {
		t.Fatal("a stage saw its own output as input")
	}
	if _, exists := inferInputs[0].Inputs[StagePlan]; !exists {
		t.Fatalf("infer inputs = %#v", inferInputs[0].Inputs)
	}
}

func TestStageFailureLeavesProjectBlockedAndNoFinalArtifacts(t *testing.T) {
	harness := newHarness(t, time.Minute, map[Stage]func(context.Context, StageInput) (StageOutput, error){
		StagePlan: func(context.Context, StageInput) (StageOutput, error) {
			return StageOutput{}, fmt.Errorf("plan: %w", ErrModelFailed)
		},
	})
	project := harness.newProject(t)
	scope := scopeOf(project)

	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope}); err == nil {
		t.Fatal("a failing stage reported success")
	}
	stored, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusBlocked || stored.LastError != "model_failed" {
		t.Fatalf("project = %q/%q", stored.Status, stored.LastError)
	}
	if stored.Current != StagePlan {
		t.Fatalf("current = %q; a failure must not advance", stored.Current)
	}
	if _, exists := stored.Artifacts[StagePlan]; exists {
		t.Fatalf("a failed stage recorded artifacts: %#v", stored.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(harness.root, "artifacts", project.ID, string(StagePlan))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed stage committed files: %v", err)
	}
	// A blocked stage may be retried without creating a second project.
	harness.tick(10)
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope, Stage: StagePlan}); err == nil {
		t.Fatal("the retry silently succeeded")
	}
}

func TestStageTimeoutClassifiedAndRecorded(t *testing.T) {
	harness := newHarness(t, 20*time.Millisecond, map[Stage]func(context.Context, StageInput) (StageOutput, error){
		StagePlan: func(ctx context.Context, _ StageInput) (StageOutput, error) {
			// A stage that respects its context is the contract; the pipeline's job
			// is to make sure the project does not stay "running" either way.
			<-ctx.Done()
			return StageOutput{}, ctx.Err()
		},
	})
	project := harness.newProject(t)
	scope := scopeOf(project)

	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope}); err == nil {
		t.Fatal("a timed-out stage reported success")
	}
	stored, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastError != "stage_timeout" || stored.Status != StatusBlocked {
		t.Fatalf("project = %q/%q", stored.Status, stored.LastError)
	}
}

func TestCanceledAdvanceLeavesBlockedNotRunning(t *testing.T) {
	harness := newHarness(t, time.Minute, map[Stage]func(context.Context, StageInput) (StageOutput, error){
		StagePlan: func(ctx context.Context, _ StageInput) (StageOutput, error) {
			<-ctx.Done()
			return StageOutput{}, ctx.Err()
		},
	})
	project := harness.newProject(t)
	scope := scopeOf(project)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := harness.pipeline.Advance(ctx, RunRequest{ProjectID: project.ID, Scope: scope}); err == nil {
		t.Fatal("a canceled advance reported success")
	}
	// The state write must outlive the canceled request, or a Ctrl-C would be
	// indistinguishable from a crash.
	stored, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusBlocked || stored.LastError != "canceled" {
		t.Fatalf("project = %q/%q", stored.Status, stored.LastError)
	}
}

func TestArtifactRejectionDiscardsStaging(t *testing.T) {
	harness := newHarness(t, time.Minute, map[Stage]func(context.Context, StageInput) (StageOutput, error){
		StagePlan: func(context.Context, StageInput) (StageOutput, error) {
			return StageOutput{
				Artifacts: []Draft{draft(StagePlan, "../escape.md", KindPlan, "x")},
				Summary:   "wrote a plan",
			}, nil
		},
	})
	project := harness.newProject(t)
	scope := scopeOf(project)

	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope}); err == nil {
		t.Fatal("an unsafe draft was accepted")
	}
	stored, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusBlocked || stored.LastError != "artifact_rejected" {
		t.Fatalf("project = %q/%q", stored.Status, stored.LastError)
	}
	if _, err := os.Stat(filepath.Join(harness.root, "artifacts", project.ID, "staging")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived a rejected batch: %v", err)
	}
	// Evidence of the wrong kind is refused for the same reason: the pipeline, not
	// the filesystem, decides what may be presented as proof.
	harness.stages[StagePlan].run = func(context.Context, StageInput) (StageOutput, error) {
		return StageOutput{Evidence: []Draft{draft(StagePlan, "build.log", KindCode, "ok")}}, nil
	}
	harness.tick(10)
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope}); err == nil {
		t.Fatal("a non-evidence draft entered the evidence list")
	}
}

func TestRerunSameStageIsIdempotent(t *testing.T) {
	harness := newHarness(t, time.Minute, nil)
	project := harness.newProject(t)
	scope := scopeOf(project)

	// Two full runs of the same stage. Reset is what makes the second one legal —
	// a completed stage has already moved Current forward, and a backward step is
	// deliberately explicit.
	for run := range 2 {
		if run > 0 {
			if _, err := harness.pipeline.Reset(context.Background(), project.ID, StagePlan, scope); err != nil {
				t.Fatal(err)
			}
		}
		harness.tick(10)
		if _, err := harness.pipeline.Advance(context.Background(), RunRequest{
			ProjectID: project.ID, Scope: scope, Stage: StagePlan,
		}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(harness.root, "artifacts", project.ID, string(StagePlan)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a re-run produced %d files; content addressing must collapse them", len(entries))
	}
	// The manifest still records both runs: the file is deduplicated, the audit
	// trail is not.
	manifest, err := os.ReadFile(filepath.Join(harness.root, "artifacts", project.ID, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(manifest)), "\n") + 1; lines != 2 {
		t.Fatalf("manifest has %d lines", lines)
	}
	stored, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Artifacts[StagePlan]) != 1 {
		t.Fatalf("refs = %#v", stored.Artifacts[StagePlan])
	}
}

func TestCrashAfterBeginIsRecoverable(t *testing.T) {
	harness := newHarness(t, time.Minute, nil)
	project := harness.newProject(t)
	scope := scopeOf(project)

	// Simulate the crash: the project on disk says a stage is running, because
	// Begin is persisted before the stage runs.
	crashed := project.Clone()
	if err := crashed.Begin(StagePlan, at(1500)); err != nil {
		t.Fatal(err)
	}
	crashed.Revision++
	if _, err := harness.projects.Save(context.Background(), crashed); err != nil {
		t.Fatal(err)
	}
	// A running project refuses a fresh advance...
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope}); err == nil {
		t.Fatal("advanced a project that was still marked running")
	}
	// ...and Reset is the documented way back, after which the redo works.
	if _, err := harness.pipeline.Reset(context.Background(), project.ID, StagePlan, scope); err != nil {
		t.Fatal(err)
	}
	harness.tick(10)
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope}); err != nil {
		t.Fatalf("redo after reset: %v", err)
	}
}

func TestStageCannotSeeOtherProjectsArtifacts(t *testing.T) {
	harness := newHarness(t, time.Minute, nil)
	scope := Scope{WorkspaceID: "ws-1111", UserID: "alice"}
	first := harness.newProject(t)
	second, err := harness.pipeline.Create(context.Background(), "other device", scope)
	if err != nil {
		t.Fatal(err)
	}

	harness.tick(10)
	firstRun, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: first.ID, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	stolen := firstRun.Refs[0]

	// The Open closure a stage receives is bound to its own project.
	harness.stages[StagePlan].run = func(_ context.Context, in StageInput) (StageOutput, error) {
		if _, err := in.Open(stolen); err == nil {
			return StageOutput{}, errors.New("a stage read another project's artifact")
		}
		return StageOutput{Artifacts: []Draft{draft(StagePlan, "plan.md", KindPlan, "mine")}, Summary: "ok"}, nil
	}
	harness.tick(10)
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: second.ID, Scope: scope}); err != nil {
		t.Fatal(err)
	}
	// Read refuses the same ref through the command path, and says "not found"
	// rather than "not yours".
	if _, err := harness.pipeline.Read(context.Background(), second.ID, stolen, scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project read = %v", err)
	}
	// Another workspace cannot even see the project.
	if _, err := harness.pipeline.Read(context.Background(), first.ID, stolen, Scope{WorkspaceID: "ws-2222", UserID: "alice"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace read = %v", err)
	}
	body, err := harness.pipeline.Read(context.Background(), first.ID, stolen, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("the owner could not read its own artifact")
	}
}

func TestErrorMessagesDoNotEchoToolOutput(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	harness := newHarness(t, time.Minute, map[Stage]func(context.Context, StageInput) (StageOutput, error){
		StagePlan: func(context.Context, StageInput) (StageOutput, error) {
			return StageOutput{}, fmt.Errorf("bash said %s: %w", secret, ErrBuildFailed)
		},
	})
	project := harness.newProject(t)
	scope := scopeOf(project)

	_, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope})
	if err == nil {
		t.Fatal("expected the stage to fail")
	}
	// The returned error still wraps the cause — the caller is inside the trust
	// boundary — but the persisted state must be a bare category.
	stored, showErr := harness.pipeline.Show(context.Background(), project.ID, scope)
	if showErr != nil {
		t.Fatal(showErr)
	}
	if stored.LastError != "build_failed" {
		t.Fatalf("last error = %q", stored.LastError)
	}
	if strings.Contains(stored.LastError, secret) {
		t.Fatal("the persisted category echoed tool output")
	}
	// classify is the only translation step, and it can only produce constants.
	if got := classify(err); got != "build_failed" {
		t.Fatalf("classify = %q", got)
	}
}

func TestBlockedStageKeepsArtifactsAndStays(t *testing.T) {
	harness := newHarness(t, time.Minute, map[Stage]func(context.Context, StageInput) (StageOutput, error){
		StagePlan: func(context.Context, StageInput) (StageOutput, error) {
			return StageOutput{
				Artifacts: []Draft{draft(StagePlan, "device.diff", KindDiff, "--- a\n+++ b\n")},
				Summary:   "generated a diff; review it with /modeling diff",
				Blocked:   true, Reason: "awaiting_apply",
			}, nil
		},
	})
	project := harness.newProject(t)
	scope := scopeOf(project)

	result, err := harness.pipeline.Advance(context.Background(), RunRequest{ProjectID: project.ID, Scope: scope})
	if err != nil {
		t.Fatalf("blocked is not a failure: %v", err)
	}
	if !result.Blocked || result.Reason != "awaiting_apply" {
		t.Fatalf("result = %#v", result)
	}
	stored, err := harness.pipeline.Show(context.Background(), project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Current != StagePlan || stored.Status != StatusBlocked {
		t.Fatalf("project = %q/%q", stored.Current, stored.Status)
	}
	// The artifact a human is about to review has to be committed and referenced.
	if len(stored.Artifacts[StagePlan]) != 1 {
		t.Fatalf("a blocked stage lost its artifacts: %#v", stored.Artifacts)
	}
}

func TestAdvanceEmitsStartAndCompletion(t *testing.T) {
	harness := newHarness(t, time.Minute, nil)
	project := harness.newProject(t)
	scope := scopeOf(project)
	events := &recordingEmitter{}

	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{
		ProjectID: project.ID, Scope: scope, Events: events,
	}); err != nil {
		t.Fatal(err)
	}
	kinds := events.kinds()
	if len(kinds) != 2 || kinds[0] != EventStageStarted || kinds[1] != EventStageCompleted {
		t.Fatalf("events = %#v", kinds)
	}

	// A failing sink must not fail a minutes-long stage run: events are
	// notifications, and the result is fetched with /modeling show anyway.
	harness.tick(10)
	events.err = errors.New("sink is gone")
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{
		ProjectID: project.ID, Scope: scope, Events: events,
	}); err != nil {
		t.Fatalf("a broken sink failed the run: %v", err)
	}
	// A nil emitter is legal: the pipeline substitutes the disabled one instead of
	// nil-checking at every call site.
	harness.tick(10)
	if _, err := harness.pipeline.Advance(context.Background(), RunRequest{
		ProjectID: project.ID, Scope: scope,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryLineIsBounded(t *testing.T) {
	long := strings.Repeat("register map ", 200)
	if got := summaryLine(long); len([]rune(got)) > maxEventText+1 {
		t.Fatalf("summary is %d runes", len([]rune(got)))
	}
	if got := summaryLine("first line\nsecond line"); got != "first line second line" {
		t.Fatalf("summary = %q; a multi-line summary would break one-line renderers", got)
	}
}

func TestNewPipelineRejectsBadOptions(t *testing.T) {
	root := t.TempDir()
	projects := openStore(t, filepath.Join(root, "projects"), 10)
	artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 1024, 8192)
	valid := PipelineOptions{
		Projects: projects, Artifacts: artifacts,
		Stages:  []StageRunner{&fakeStage{stage: StagePlan}},
		Timeout: time.Minute, Now: func() time.Time { return at(1) }, Logger: testLogger(t),
	}
	tests := map[string]func(*PipelineOptions){
		"no projects":  func(o *PipelineOptions) { o.Projects = nil },
		"no artifacts": func(o *PipelineOptions) { o.Artifacts = nil },
		"no stages":    func(o *PipelineOptions) { o.Stages = nil },
		"nil stage":    func(o *PipelineOptions) { o.Stages = []StageRunner{nil} },
		"unknown stage": func(o *PipelineOptions) {
			o.Stages = []StageRunner{&fakeStage{stage: "polish"}}
		},
		"duplicate stage": func(o *PipelineOptions) {
			o.Stages = []StageRunner{&fakeStage{stage: StagePlan}, &fakeStage{stage: StagePlan}}
		},
		"zero timeout": func(o *PipelineOptions) { o.Timeout = 0 },
		"no clock":     func(o *PipelineOptions) { o.Now = nil },
		"no logger":    func(o *PipelineOptions) { o.Logger = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)
			if _, err := NewPipeline(opts); err == nil {
				t.Fatal("NewPipeline() error = nil")
			}
		})
	}
}
