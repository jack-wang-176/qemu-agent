package modeling

// pipeline.go is the transaction boundary of the modeling package and the
// counterpart of agent.Run: everything with a side effect happens here, in one
// fixed order, so that every failure has exactly one known landing place.
//
// The order Advance follows is:
//
//	 1 load the project under the caller's scope   missing/unauthorized -> ErrNotFound
//	 2 check the transition                        illegal -> error, nothing written
//	 3 Begin and Save                              running is persisted *before* the work
//	 4 wrap the run in the stage timeout
//	 5 emit stage_started
//	 6 run the stage
//	 7 on error: Fail(classify(err)) and Save
//	 8 stage the drafts                            rejected -> Discard, then Fail
//	 9 commit the batch                            artifacts land before state
//	10 Complete or Hold on the project value
//	11 Save the new state
//	12 emit stage_completed and return the result
//
// Two invariants explain the order. Begin is persisted first, so a process killed
// mid-stage leaves a project in "running" — that is the crash signal, and a redo
// of the current stage is always legal. Artifacts are committed before state, so
// the only reachable inconsistency is "files exist, state did not advance", which
// a re-run fixes for free because content addressing recognizes the same digest.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// RunRequest is one /modeling advance. Scope comes from the command context, never
// from a command argument, so a user cannot advance somebody else's project.
type RunRequest struct {
	ProjectID string
	Scope     Scope
	Stage     Stage  // empty means "the project's current stage"
	Request   string // the human's device request, passed to the plan stage as data
	Sources   []string
	Events    EventEmitter
}

// RunResult is what the command layer renders. It carries refs, not bytes: the
// caller reads an artifact with Read, which re-verifies its digest.
type RunResult struct {
	Project Project
	Stage   Stage
	Refs    []ArtifactRef
	Summary string
	Blocked bool
	Reason  string
}

// Runner is the whole surface the app layer gets. It deliberately has no Save and
// no access to the ArtifactStore's write side: a command cannot change project
// state except by asking the Pipeline to run a stage.
type Runner interface {
	Create(ctx context.Context, title string, scope Scope) (Project, error)
	List(ctx context.Context, query Query) ([]Project, error)
	Show(ctx context.Context, id string, scope Scope) (Project, error)
	Advance(ctx context.Context, req RunRequest) (RunResult, error)
	Reset(ctx context.Context, id string, stage Stage, scope Scope) (Project, error)
	Read(ctx context.Context, id string, ref ArtifactRef, scope Scope) ([]byte, error)
}

// PipelineOptions carries every dependency. Stages is a slice rather than a map
// because the registry is built once, at wiring time, and a duplicate stage must
// be an error instead of a silent overwrite.
type PipelineOptions struct {
	Projects  ProjectStore
	Artifacts ArtifactStore
	Stages    []StageRunner
	Tools     ToolRunner
	Completer Completer
	Workspace string
	QemuRoot  string
	Timeout   time.Duration
	Now       func() time.Time
	Logger    *slog.Logger
}

// Pipeline is immutable after construction: the stage registry cannot change at
// runtime, so what a project can do is a property of the binary, not of state
// somebody edited.
type Pipeline struct {
	projects  ProjectStore
	artifacts ArtifactStore
	stages    map[Stage]StageRunner
	tools     ToolRunner
	completer Completer
	workspace string
	qemuRoot  string
	timeout   time.Duration
	now       func() time.Time
	logger    *slog.Logger
}

var _ Runner = (*Pipeline)(nil)

