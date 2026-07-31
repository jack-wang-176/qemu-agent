package modeling

// apply.go is the landing side of the pipeline: the only code in the project that
// may change a QEMU source tree. Emit never touches that tree — it writes into
// the artifact store — so everything here happens after a human has read a diff.
//
// This file holds the value layer plus the seam. The rule the values encode is
// "nothing is written that was not described first": an ApplyPlan names every
// file, the artifact whose bytes go there, and (for a modification) the digest
// the target must still have. A plan that no longer matches the tree is refused
// rather than merged.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ApplyAction is what happens to one file. There is no "delete" and no
// "overwrite": a generated device is new code plus a small number of edits to
// build files, and anything else means the plan was computed against a different
// tree.
type ApplyAction string

const (
	ApplyCreate ApplyAction = "create" // the target must not exist
	ApplyModify ApplyAction = "modify" // the target must exist and match BaseDigest
)

// FileChange is one file of an ApplyPlan.
//
// Path is relative to QemuRoot and always uses forward slashes, because it is
// also rendered into the diff and compared against the plan a reviewer approved;
// a path that differs only in separator would let the same change be approved
// once and applied somewhere else.
//
// The two actions differ in more than their preconditions. ApplyCreate writes the
// artifact's bytes as the whole file. ApplyModify *appends* them to what is there,
// after checking the existing content still hashes to BaseDigest — it is not a
// patch and not an overwrite. That is enough for the only edits a generated device
// needs (a source line in a meson.build, an entry in a Kconfig), and it means a
// failed apply can never destroy code the agent did not write.
type FileChange struct {
	Path       string      `json:"path"`
	Action     ApplyAction `json:"action"`
	Ref        ArtifactRef `json:"ref"`         // the artifact whose bytes are written
	BaseDigest string      `json:"base_digest"` // modify only: sha256 the target must still have
}

// ApplyPlan is the reviewable description of a landing. It is produced from
// committed artifacts only, so the bytes a reviewer reads in `/modeling diff` are
// the bytes an apply writes.
type ApplyPlan struct {
	ProjectID string       `json:"project_id"`
	Files     []FileChange `json:"files"`
	Diff      ArtifactRef  `json:"diff"` // the device.diff artifact the plan was rendered from
}

// ApplyResult is the report of a landing attempt. Written and Skipped are exact
// because there is deliberately no rollback: half a revert is worse than half an
// apply, so the contract is "tell the operator precisely what is on disk".
type ApplyResult struct {
	ProjectID string        `json:"project_id"`
	Written   []string      `json:"written"`            // paths actually written, in order
	Skipped   []string      `json:"skipped"`            // paths never attempted, because a write failed first
	Partial   bool          `json:"partial"`            // true when Written and Skipped are both non-empty
	Reason    string        `json:"reason,omitempty"`   // failure category, never a message
	Evidence  []ArtifactRef `json:"evidence,omitempty"` // the applied plan, persisted as evidence
}

// Applier is the seam the command layer gets. It takes an id and a Scope rather
// than a Project value on purpose: the applier re-loads the project under the
// caller's scope itself, so the one component that writes into a source tree
// never trusts a project handed to it by its caller.
type Applier interface {
	Plan(ctx context.Context, id string, scope Scope) (ApplyPlan, error)
	Apply(ctx context.Context, id string, scope Scope) (ApplyResult, error)
}

// Apply-specific failure categories. They are sentinels for the same reason the
// stage errors are: classify turns them into a category, and the message they
// carry — which may quote a path or a digest — never reaches state or a log.
var (
	// ErrApplyUnavailable means this build cannot land anything: no QemuRoot, or
	// the modeling capability is off.
	ErrApplyUnavailable = errors.New("modeling apply is not available in this configuration")
	// ErrApplyRejected covers every pre-write refusal: path escape, a create
	// whose target exists, a modify whose base digest moved, a denied tool call.
	ErrApplyRejected = errors.New("modeling apply was rejected before writing")
	// ErrApplyPartial means some files were written and some were not. It is
	// returned with a populated ApplyResult, because the report is the point.
	ErrApplyPartial = errors.New("modeling apply wrote only part of its plan")
)

