package modeling

// applier.go is the implementation behind the Applier seam declared in apply.go:
// the only code in this project that changes a QEMU source tree.
//
// Everything here exists to make one sentence true: *nothing is written that a
// human did not first read.* Emit produced bytes and a diff; the applier turns
// that into a plan, re-checks the plan against the tree it is about to touch, and
// then writes — through the audited tool executor, one file at a time, stopping at
// the first refusal.
//
// Four rules shape the file:
//
//   - The applier never composes content of its own. Every byte it writes comes
//     from a committed artifact whose digest it re-verifies, so `/modeling diff`
//     and the write cannot disagree.
//   - It reads the target tree directly (os.Stat/os.ReadFile) but writes only
//     through ToolRunner. Reading is what a base digest needs and it has no side
//     effect; writing is a policy decision, and policy lives in the executor.
//   - Every path is checked twice: as a value (FileChange.Validate) and as a
//     resolved location under QemuRoot, symlinks included. A plan that points
//     outside the tree is refused before the first write, not during it.
//   - There is no rollback. A failure after the first successful write is reported
//     as a partial apply naming exactly which files landed, because half a revert
//     is worse than half an apply.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// artifactApplyPlan is the evidence an apply leaves behind: the plan as it was
// executed. It is committed under the emit stage, because that is the stage whose
// files landed, and it is what a later reviewer reads to learn which paths in the
// tree came from this project.
const artifactApplyPlan = "apply-plan.json"

// maxApplyFileBytes bounds a single target file the applier is willing to read for
// a base digest or an append. A meson.build is a few kilobytes; anything this large
// under a generated path means the plan was computed against a different tree, and
// reading it into memory to append one fragment is not something worth doing.
const maxApplyFileBytes = 8 << 20 // 8 MiB

// ApplierOptions carries every dependency. Like StoreOptions it is a struct
// because every field is required and positional arguments of the same type would
// silently swap.
type ApplierOptions struct {
	Projects  ProjectStore     // reloaded under the caller's scope; never trusted from the caller
	Artifacts ArtifactStore    // source of every byte written
	Tools     ToolRunner       // the audited write path; nil means this build cannot apply
	QemuRoot  string           // absolute; empty means this build cannot apply
	Now       func() time.Time // injected so tests get deterministic timestamps
	Logger    *slog.Logger     // categories only, never a path from a plan
}

// FileApplier lands a project's emit output into a QEMU source tree.
//
// It is immutable after construction for the same reason Pipeline is: which tree
// may be written is a property of the binary's configuration, not of state a chat
// message can influence.
type FileApplier struct {
	projects  ProjectStore
	artifacts ArtifactStore
	tools     ToolRunner
	qemuRoot  string
	now       func() time.Time
	logger    *slog.Logger
}

var _ Applier = (*FileApplier)(nil)

// NewApplier validates its wiring. An absent QemuRoot or ToolRunner is *not* an
// error here — a build without a source tree is a legal build — but it is recorded
// so every entry point can answer ErrApplyUnavailable instead of panicking.
func NewApplier(opts ApplierOptions) (*FileApplier, error) {
	if opts.Projects == nil {
		return nil, errors.New("modeling applier project store is nil")
	}
	if opts.Artifacts == nil {
		return nil, errors.New("modeling applier artifact store is nil")
	}
	if opts.Now == nil {
		return nil, errors.New("modeling applier clock is nil")
	}
	if opts.Logger == nil {
		return nil, errors.New("modeling applier logger is nil")
	}
	root := strings.TrimSpace(opts.QemuRoot)
	if root != "" {
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("modeling qemu root %q must be absolute", root)
		}
		root = filepath.Clean(root)
	}
	return &FileApplier{
		projects: opts.Projects, artifacts: opts.Artifacts, tools: opts.Tools,
		qemuRoot: root, now: opts.Now, logger: opts.Logger,
	}, nil
}

// Plan describes what an apply would do, without touching anything. It is what
// `/modeling apply` renders for approval, so it must be computed from exactly the
// same inputs the write path uses — which is why Apply calls this method rather
// than recomputing the file set its own way.
func (a *FileApplier) Plan(ctx context.Context, id string, scope Scope) (ApplyPlan, error) {
	plan, _, err := a.plan(ctx, id, scope)
	return plan, err
}