func NewPipeline(opts PipelineOptions) (*Pipeline, error) {
	if opts.Projects == nil {
		return nil, errors.New("modeling pipeline project store is nil")
	}
	if opts.Artifacts == nil {
		return nil, errors.New("modeling pipeline artifact store is nil")
	}
	if len(opts.Stages) == 0 {
		return nil, errors.New("modeling pipeline has no stages")
	}
	if opts.Timeout <= 0 {
		return nil, errors.New("modeling stage timeout must be > 0")
	}
	if opts.Now == nil {
		return nil, errors.New("modeling clock is nil")
	}
	if opts.Logger == nil {
		return nil, errors.New("modeling logger is nil")
	}
	registry := make(map[Stage]StageRunner, len(opts.Stages))
	for _, runner := range opts.Stages {
		if runner == nil {
			return nil, errors.New("modeling stage runner is nil")
		}
		name := runner.Name()
		if stageIndex(name) < 0 {
			return nil, fmt.Errorf("modeling stage runner claims unknown stage %q", name)
		}
		if _, exists := registry[name]; exists {
			return nil, fmt.Errorf("modeling stage %q is registered twice", name)
		}
		registry[name] = runner
	}
	return &Pipeline{
		projects: opts.Projects, artifacts: opts.Artifacts, stages: registry,
		tools: opts.Tools, completer: opts.Completer,
		workspace: opts.Workspace, qemuRoot: opts.QemuRoot,
		timeout: opts.Timeout, now: opts.Now, logger: opts.Logger,
	}, nil
}

// Create starts a project at the first stage. The owner is the caller: a modeling
// project is private to whoever started it, because its title names unreleased
// hardware.
func (p *Pipeline) Create(ctx context.Context, title string, scope Scope) (Project, error) {
	return p.projects.Create(ctx, Project{
		Title: title, WorkspaceID: scope.WorkspaceID, UserID: scope.UserID,
	})
}

func (p *Pipeline) List(ctx context.Context, query Query) ([]Project, error) {
	return p.projects.List(ctx, query)
}

func (p *Pipeline) Show(ctx context.Context, id string, scope Scope) (Project, error) {
	return p.projects.Get(ctx, id, scope)
}

// Reset rewinds a project. It is a state change without a stage run, so it is the
// one place besides Advance that bumps the revision.
func (p *Pipeline) Reset(ctx context.Context, id string, stage Stage, scope Scope) (Project, error) {
	project, err := p.projects.Get(ctx, id, scope)
	if err != nil {
		return Project{}, err
	}
	working := project.Clone()
	if err := working.Reset(stage, p.now()); err != nil {
		return Project{}, err
	}
	working.Revision++
	return p.projects.Save(ctx, working)
}

// Read returns the bytes of an artifact the project actually references. Going
// through the project rather than straight to the store is what makes a stolen
// ref useless: an id from another project is not in this project's lists, so it
// is reported as not found.
func (p *Pipeline) Read(ctx context.Context, id string, ref ArtifactRef, scope Scope) ([]byte, error) {
	project, err := p.projects.Get(ctx, id, scope)
	if err != nil {
		return nil, err
	}
	if !projectReferences(project, ref) {
		return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, ref.ID)
	}
	body, err := p.artifacts.Open(ctx, project.ID, ref)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