// Validate is the check an Applier runs before the first write, and it is a
// method on the value so a test can pin the rules without a filesystem.
func (p ApplyPlan) Validate() error {
	if err := ValidateProjectID(p.ProjectID); err != nil {
		return err
	}
	if len(p.Files) == 0 {
		return fmt.Errorf("%w: plan has no files", ErrApplyRejected)
	}
	seen := make(map[string]struct{}, len(p.Files))
	for _, change := range p.Files {
		if err := change.Validate(); err != nil {
			return err
		}
		// A plan that lists one path twice would make "what will be written"
		// ambiguous, and the second entry would silently win.
		if _, duplicate := seen[change.Path]; duplicate {
			return fmt.Errorf("%w: plan lists %q twice", ErrApplyRejected, change.Path)
		}
		seen[change.Path] = struct{}{}
	}
	if err := validateRef(p.Diff); err != nil {
		return err
	}
	if p.Diff.Kind != KindDiff {
		return fmt.Errorf("%w: plan diff has kind %q", ErrApplyRejected, p.Diff.Kind)
	}
	return nil
}

// Validate checks one change. The path rules are enforced here, on the value,
// rather than only at write time: a plan is persisted as evidence and rendered to
// a reviewer, so an unusable path must never become part of an approved plan.
func (c FileChange) Validate() error {
	if err := c.validatePath(); err != nil {
		return err
	}
	cleaned := strings.TrimSpace(c.Path)
	switch c.Action {
	case ApplyCreate:
		if c.BaseDigest != "" {
			return fmt.Errorf("%w: create of %q carries a base digest", ErrApplyRejected, cleaned)
		}
	case ApplyModify:
		// Without a base digest a modify is an overwrite, which is exactly the
		// operation this package refuses to perform.
		if !digestPattern.MatchString(c.BaseDigest) {
			return fmt.Errorf("%w: modify of %q has no usable base digest", ErrApplyRejected, cleaned)
		}
	default:
		return fmt.Errorf("%w: unknown apply action %q", ErrApplyRejected, c.Action)
	}
	if err := validateRef(c.Ref); err != nil {
		return err
	}
	// Evidence is a record of what happened, never an input to a write.
	if c.Ref.Kind == KindEvidence {
		return fmt.Errorf("%w: %q would be written from evidence", ErrApplyRejected, cleaned)
	}
	return nil
}

// validatePath is the half of Validate that needs nothing but the path string.
//
// It is separate because a caller that is about to stat the path has to reject an
// unusable one *first* — a path with ".." in it must never reach the filesystem,
// even to be resolved — while the rest of Validate depends on a base digest that
// can only be computed by reading the file. Validate still runs every check, so
// nothing is skipped by calling this one early.
func (c FileChange) validatePath() error {
	cleaned := strings.TrimSpace(c.Path)
	switch {
	case cleaned == "":
		return fmt.Errorf("%w: change has no path", ErrApplyRejected)
	case strings.ContainsRune(cleaned, '\\'):
		// Backslashes are rejected instead of normalized: the plan a reviewer
		// approved and the path a write uses have to be the same string.
		return fmt.Errorf("%w: path %q must use forward slashes", ErrApplyRejected, cleaned)
	case path.IsAbs(cleaned):
		return fmt.Errorf("%w: path %q must be relative to the QEMU root", ErrApplyRejected, cleaned)
	case cleaned != path.Clean(cleaned):
		// "hw/./x.c" and "hw/x.c" are the same file with two spellings; refusing
		// the unclean one keeps digest bookkeeping keyed by exactly one name.
		return fmt.Errorf("%w: path %q is not in canonical form", ErrApplyRejected, cleaned)
	case cleaned == ".." || strings.HasPrefix(cleaned, "../"):
		return fmt.Errorf("%w: path %q escapes the QEMU root", ErrApplyRejected, cleaned)
	}
	return nil
}