// plan is the shared half of Plan and Apply. It returns the project as well,
// because Apply has to record its outcome on the same revision it planned from.
//
// The steps are ordered so that nothing is read from the tree until the manifest
// itself has been accepted: a manifest that does not decode must not cause a single
// stat call under QemuRoot.
func (a *FileApplier) plan(ctx context.Context, id string, scope Scope) (ApplyPlan, Project, error) {
	if err := a.available(); err != nil {
		return ApplyPlan{}, Project{}, err
	}
	// 1: the project, under the caller's scope. The applier reloads it itself so
	// that the one component able to write into a source tree never acts on a
	// Project value handed to it by a command.
	project, err := a.projects.Get(ctx, id, scope)
	if err != nil {
		return ApplyPlan{}, Project{}, err
	}

	// 2: the two emit artifacts a landing is defined by. Both are looked up by name
	// among the refs the project actually holds, so a ref cannot be smuggled in.
	manifestRef, ok := findArtifact(project, StageEmit, artifactApplyManifest)
	if !ok {
		return ApplyPlan{}, Project{}, fmt.Errorf("%w: project %s has no apply manifest; run /modeling advance to emit first",
			ErrApplyRejected, project.ID)
	}
	diffRef, ok := findArtifact(project, StageEmit, artifactDeviceDiff)
	if !ok {
		return ApplyPlan{}, Project{}, fmt.Errorf("%w: project %s has no review diff", ErrApplyRejected, project.ID)
	}
	body, err := a.read(ctx, project.ID, manifestRef)
	if err != nil {
		return ApplyPlan{}, Project{}, err
	}
	manifest, err := decodeManifest(body)
	if err != nil {
		return ApplyPlan{}, Project{}, err
	}

	// 3: one FileChange per manifest entry. This is where the tree is consulted:
	// a modify needs the digest the target has *now*, and a create needs to know
	// the target is absent before a reviewer is shown a plan that cannot run.
	changes := make([]FileChange, 0, len(manifest.Files))
	for _, entry := range manifest.Files {
		change, err := a.change(project, entry)
		if err != nil {
			return ApplyPlan{}, Project{}, err
		}
		changes = append(changes, change)
	}

	// 4: validate the plan as a value. It repeats some of the checks above on
	// purpose: this is the check a persisted plan is judged by, so it may not
	// depend on having been built by this function.
	plan := ApplyPlan{ProjectID: project.ID, Files: changes, Diff: diffRef}
	if err := plan.Validate(); err != nil {
		return ApplyPlan{}, Project{}, err
	}
	return plan, project, nil
}

// change turns one manifest entry into a FileChange, consulting the target tree
// for the current state of the path.
func (a *FileApplier) change(project Project, entry applyEntry) (FileChange, error) {
	ref, ok := findArtifact(project, StageEmit, entry.Artifact)
	if !ok {
		return FileChange{}, fmt.Errorf("%w: the manifest names artifact %q, which project %s does not hold",
			ErrApplyRejected, entry.Artifact, project.ID)
	}
	change := FileChange{Path: entry.Path, Action: entry.Action, Ref: ref}

	// The path is checked before the filesystem is touched at all: only the path
	// half of the validation can run here, because a modify's base digest does not
	// exist until the file has been read.
	if err := change.validatePath(); err != nil {
		return FileChange{}, err
	}
	target, err := a.resolve(change.Path)
	if err != nil {
		return FileChange{}, err
	}
	existing, present, err := readTarget(target)
	if err != nil {
		return FileChange{}, err
	}
	switch change.Action {
	case ApplyCreate:
		if present {
			return FileChange{}, fmt.Errorf("%w: %q already exists; reset the project or remove the file",
				ErrApplyRejected, change.Path)
		}
	case ApplyModify:
		if !present {
			return FileChange{}, fmt.Errorf("%w: %q does not exist, so there is nothing to append to",
				ErrApplyRejected, change.Path)
		}
		// BaseDigest must be set before Validate, which requires it for modify actions.
		change.BaseDigest = digestOf(existing)
	}
	// Full validation after all fields are populated: the manifest path rules, the
	// digest format, and the ref integrity are all checked here so a plan that is
	// persisted as evidence was also valid as a value.
	if err := change.Validate(); err != nil {
		return FileChange{}, err
	}
	return change, nil
}