// Advance runs exactly one stage. See the file comment for the fixed order; the
// steps below are numbered to match it.
func (p *Pipeline) Advance(ctx context.Context, req RunRequest) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	events := normalizeEmitter(req.Events)

	// 1 + 2: load under the caller's scope, then check the transition. Both
	// checks happen before anything is written, so a rejected request leaves the
	// project byte-identical.
	project, err := p.projects.Get(ctx, req.ProjectID, req.Scope)
	if err != nil {
		return RunResult{}, err
	}
	target := req.Stage
	if strings.TrimSpace(string(target)) == "" {
		target = project.Current
	}
	if err := project.CanAdvance(target); err != nil {
		return RunResult{}, err
	}
	runner, ok := p.stages[target]
	if !ok {
		return RunResult{}, fmt.Errorf("modeling stage %q is not registered", target)
	}

	// 3: persist "running" before doing the work. A crash from here on is
	// recoverable precisely because the state on disk says which stage was live.
	working := project.Clone()
	if err := working.Begin(target, p.now()); err != nil {
		return RunResult{}, err
	}
	working.Revision++
	started, err := p.projects.Save(ctx, working)
	if err != nil {
		return RunResult{}, err
	}
	working = started

	// 4: the stage timeout wraps the whole run, model call and tools included, so
	// a hung provider leaves a blocked project instead of a permanent "running".
	runCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// 5: events are notifications and may be lost; a failing sink must not fail
	// the run, so it is logged as a category and the stage proceeds.
	p.emit(ctx, events, StageEvent{Kind: EventStageStarted, Project: working.ID, Stage: target})

	// 6: the stage itself. Everything it may touch is in StageInput; it gets no
	// store, so it cannot persist anything on its own.
	out, runErr := runner.Run(runCtx, StageInput{
		Project: working.Clone(), Stage: target, Request: req.Request, Sources: req.Sources,
		Workspace: p.workspace, QemuRoot: p.qemuRoot,
		Inputs: inputsBefore(working, target), Open: p.openFor(ctx, working.ID),
		Completer: p.completer, Executor: p.tools, Events: events, Now: p.now(),
	})
	if runErr != nil {
		// 7: classify, record, and return. The original error goes to the caller
		// for its own logging; only the category is persisted. out.Evidence is
		// passed along because a stage may return evidence together with an error —
		// see StageOutput — and it is the only output of a failed run that is kept.
		return RunResult{}, p.fail(ctx, working, target, events, out.Evidence, runErr)
	}

	// 8: stage the drafts. Anything the artifact store rejects — an unsafe name, a
	// size over the limit — is a stage failure with the staging area cleaned up.
	refs, err := p.persist(ctx, working, target, out)
	if err != nil {
		// The batch was discarded as a unit, so there is no evidence to keep here:
		// re-committing just the evidence part could succeed for the same reason the
		// artifact part failed, and half a batch is worse than none.
		return RunResult{}, p.fail(ctx, working, target, events, nil, err)
	}

	// 10 + 11: move the project value, then commit it. Blocked is not a failure:
	// the artifacts are recorded and the project waits on the same stage.
	if out.Blocked {
		reason := out.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "awaiting_human"
		}
		if err := working.Hold(target, refs, reason, p.now()); err != nil {
			return RunResult{}, p.fail(ctx, working, target, events, nil, err)
		}
	} else if err := working.Complete(target, refs, p.now()); err != nil {
		return RunResult{}, p.fail(ctx, working, target, events, nil, err)
	}
	working.Revision++
	saved, err := p.projects.Save(ctx, working)
	if err != nil {
		// Invariant: the artifacts are already committed. A lost race therefore
		// costs a re-run, not a corrupt project — the re-run re-commits the same
		// digests idempotently.
		return RunResult{}, err
	}

	// 12: report. Summary is the only free text that leaves the pipeline, and it
	// is bounded here rather than trusted.
	summary := summaryLine(out.Summary)
	p.emit(ctx, events, StageEvent{
		Kind: EventStageCompleted, Project: saved.ID, Stage: target,
		Text: summary, OK: true, Blocked: out.Blocked, Reason: saved.LastError,
	})
	return RunResult{
		Project: saved, Stage: target, Refs: refs, Summary: summary,
		Blocked: out.Blocked, Reason: saved.LastError,
	}, nil
}

// persist turns drafts into committed refs. Staging and committing are one unit
// here: if anything is rejected the staging directory is discarded, so the next
// attempt starts from an empty area.
func (p *Pipeline) persist(ctx context.Context, project Project, stage Stage, out StageOutput) ([]ArtifactRef, error) {
	drafts := make([]Draft, 0, len(out.Artifacts)+len(out.Evidence))
	drafts = append(drafts, out.Artifacts...)
	for _, draft := range out.Evidence {
		// Evidence is checked here rather than in the store, because "may this be
		// presented as proof" is a pipeline rule, not a filesystem rule.
		if draft.Kind != KindEvidence {
			return nil, fmt.Errorf("%w: evidence %q has kind %q", ErrUnsafeName, draft.Name, draft.Kind)
		}
		drafts = append(drafts, draft)
	}
	if len(drafts) == 0 {
		// A stage that produced nothing is legal only in the sense that there is
		// nothing to commit; the state change below still happens.
		return nil, nil
	}
	for index := range drafts {
		// A stage may not file its output under another stage: the refs are about
		// to be recorded under `stage`, and Project.record would reject them later
		// anyway — failing here keeps staging clean.
		if drafts[index].Stage == "" {
			drafts[index].Stage = stage
		}
		if drafts[index].Stage != stage {
			return nil, fmt.Errorf("%w: artifact %q claims stage %q", ErrUnsafeName, drafts[index].Name, drafts[index].Stage)
		}
	}
	batch, err := p.artifacts.Stage(ctx, project.ID, drafts)
	if err != nil {
		return nil, err
	}
	refs, err := p.artifacts.Commit(ctx, batch)
	if err != nil {
		if discardErr := p.artifacts.Discard(ctx, batch); discardErr != nil {
			p.logger.Warn("modeling staging discard failed", "project", project.ID, "stage", stage, "err", classify(discardErr))
		}
		return nil, err
	}
	return refs, nil
}

