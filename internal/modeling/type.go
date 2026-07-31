// Package modeling is the staged device-modeling pipeline: it turns a datasheet
// plus a human request into a reviewable chain of artifacts (plan → register IR
// → generated code → build/qtest evidence) that survives a process restart.
//
// The package knows nothing about channels, sessions or the agent loop. A
// Pipeline is *not* an Agent and never calls one: the agent-facing side is the
// /modeling command family in internal/app, which only reads projects and asks
// the Pipeline to advance one stage. Two rules follow from that and are enforced
// throughout:
//
//   - Nothing here is ever injected into a conversation prompt. Artifacts are
//     model output about untrusted input; they are shown when a human asks.
//   - Nothing here writes into a QEMU source tree by itself. Emit writes to a
//     staging area, and landing it is a separate, audited, approved step.
//
// This file holds the pure value layer: no I/O, no model call, no clock of its
// own. Every state-machine rule is a function of its arguments, so all of it is
// testable without creating a directory.
package modeling

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Stage is one step of the pipeline.
type Stage string

const (
	StagePlan    Stage = "plan"    // read the request and skills, produce a modeling plan
	StageExtract Stage = "extract" // pull the register IR out of a datasheet/header
	StageInfer   Stage = "infer"   // fill in behaviour: reset values, side effects, IRQs
	StageEmit    Stage = "emit"    // generate code into staging
	StageVerify  Stage = "verify"  // build + qtest, produce Evidence
)

// StageOrder is the single declaration of stage order. Next, previous and
// "may I advance to" are all derived from it; a second switch somewhere else
// would be a second source of truth that drifts.
var StageOrder = []Stage{StagePlan, StageExtract, StageInfer, StageEmit, StageVerify}

// Status is the state of the project's current stage.
type Status string

const (
	StatusPending Status = "pending" // not run yet
	StatusRunning Status = "running" // persisted before the stage runs, so a crash is detectable
	StatusBlocked Status = "blocked" // failed, or waiting for a human (Emit awaiting apply)
	StatusDone    Status = "done"    // every stage finished
)

// Kind classifies an artifact for display and for the checks that refuse to
// treat, say, generated code as evidence.
type Kind string

const (
	KindPlan     Kind = "plan"
	KindRegIR    Kind = "regir"
	KindCode     Kind = "code"
	KindDiff     Kind = "diff"
	KindEvidence Kind = "evidence"
)

// ArtifactRef is a project's reference to a stored artifact. The bytes live in
// the ArtifactStore; this is the part that goes into the project JSON.
type ArtifactRef struct {
	ID      string    `json:"id"`      // content address: first 16 hex of the sha256
	Stage   Stage     `json:"stage"`   // which stage produced it
	Name    string    `json:"name"`    // "reg-ir.json", "device.c": a bare name, no separators
	Kind    Kind      `json:"kind"`    // what the bytes are
	Bytes   int64     `json:"bytes"`   // size as committed
	Digest  string    `json:"digest"`  // full sha256, re-checked when the artifact is opened
	Created time.Time `json:"created"` // commit time
}