// Apply writes the plan. The order is the file comment's rules turned into steps,
// and it is fixed: every check that can refuse happens before the first write, so
// the common failure is "nothing happened" rather than "half a device landed".
func (a *FileApplier) Apply(ctx context.Context, id string, scope Scope) (ApplyResult, error) {
	// 1: re-plan against the tree as it is now, rather than trusting a plan value
	// passed in from the command layer. The reviewer approved a *description* of the
	// change ("append this fragment to meson.build"), and this is the authoritative
	// reading of what that description means on today's tree. Note what this does and
	// does not buy: an edit made between the command layer's Plan call and this one is
	// absorbed into the new plan, not refused. The BaseDigest carried from here to
	// step 2 closes the narrower window inside this call.
	plan, project, err := a.plan(ctx, id, scope)
	if err != nil {
		return ApplyResult{ProjectID: strings.TrimSpace(id)}, err
	}

	// 2: resolve and load everything, still writing nothing. After this loop each
	// pending write is a path plus the exact bytes to put there, and any reason to
	// refuse has already been found.
	pending, err := a.prepare(ctx, plan)
	if err != nil {
		return ApplyResult{ProjectID: plan.ProjectID}, err
	}

	// 3: write, one audited call per file, stopping at the first failure. The result
	// is built as we go so that a failure reports exactly what is on disk.
	result := ApplyResult{ProjectID: plan.ProjectID}
	var writeErr error
	for index, write := range pending {
		if _, err := a.tools.Run(ctx, "write", map[string]any{
			"file_path": write.target,
			"content":   string(write.content),
		}); err != nil {
			// The tool's message may quote a path or its own policy decision, so it
			// is dropped: the plan already says which file this was.
			writeErr = fmt.Errorf("%w: writing %q was refused", ErrApplyRejected, write.path)
			for _, remaining := range pending[index:] {
				result.Skipped = append(result.Skipped, remaining.path)
			}
			break
		}
		result.Written = append(result.Written, write.path)
	}

	// 4: nothing landed. The tree is untouched, so this is an ordinary refusal and
	// the project keeps waiting for an apply — no state change, no evidence.
	if writeErr != nil && len(result.Written) == 0 {
		return result, writeErr
	}

	// 5: something landed, so the outcome becomes part of the record. The plan is
	// committed as evidence *before* the state moves, for the same reason the
	// pipeline commits artifacts before state: a ref that points at nothing is not
	// repairable, a file nobody references is.
	result.Partial = writeErr != nil
	if result.Partial {
		result.Reason = classify(ErrApplyPartial)
	}
	refs, evidenceErr := a.commitPlan(ctx, project, plan, result)
	if evidenceErr != nil {
		// The files are on disk either way; failing the whole apply over its receipt
		// would tell the operator less than reporting what was written.
		a.logger.Warn("modeling apply evidence not stored", "project", project.ID, "err", classify(evidenceErr))
	}
	result.Evidence = refs
	a.record(ctx, project, refs, result)
	if result.Partial {
		return result, fmt.Errorf("%w: %d of %d file(s) were written", ErrApplyPartial, len(result.Written), len(pending))
	}
	return result, nil
}

// pendingWrite is one resolved write: the plan's path for reporting, the absolute
// location on disk, and the finished bytes. It exists so the write loop has no
// decisions left to make.
type pendingWrite struct {
	path    string
	target  string
	content []byte
}

// prepare resolves every change and composes its content. It is a separate pass
// so that a plan which cannot be executed fully is refused before the first byte
// is written — the difference between "rejected" and "partial".
func (a *FileApplier) prepare(ctx context.Context, plan ApplyPlan) ([]pendingWrite, error) {
	pending := make([]pendingWrite, 0, len(plan.Files))
	for _, change := range plan.Files {
		// Re-validate: the plan may have been round-tripped through JSON since it was
		// built, and the path rules are cheap enough to check again.
		if err := change.Validate(); err != nil {
			return nil, err
		}
		target, err := a.resolve(change.Path)
		if err != nil {
			return nil, err
		}
		// The bytes come from the artifact store, and read re-verifies the digest, so
		// a tampered artifact file cannot reach the tree even though it is referenced.
		body, err := a.read(ctx, plan.ProjectID, change.Ref)
		if err != nil {
			return nil, err
		}
		existing, present, err := readTarget(target)
		if err != nil {
			return nil, err
		}
		content, err := compose(change, body, existing, present)
		if err != nil {
			return nil, err
		}
		pending = append(pending, pendingWrite{path: change.Path, target: target, content: content})
	}
	return pending, nil
}