// persistEvidence commits the evidence of a *failed* stage. It is separate from
// persist because the rules are narrower: only KindEvidence may be written on a
// failure path, since anything else would become a legal input to the next stage —
// a register map that did not validate must never be readable as "the IR".
func (p *Pipeline) persistEvidence(ctx context.Context, project Project, stage Stage, evidence []Draft) ([]ArtifactRef, error) {
	if len(evidence) == 0 {
		return nil, nil
	}
	drafts := make([]Draft, 0, len(evidence))
	for _, draft := range evidence {
		if draft.Stage == "" {
			draft.Stage = stage
		}
		if draft.Stage != stage {
			return nil, fmt.Errorf("%w: evidence %q claims stage %q", ErrUnsafeName, draft.Name, draft.Stage)
		}
		if draft.Kind != KindEvidence {
			return nil, fmt.Errorf("%w: evidence %q has kind %q", ErrUnsafeName, draft.Name, draft.Kind)
		}
		drafts = append(drafts, draft)
	}
	batch, err := p.artifacts.Stage(ctx, project.ID, drafts)
	if err != nil {
		return nil, err
	}
	refs, err := p.artifacts.Commit(ctx, batch)
	if err != nil {
		if discardErr := p.artifacts.Discard(ctx, batch); discardErr != nil {
			p.logger.Warn("modeling evidence discard failed", "project", project.ID, "stage", stage, "err", classify(discardErr))
		}
		return nil, err
	}
	return refs, nil
}

// fail is the single failure landing place. It records a category, never a
// message: the error may quote a datasheet, a provider URL or tool output, and
// LastError is rendered by /modeling show and written to the log.
//
// evidence is the one thing that survives a failed stage. A schema_invalid run
// whose raw model reply was thrown away is undiagnosable — the category alone does
// not say which field the model got wrong — so the evidence drafts are committed
// here while the project record still holds nothing but the category.
func (p *Pipeline) fail(ctx context.Context, working Project, stage Stage, events EventEmitter, evidence []Draft, cause error) error {
	category := classify(cause)
	// The state write must survive a canceled request: a canceled advance that
	// left the project in "running" would be indistinguishable from a crash.
	saveCtx := context.WithoutCancel(ctx)

	// Commit the evidence before the state change, for the same reason the success
	// path commits artifacts before state: "files exist, state did not move" is
	// repairable, "state points at files that were never written" is not. A failure
	// while committing evidence is itself only logged — it must not replace the
	// category the caller is being told about.
	refs, evidenceErr := p.persistEvidence(saveCtx, working, stage, evidence)
	if evidenceErr != nil {
		p.logger.Warn("modeling failure evidence not stored", "project", working.ID, "stage", stage, "err", classify(evidenceErr))
	}

	working.Revision++
	if err := working.FailWith(stage, category, refs, p.now()); err != nil {
		p.logger.Warn("modeling failure state rejected", "project", working.ID, "stage", stage, "err", classify(err))
	} else if _, err := p.projects.Save(saveCtx, working); err != nil {
		p.logger.Warn("modeling failure state not saved", "project", working.ID, "stage", stage, "err", classify(err))
	}
	p.logger.Warn("modeling stage failed", "project", working.ID, "stage", stage, "category", category)
	p.emit(saveCtx, events, StageEvent{
		Kind: EventStageCompleted, Project: working.ID, Stage: stage, Text: category, OK: false,
		Reason: category,
	})
	// The category is wrapped around the cause so a caller can still inspect the
	// sentinel; only the pipeline's own persisted copy is category-only.
	return fmt.Errorf("modeling stage %s failed (%s): %w", stage, category, cause)
}