// Project is the durable state of one modeling run. It is a value: the Pipeline
// clones it, mutates the clone, and only then asks the store to commit — the
// same validate-then-commit shape session.Session uses.
//
// LastError deliberately holds an error *category* and nothing else. This struct
// is rendered by /modeling show and written to logs, so tool output, model
// output and datasheet fragments must never reach it.
type Project struct {
	ID          string                  `json:"id"`    // "mp-<16hex>": carries no device name and no path
	Title       string                  `json:"title"` // the only human-readable field
	WorkspaceID string                  `json:"workspace_id"`
	UserID      string                  `json:"user_id,omitempty"`
	Current     Stage                   `json:"current"`
	Status      Status                  `json:"status"`
	Artifacts   map[Stage][]ArtifactRef `json:"artifacts,omitempty"`
	Evidence    []ArtifactRef           `json:"evidence,omitempty"`
	LastError   string                  `json:"last_error,omitempty"`
	Revision    int                     `json:"revision"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
}

var (
	ErrNotFound = errors.New("modeling project not found")
	ErrConflict = errors.New("modeling project was modified concurrently")
	ErrDisabled = errors.New("modeling is disabled")
)

// idPattern guards the file name as well: the project id is the only
// caller-visible value that becomes a path element.
var idPattern = regexp.MustCompile(`^mp-[0-9a-f]{16}$`)

// namePattern is deliberately stricter than "no slashes": artifact names appear
// in manifests and in staging paths, so anything that could be read as a path or
// a shell word is rejected before it is persisted.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ParseStage accepts only the five declared stages, so a command argument can
// never invent a sixth one.
func ParseStage(raw string) (Stage, error) {
	candidate := Stage(strings.ToLower(strings.TrimSpace(raw)))
	if stageIndex(candidate) < 0 {
		return "", fmt.Errorf("invalid modeling stage %q; want plan|extract|infer|emit|verify", raw)
	}
	return candidate, nil
}

// ParseKind exists for the same reason as ParseStage: artifact kinds arrive from
// stage implementations and must be a closed set before they are persisted.
func ParseKind(raw string) (Kind, error) {
	switch kind := Kind(strings.ToLower(strings.TrimSpace(raw))); kind {
	case KindPlan, KindRegIR, KindCode, KindDiff, KindEvidence:
		return kind, nil
	default:
		return "", fmt.Errorf("invalid modeling artifact kind %q", raw)
	}
}

// stageIndex is the only place StageOrder is searched.
func stageIndex(stage Stage) int {
	for index, candidate := range StageOrder {
		if candidate == stage {
			return index
		}
	}
	return -1
}

// FirstStage is where a new project starts.
func FirstStage() Stage { return StageOrder[0] }

// LastStage is the stage after which a project is complete.
func LastStage() Stage { return StageOrder[len(StageOrder)-1] }

// NextStage reports the stage after the given one. ok is false for the last
// stage, which is how Advance learns the pipeline is finished.
func NextStage(stage Stage) (Stage, bool) {
	index := stageIndex(stage)
	if index < 0 || index+1 >= len(StageOrder) {
		return "", false
	}
	return StageOrder[index+1], true
}

// Clone deep-copies the maps and slices. It is the precondition of every
// mutation: the Pipeline runs on a copy and commits only after validation, so a
// rejected transition cannot leave a half-updated project in memory.
func (p Project) Clone() Project {
	clone := p
	if p.Artifacts != nil {
		clone.Artifacts = make(map[Stage][]ArtifactRef, len(p.Artifacts))
		for stage, refs := range p.Artifacts {
			clone.Artifacts[stage] = append([]ArtifactRef(nil), refs...)
		}
	}
	clone.Evidence = append([]ArtifactRef(nil), p.Evidence...)
	return clone
}

// CanAdvance reports whether the project may run the given stage next. Two
// transitions are legal: re-running the current stage (a redo, which is how a
// blocked stage is retried) and moving to the immediately following stage. Any
// other target — a skip forward or a step back — is an error, because artifacts
// of a later stage would then reference an IR that was never produced.
func (p Project) CanAdvance(to Stage) error {
	target := stageIndex(to)
	if target < 0 {
		return fmt.Errorf("unknown modeling stage %q", to)
	}
	current := stageIndex(p.Current)
	if current < 0 {
		return fmt.Errorf("project %s has an unknown current stage", p.ID)
	}
	switch {
	case p.Status == StatusRunning:
		return fmt.Errorf("stage %q of project %s is already running", p.Current, p.ID)
	case target == current:
		// A redo is always allowed: pending, blocked and done all re-run cleanly
		// because Complete replaces this stage's refs and clears the later ones.
		return nil
	case target == current+1:
		// Moving on requires the current stage to have actually finished.
		if p.Status != StatusDone {
			return fmt.Errorf("stage %q of project %s is %s, not done", p.Current, p.ID, p.Status)
		}
		return nil
	case target > current+1:
		return fmt.Errorf("cannot skip from stage %q to %q", p.Current, to)
	default:
		return fmt.Errorf("cannot move back from stage %q to %q; reset the project instead", p.Current, to)
	}
}

// Begin marks a stage as running. It is persisted *before* the stage executes,
// so a project found in StatusRunning at startup is exactly the crash-recovery
// signal: its staging area may be dirty and its stage must be re-run.
func (p *Project) Begin(stage Stage, now time.Time) error {
	if err := p.CanAdvance(stage); err != nil {
		return err
	}
	if now.IsZero() {
		return errors.New("modeling begin needs a timestamp")
	}
	p.Current = stage
	p.Status = StatusRunning
	p.LastError = ""
	p.UpdatedAt = now
	return nil
}

// Complete records the artifacts of a finished stage and moves the project on.
//
// Re-running a stage replaces that stage's references rather than appending to
// them, and drops every later stage's references: code generated from a previous
// register IR must not survive a new extraction. The files themselves are kept —
// the ArtifactStore never deletes, because a superseded artifact is still the
// audit record of what the agent once produced.
func (p *Project) Complete(stage Stage, refs []ArtifactRef, now time.Time) error {
	if err := p.record(stage, refs, now); err != nil {
		return err
	}
	if next, ok := NextStage(stage); ok {
		p.Current = next
		p.Status = StatusPending
		return nil
	}
	// The last stage finished: stay on it and report done rather than inventing
	// a sixth stage to park on.
	p.Current = stage
	p.Status = StatusDone
	return nil
}

// Hold records the artifacts of a stage that finished its work but needs a human
// before the pipeline may continue — Emit producing a diff nobody has approved
// yet is the motivating case.
//
// It exists so that "blocked" and "produced artifacts" are not mutually
// exclusive: the diff a reviewer is about to read has to be committed and
// referenced, while the project stays on the stage so /modeling apply knows what
// it is applying.
func (p *Project) Hold(stage Stage, refs []ArtifactRef, reason string, now time.Time) error {
	if err := p.record(stage, refs, now); err != nil {
		return err
	}
	p.Current = stage
	p.Status = StatusBlocked
	p.LastError = shortCategory(reason)
	return nil
}

// record is the shared part of Complete and Hold: validate the refs, replace this
// stage's references, invalidate everything downstream, and refresh Evidence.
// Neither caller may skip it, which is why it is a method and not a comment.
func (p *Project) record(stage Stage, refs []ArtifactRef, now time.Time) error {
	if p.Status != StatusRunning || p.Current != stage {
		return fmt.Errorf("stage %q of project %s is not running", stage, p.ID)
	}
	if now.IsZero() {
		return errors.New("modeling complete needs a timestamp")
	}
	for _, ref := range refs {
		if ref.Stage != stage {
			return fmt.Errorf("artifact %q belongs to stage %q, not %q", ref.Name, ref.Stage, stage)
		}
		if err := validateRef(ref); err != nil {
			return err
		}
	}

	// Replace this stage's refs, then invalidate everything downstream.
	if p.Artifacts == nil {
		p.Artifacts = make(map[Stage][]ArtifactRef)
	}
	p.Artifacts[stage] = append([]ArtifactRef(nil), refs...)
	p.clearAfter(stage)

	// Evidence is a separate list because it is what a reviewer reads first; it
	// is produced by Verify only, so a redo of Verify replaces it wholesale.
	if stage == StageVerify {
		p.Evidence = collectEvidence(refs)
	}

	p.LastError = ""
	p.UpdatedAt = now
	return nil
}

// Block parks a stage that needs a human before the pipeline may continue —
// Emit awaiting an apply is the motivating case. It is not an error: reason is
// still a short category, never a rendered diff or tool output.
func (p *Project) Block(stage Stage, reason string, now time.Time) error {
	if p.Status != StatusRunning || p.Current != stage {
		return fmt.Errorf("stage %q of project %s is not running", stage, p.ID)
	}
	if now.IsZero() {
		return errors.New("modeling block needs a timestamp")
	}
	p.Status = StatusBlocked
	p.LastError = shortCategory(reason)
	p.UpdatedAt = now
	return nil
}

// Fail records a failed stage. kind is an error *category* such as
// "stage_timeout" or "schema_invalid"; callers must have classified the error
// already, because everything stored here is shown to users and logged.
func (p *Project) Fail(stage Stage, kind string, now time.Time) error {
	if p.Status != StatusRunning || p.Current != stage {
		return fmt.Errorf("stage %q of project %s is not running", stage, p.ID)
	}
	if now.IsZero() {
		return errors.New("modeling fail needs a timestamp")
	}
	category := shortCategory(kind)
	if category == "" {
		return errors.New("modeling failure category is empty")
	}
	p.Status = StatusBlocked
	p.LastError = category
	p.UpdatedAt = now
	return nil
}

// FailWith records a failed stage together with the evidence that failure
// produced — in practice the raw model reply of a schema failure.
//
// It is a separate method rather than a parameter of Fail because the rules are
// stricter: only KindEvidence refs may attach to a failure, and they are appended
// rather than replacing the stage's references. Appending is the point — a failed
// redo must not delete the artifacts of the run that succeeded, and a later
// successful run replaces the whole list anyway.
func (p *Project) FailWith(stage Stage, kind string, refs []ArtifactRef, now time.Time) error {
	// Validate everything before mutating, so a bad ref cannot leave a project
	// that is marked failed but references nothing.
	for _, ref := range refs {
		if ref.Stage != stage {
			return fmt.Errorf("evidence %q belongs to stage %q, not %q", ref.Name, ref.Stage, stage)
		}
		if ref.Kind != KindEvidence {
			return fmt.Errorf("evidence %q of a failed stage has kind %q", ref.Name, ref.Kind)
		}
		if err := validateRef(ref); err != nil {
			return err
		}
	}
	if err := p.Fail(stage, kind, now); err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	if p.Artifacts == nil {
		p.Artifacts = make(map[Stage][]ArtifactRef)
	}
	p.Artifacts[stage] = append(p.Artifacts[stage], refs...)
	return nil
}

// Applied records a successful landing of a stage's files into the QEMU tree.
//
// It is separate from Complete because an apply happens *between* stage runs: the
// emit stage is already finished and blocked on "awaiting_apply", so nothing is
// running and Complete's precondition cannot hold. The refs it takes are evidence
// — the plan that was applied — and they are appended, because the stage's own
// generated artifacts must stay referenced exactly as the reviewer saw them.
//
// On success the project moves to the next stage, which is what makes
// `/modeling advance` after an apply run verify rather than re-emit.
func (p *Project) Applied(stage Stage, refs []ArtifactRef, now time.Time) error {
	if p.Status == StatusRunning {
		return fmt.Errorf("stage %q of project %s is running; an apply may not interleave with it", p.Current, p.ID)
	}
	if len(p.Artifacts[stage]) == 0 {
		return fmt.Errorf("stage %q of project %s has no artifacts to apply", stage, p.ID)
	}
	if now.IsZero() {
		return errors.New("modeling apply needs a timestamp")
	}
	if err := p.appendEvidence(stage, refs); err != nil {
		return err
	}
	// A re-apply of a stage the project has already left must not rewind it: the
	// files on disk are the same ones, and moving Current backwards would make the
	// next advance re-run a stage whose output is already recorded.
	if stageIndex(stage) >= stageIndex(p.Current) {
		if next, ok := NextStage(stage); ok {
			p.Current = next
			p.Status = StatusPending
		} else {
			p.Current = stage
			p.Status = StatusDone
		}
	}
	p.LastError = ""
	p.UpdatedAt = now
	return nil
}

// ApplyFailed records a landing that did not finish. The project stays on the
// stage and stays blocked, because the tree is now in a state only a human can
// judge: kind is a category such as "apply_partial", and the refs are the plan
// that says exactly which files were written.
func (p *Project) ApplyFailed(stage Stage, kind string, refs []ArtifactRef, now time.Time) error {
	category := shortCategory(kind)
	if category == "" {
		return errors.New("modeling apply failure category is empty")
	}
	if now.IsZero() {
		return errors.New("modeling apply needs a timestamp")
	}
	if err := p.appendEvidence(stage, refs); err != nil {
		return err
	}
	// Unlike Applied this *does* park the project back on the stage, even if it had
	// moved on: a half-written tree means the stage's output is no longer what is on
	// disk, and re-running it is the cheapest way back to a known state.
	p.Current = stage
	p.Status = StatusBlocked
	p.LastError = category
	p.UpdatedAt = now
	return nil
}

// appendEvidence is the shared, validate-everything-first half of Applied and
// ApplyFailed. Only evidence may be appended this way: an apply produces a record
// of what happened, never a new input for a later stage.
func (p *Project) appendEvidence(stage Stage, refs []ArtifactRef) error {
	for _, ref := range refs {
		if ref.Stage != stage {
			return fmt.Errorf("evidence %q belongs to stage %q, not %q", ref.Name, ref.Stage, stage)
		}
		if ref.Kind != KindEvidence {
			return fmt.Errorf("apply evidence %q has kind %q", ref.Name, ref.Kind)
		}
		if err := validateRef(ref); err != nil {
			return err
		}
	}
	if len(refs) == 0 {
		return nil
	}
	if p.Artifacts == nil {
		p.Artifacts = make(map[Stage][]ArtifactRef)
	}
	p.Artifacts[stage] = append(p.Artifacts[stage], refs...)
	return nil
}

// Reset rewinds the project to a stage and drops that stage's artifacts and
// everything after it. This is the only backward move, and it is explicit
// precisely because it invalidates downstream work.
func (p *Project) Reset(stage Stage, now time.Time) error {
	if stageIndex(stage) < 0 {
		return fmt.Errorf("unknown modeling stage %q", stage)
	}
	if now.IsZero() {
		return errors.New("modeling reset needs a timestamp")
	}
	if p.Artifacts != nil {
		delete(p.Artifacts, stage)
	}
	p.clearAfter(stage)
	p.Current = stage
	p.Status = StatusPending
	p.LastError = ""
	p.UpdatedAt = now
	return nil
}

// clearAfter drops the artifacts of every stage later than stage, and the
// evidence with them when Verify is among those stages.
func (p *Project) clearAfter(stage Stage) {
	index := stageIndex(stage)
	if index < 0 || p.Artifacts == nil {
		return
	}
	for _, later := range StageOrder[index+1:] {
		delete(p.Artifacts, later)
		if later == StageVerify {
			p.Evidence = nil
		}
	}
}

// CanReplaceFrom is the optimistic-concurrency check the store runs before it
// overwrites a file: identity may not change and the revision must be exactly
// one ahead, so two concurrent advances cannot both win.
func (p Project) CanReplaceFrom(old Project) error {
	if p.ID != old.ID {
		return fmt.Errorf("%w: project id changed", ErrConflict)
	}
	if p.WorkspaceID != old.WorkspaceID {
		return fmt.Errorf("%w: workspace id changed", ErrConflict)
	}
	if p.UserID != old.UserID {
		return fmt.Errorf("%w: owner changed", ErrConflict)
	}
	if !p.CreatedAt.Equal(old.CreatedAt) {
		return fmt.Errorf("%w: creation time changed", ErrConflict)
	}
	if p.Revision != old.Revision+1 {
		return fmt.Errorf("%w: revision %d does not follow %d", ErrConflict, p.Revision, old.Revision)
	}
	return nil
}

// Validate is the gate every project passes before it is written and after it is
// read back, so a hand-edited file cannot introduce a stage or a name the rest
// of the package does not expect.
func (p Project) Validate() error {
	if err := ValidateProjectID(p.ID); err != nil {
		return err
	}
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("modeling project title is empty")
	}
	if strings.TrimSpace(p.WorkspaceID) == "" {
		return errors.New("modeling project workspace id is empty")
	}
	if stageIndex(p.Current) < 0 {
		return fmt.Errorf("modeling project %s has unknown stage %q", p.ID, p.Current)
	}
	switch p.Status {
	case StatusPending, StatusRunning, StatusBlocked, StatusDone:
	default:
		return fmt.Errorf("modeling project %s has unknown status %q", p.ID, p.Status)
	}
	if p.Revision < 0 {
		return fmt.Errorf("modeling project %s has negative revision", p.ID)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		return fmt.Errorf("modeling project %s has no timestamps", p.ID)
	}
	for stage, refs := range p.Artifacts {
		if stageIndex(stage) < 0 {
			return fmt.Errorf("modeling project %s references unknown stage %q", p.ID, stage)
		}
		for _, ref := range refs {
			if ref.Stage != stage {
				return fmt.Errorf("artifact %q is filed under %q but claims %q", ref.Name, stage, ref.Stage)
			}
			if err := validateRef(ref); err != nil {
				return err
			}
		}
	}
	for _, ref := range p.Evidence {
		if err := validateRef(ref); err != nil {
			return err
		}
		if ref.Kind != KindEvidence {
			return fmt.Errorf("evidence %q has kind %q", ref.Name, ref.Kind)
		}
	}
	// LastError is a category, so anything long or multi-line means a raw error
	// string leaked in and would be rendered to the user.
	if p.LastError != shortCategory(p.LastError) {
		return fmt.Errorf("modeling project %s stores a non-category error", p.ID)
	}
	return nil
}

// validateRef checks one artifact reference. Name and digest are the two fields
// that later become a path and a content check, so both are closed here.
func validateRef(ref ArtifactRef) error {
	if err := validateName(ref.Name); err != nil {
		return err
	}
	if stageIndex(ref.Stage) < 0 {
		return fmt.Errorf("artifact %q has unknown stage %q", ref.Name, ref.Stage)
	}
	if _, err := ParseKind(string(ref.Kind)); err != nil {
		return err
	}
	if !digestPattern.MatchString(ref.Digest) {
		return fmt.Errorf("artifact %q has an unusable digest", ref.Name)
	}
	if !strings.HasPrefix(ref.Digest, ref.ID) || len(ref.ID) != 16 {
		return fmt.Errorf("artifact %q id does not address its content", ref.Name)
	}
	if ref.Bytes < 0 {
		return fmt.Errorf("artifact %q has negative size", ref.Name)
	}
	if ref.Created.IsZero() {
		return fmt.Errorf("artifact %q has no creation time", ref.Name)
	}
	return nil
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// collectEvidence keeps only the evidence-kind refs of a Verify run, so a code
// artifact can never be presented as proof that the device builds.
func collectEvidence(refs []ArtifactRef) []ArtifactRef {
	result := make([]ArtifactRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == KindEvidence {
			result = append(result, ref)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// shortCategory normalizes an error category and is the enforcement point for
// "no payload in Project": anything with whitespace, punctuation or excess
// length is truncated to a bare token instead of being stored verbatim.
func shortCategory(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r + ('a' - 'A'))
		default:
			// Stop at the first character that is not part of a category token:
			// that is where a wrapped error message would start.
			return capCategory(builder.String())
		}
	}
	return capCategory(builder.String())
}

func capCategory(value string) string {
	const maxCategoryLen = 32
	if len(value) > maxCategoryLen {
		return value[:maxCategoryLen]
	}
	return value
}

// validateName rejects a name that could escape a directory or confuse a
// manifest. It runs before persistence, never after.
func validateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("artifact name is empty")
	}
	if strings.ContainsAny(trimmed, `/\`) || strings.Contains(trimmed, "..") {
		return fmt.Errorf("artifact name %q must not contain a path", trimmed)
	}
	if !namePattern.MatchString(trimmed) {
		return fmt.Errorf("artifact name %q is not a plain file name", trimmed)
	}
	return nil
}

// ValidateProjectID is exported because the command layer checks an argument
// before it reaches the store. It reports ErrNotFound rather than a syntax
// error: a malformed id and an unknown id must be indistinguishable so ids
// cannot be probed.
func ValidateProjectID(id string) error {
	if !idPattern.MatchString(strings.TrimSpace(id)) {
		return fmt.Errorf("%w: %s", ErrNotFound, strings.TrimSpace(id))
	}
	return nil
}