// compose produces the final bytes for one change and enforces the state the plan
// assumed. A create writes the artifact as-is; a modify appends it to what is
// already there, which is the whole meaning of ApplyModify — see FileChange.
func compose(change FileChange, artifact, existing []byte, present bool) ([]byte, error) {
	switch change.Action {
	case ApplyCreate:
		if present {
			return nil, fmt.Errorf("%w: %q appeared since the plan was made", ErrApplyRejected, change.Path)
		}
		return artifact, nil
	case ApplyModify:
		if !present {
			return nil, fmt.Errorf("%w: %q disappeared since the plan was made", ErrApplyRejected, change.Path)
		}
		if digestOf(existing) != change.BaseDigest {
			// This is the check that makes an apply safe to approve minutes later: the
			// file a reviewer's plan was computed from is the file being appended to.
			return nil, fmt.Errorf("%w: %q changed since the plan was made", ErrApplyRejected, change.Path)
		}
		// The fragment is appended, never merged. A source file that does not end in
		// a newline would otherwise get the fragment glued onto its last line.
		merged := make([]byte, 0, len(existing)+len(artifact)+1)
		merged = append(merged, existing...)
		if len(existing) > 0 && existing[len(existing)-1] != '\n' {
			merged = append(merged, '\n')
		}
		return append(merged, artifact...), nil
	default:
		return nil, fmt.Errorf("%w: unknown apply action %q", ErrApplyRejected, change.Action)
	}
}

// resolve turns a plan path into an absolute location inside QemuRoot, refusing
// anything that leaves the tree.
//
// The value-level checks in FileChange.Validate already rejected absolute paths and
// "..", so this is the second half: a *symlink* can point out of the tree without
// the path saying so, and only the filesystem knows that. The deepest existing
// ancestor is resolved and compared against the resolved root, because the target
// itself usually does not exist yet.
func (a *FileApplier) resolve(rel string) (string, error) {
	if a.qemuRoot == "" {
		return "", fmt.Errorf("%w: no QEMU source tree is configured", ErrApplyUnavailable)
	}
	root, err := filepath.EvalSymlinks(a.qemuRoot)
	if err != nil {
		return "", fmt.Errorf("%w: the configured QEMU source tree cannot be resolved", ErrApplyUnavailable)
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if !within(root, target) {
		return "", fmt.Errorf("%w: %q resolves outside the QEMU source tree", ErrApplyRejected, rel)
	}
	// Walk up to the first ancestor that exists and resolve *that*: if any component
	// on the way is a symlink out of the tree, the resolved ancestor is no longer
	// under the root and the write is refused.
	ancestor := target
	for {
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("%w: %q has no resolvable parent", ErrApplyRejected, rel)
		}
		ancestor = parent
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err != nil {
			if os.IsNotExist(err) {
				continue // not created yet; keep walking up
			}
			return "", fmt.Errorf("%w: %q cannot be resolved", ErrApplyRejected, rel)
		}
		if !within(root, resolved) && resolved != root {
			return "", fmt.Errorf("%w: %q leads outside the QEMU source tree", ErrApplyRejected, rel)
		}
		return target, nil
	}
}

// within reports whether path is root or lives under it. It compares path
// elements rather than strings, so "/qemu-old" is not accepted as being inside
// "/qemu".
func within(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// readTarget reads an existing target file. A missing file is not an error — it is
// the expected state of every create — so absence is reported as a flag.
func readTarget(target string) ([]byte, bool, error) {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: a target path could not be inspected", ErrApplyRejected)
	}
	// Only regular files are touched. A symlink target would let an append follow a
	// link out of the tree, and a directory means the plan is about a different tree.
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: a target path is not a regular file", ErrApplyRejected)
	}
	if info.Size() > maxApplyFileBytes {
		return nil, false, fmt.Errorf("%w: a target file is too large to append to", ErrApplyRejected)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, false, fmt.Errorf("%w: a target file could not be read", ErrApplyRejected)
	}
	return data, true, nil
}