// emit sends one progress notification. A sink error is logged and swallowed:
// invariant "events are notifications, not a data channel" means a lossy channel
// must not be able to fail a minutes-long stage run.
func (p *Pipeline) emit(ctx context.Context, events EventEmitter, event StageEvent) {
	if err := events.StageEvent(ctx, event); err != nil {
		p.logger.Warn("modeling stage event dropped", "project", event.Project, "stage", event.Stage, "err", classify(err))
	}
}

// openFor binds artifact reading to one project, which is what stops a stage from
// reading another project's bytes even if it obtains a ref.
func (p *Pipeline) openFor(ctx context.Context, projectID string) OpenFunc {
	return func(ref ArtifactRef) (io.ReadCloser, error) {
		return p.artifacts.Open(ctx, projectID, ref)
	}
}

// inputsBefore hands a stage the references of every *earlier* stage only. A
// stage cannot see its own previous output, so a re-run starts from the same
// inputs as the first run and stays reproducible.
func inputsBefore(project Project, stage Stage) map[Stage][]ArtifactRef {
	index := stageIndex(stage)
	if index <= 0 || len(project.Artifacts) == 0 {
		return map[Stage][]ArtifactRef{}
	}
	inputs := make(map[Stage][]ArtifactRef, index)
	for _, earlier := range StageOrder[:index] {
		if refs, ok := project.Artifacts[earlier]; ok {
			inputs[earlier] = append([]ArtifactRef(nil), refs...)
		}
	}
	return inputs
}

// projectReferences reports whether a project actually points at this artifact.
func projectReferences(project Project, ref ArtifactRef) bool {
	for _, refs := range project.Artifacts {
		for _, candidate := range refs {
			if candidate.ID == ref.ID && candidate.Name == ref.Name && candidate.Stage == ref.Stage {
				return true
			}
		}
	}
	for _, candidate := range project.Evidence {
		if candidate.ID == ref.ID && candidate.Name == ref.Name && candidate.Stage == ref.Stage {
			return true
		}
	}
	return false
}

// Category is the exported view of classify. It exists because the command layer
// has to name a failure without ever rendering one: the error a stage returns may
// quote a datasheet line, a provider URL or tool stdout, so /modeling reports the
// category and nothing else. Having exactly one classifier for state, logs, events
// and replies is what makes "no payload escapes" checkable.
func Category(err error) string { return classify(err) }

// classify maps an error to one of a closed set of categories. It is the
// enforcement point for "no payload in state or logs": everything that reaches
// LastError, a log line or an event passes through here first, and the output can
// only ever be one of these constants.
func classify(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "stage_timeout"
	case errors.Is(err, ErrToolDenied):
		return "tool_denied"
	case errors.Is(err, ErrSchemaInvalid):
		return "schema_invalid"
	case errors.Is(err, ErrBuildFailed):
		return "build_failed"
	case errors.Is(err, ErrModelFailed):
		return "model_failed"
	case errors.Is(err, ErrStageUnavailable):
		return "stage_unavailable"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrApplyPartial):
		return "apply_partial"
	case errors.Is(err, ErrApplyRejected):
		return "apply_rejected"
	case errors.Is(err, ErrApplyUnavailable):
		return "apply_unavailable"
	case errors.Is(err, ErrDisabled):
		return "disabled"
	case errors.Is(err, ErrUnsafeName), errors.Is(err, ErrTooLarge),
		errors.Is(err, ErrTampered), errors.Is(err, ErrIDCollision),
		errors.Is(err, ErrEmptyArtifac):
		return "artifact_rejected"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	default:
		// An unclassified error is still reported as a category rather than as
		// its message, so an unexpected wrapped error cannot leak content.
		return "stage_failed"
	}
}