// read returns an artifact's bytes and re-verifies its digest here as well as in
// the store. Duplicating the check is deliberate: this is the last moment before
// bytes leave the agent's own storage for a source tree.
func (a *FileApplier) read(ctx context.Context, projectID string, ref ArtifactRef) ([]byte, error) {
	reader, err := a.artifacts.Open(ctx, projectID, ref)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := readAllLimited(reader, maxApplyFileBytes)
	if err != nil {
		return nil, err
	}
	if digestOf(data) != ref.Digest {
		return nil, fmt.Errorf("%w: artifact %s no longer matches its digest", ErrTampered, ref.ID)
	}
	return data, nil
}

// commitPlan persists the executed plan as evidence under the emit stage. The
// result travels with it, because "which files were written" is the part a partial
// apply is judged by and it cannot be recomputed later.
func (a *FileApplier) commitPlan(ctx context.Context, project Project, plan ApplyPlan, result ApplyResult) ([]ArtifactRef, error) {
	record := struct {
		Plan   ApplyPlan   `json:"plan"`
		Result ApplyResult `json:"result"`
		At     time.Time   `json:"at"`
	}{Plan: plan, Result: result, At: a.now()}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode apply plan: %w", err)
	}
	batch, err := a.artifacts.Stage(ctx, project.ID, []Draft{{
		Stage: StageEmit, Name: artifactApplyPlan, Kind: KindEvidence, Body: append(encoded, '\n'),
	}})
	if err != nil {
		return nil, err
	}
	refs, err := a.artifacts.Commit(ctx, batch)
	if err != nil {
		if discardErr := a.artifacts.Discard(ctx, batch); discardErr != nil {
			a.logger.Warn("modeling apply staging discard failed", "project", project.ID, "err", classify(discardErr))
		}
		return nil, err
	}
	return refs, nil
}

// record moves the project value and saves it. A failure here is logged rather
// than returned: the files are already in the tree, and the caller has to be told
// what was written even if the bookkeeping did not survive.
func (a *FileApplier) record(ctx context.Context, project Project, refs []ArtifactRef, result ApplyResult) {
	// The state write must survive a canceled request, exactly like the pipeline's
	// failure path: a canceled apply that wrote files must not leave the project
	// claiming it is still waiting for one.
	saveCtx := context.WithoutCancel(ctx)
	working := project.Clone()
	working.Revision++
	var err error
	if result.Partial {
		err = working.ApplyFailed(StageEmit, classify(ErrApplyPartial), refs, a.now())
	} else {
		err = working.Applied(StageEmit, refs, a.now())
	}
	if err != nil {
		a.logger.Warn("modeling apply state rejected", "project", project.ID, "err", classify(err))
		return
	}
	if _, err := a.projects.Save(saveCtx, working); err != nil {
		a.logger.Warn("modeling apply state not saved", "project", project.ID, "err", classify(err))
	}
}

// available is the single "can this build land anything" check. Both halves are
// configuration facts, so they answer ErrApplyUnavailable and the command layer
// renders them as "this build has no QEMU source tree".
func (a *FileApplier) available() error {
	if a.qemuRoot == "" {
		return fmt.Errorf("%w: no QEMU source tree is configured", ErrApplyUnavailable)
	}
	if a.tools == nil {
		return fmt.Errorf("%w: this build may not run the write tool", ErrApplyUnavailable)
	}
	return nil
}

// findArtifact looks a stage's artifact up by name. The *last* match wins, because
// a re-run appends evidence and a redo replaces the stage's list: in both cases the
// newest entry is the one that describes the current output.
func findArtifact(project Project, stage Stage, name string) (ArtifactRef, bool) {
	var found ArtifactRef
	var ok bool
	for _, ref := range project.Artifacts[stage] {
		if ref.Name == name {
			found, ok = ref, true
		}
	}
	return found, ok
}

// digestOf is the one hash function of the apply path, so a base digest recorded
// in a plan and a base digest checked at write time cannot be computed differently.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readAllLimited reads an artifact with a ceiling. The store already enforces a
// per-artifact limit, so a body over this one means the file on disk is not what
// was committed — which is a refusal, not a truncation, because the applier must
// never write a prefix of a source file.
func readAllLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: an artifact could not be read", ErrApplyRejected)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: an artifact is too large to apply", ErrTooLarge)
	}
	return data, nil
}
