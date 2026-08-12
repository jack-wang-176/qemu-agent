package modelingapi

// view.go defines stable caller-facing view DTOs.
//
// Design principles :
//   - A view is a projection, not a store schema. It hides Stage enums, store paths,
//     private metadata, and transient runtime fields.
//   - ProjectView serves callers and is never persisted as project state.
//   - ArtifactDescriptor contains neither paths nor artifact content.
//   - Slices, maps, and byte payloads are defensively copied.
//   - ProjectStatus uses stable product terms such as completed instead of done.

import (
	"strings"
	"time"
)

// ProjectStatus is the stable caller-facing project state.
type ProjectStatus string

const (
	ProjectPending   ProjectStatus = "pending"
	ProjectRunning   ProjectStatus = "running"
	ProjectBlocked   ProjectStatus = "blocked"
	ProjectCompleted ProjectStatus = "completed" // Current modeling.StatusDone maps here.
)

// OperationStatus is the stable result state of one operation invocation.
type OperationStatus string

const (
	OperationSucceeded OperationStatus = "succeeded"
	OperationBlocked   OperationStatus = "blocked"
	OperationFailed    OperationStatus = "failed"
)

// ProjectView is the product projection of a modeling project.
type ProjectView struct {
	ID               ProjectID
	Title            string
	Revision         int
	Status           ProjectStatus
	CurrentOperation OperationName
	Recommended      []OperationDescriptor // Supplied by the Engine.
	Artifacts        []ArtifactDescriptor
	EvidenceCount    int
	PublicError      *PublicError
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ArtifactDescriptor is the path-free, content-free product projection of an artifact.
type ArtifactDescriptor struct {
	ID        ArtifactID
	Operation OperationName
	Name      string
	Kind      string // For example plan, regir, code, diff, or evidence.
	Bytes     int64
	Digest    string
	CreatedAt time.Time
}

// OperationResult is returned by operation-oriented use cases such as Advance and Apply.
type OperationResult struct {
	Project   ProjectView
	Operation OperationName
	Status    OperationStatus
	Artifacts []ArtifactDescriptor
	Evidence  []ArtifactDescriptor
	Summary   string
	Blocked   bool
	Reason    string
	Apply     *ApplyOutcome
}

// ApplyOutcome preserves the exact landing result. A partial apply can return both
// a non-empty result and an error, so adapters must not discard these fields.
type ApplyOutcome struct {
	Written  []string
	Skipped  []string
	Partial  bool
	Reason   string
	Evidence []ArtifactDescriptor
}

// ArtifactContent is the bounded ReadArtifact result. Data is defensively copied.
type ArtifactContent struct {
	Artifact ArtifactDescriptor
	Data     []byte
	Offset   int64
	Next     int64
	EOF      bool
}

// ApplyPreview is returned by PlanApply and binds a specific diff, base, and revision.
type ApplyPreview struct {
	ID              string
	ProjectID       ProjectID
	ProjectRevision int
	Diff            ArtifactDescriptor
	Files           []FileChange
	Summary         string
	ExpiresAt       time.Time
}

// FileChange contains only a relative path, action, digests, and sizes.
type FileChange struct {
	Path      string // Path relative to the QEMU root.
	Kind      string // create or modify; the current implementation does not delete.
	OldBytes  int64
	NewBytes  int64
	OldDigest string
	NewDigest string
}

// ProjectPage is returned by List.
type ProjectPage struct {
	Projects   []ProjectView
	NextCursor string // Empty means there is no next page.
}

// EvidencePage is returned by Evidence.
type EvidencePage struct {
	Evidence   []ArtifactDescriptor
	NextCursor string
}

// ValidateProjectView checks ProjectView field consistency.
func ValidateProjectView(v ProjectView) error {
	if _, err := ParseProjectID(string(v.ID)); err != nil {
		return err
	}
	if err := ValidateTitle(v.Title); err != nil {
		return err
	}
	if v.Revision < 0 {
		return errInvalid("modelingapi: negative revision")
	}
	if v.EvidenceCount < 0 {
		return errInvalid("modelingapi: negative evidence count")
	}
	if v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() || v.UpdatedAt.Before(v.CreatedAt) {
		return errInvalid("modelingapi: invalid project timestamps")
	}
	switch v.Status {
	case ProjectPending, ProjectRunning, ProjectBlocked, ProjectCompleted:
	default:
		return errInvalid("modelingapi: unknown project status: " + string(v.Status))
	}
	if v.CurrentOperation != "" {
		if err := ValidateOperationName(v.CurrentOperation); err != nil {
			return err
		}
	}
	for _, a := range v.Artifacts {
		if err := validateArtifactDescriptor(a); err != nil {
			return err
		}
	}
	for _, op := range v.Recommended {
		if err := ValidateOperationName(op.Name); err != nil {
			return err
		}
	}
	if v.PublicError != nil {
		if err := ValidatePublicError(*v.PublicError); err != nil {
			return err
		}
	}
	return nil
}

func validateArtifactDescriptor(a ArtifactDescriptor) error {
	if _, err := ParseArtifactID(string(a.ID)); err != nil {
		return err
	}
	if a.Name == "" {
		return errMissing("artifact.name")
	}
	if hasControlChar(a.Name) || len(a.Name) > MaxIDBytes {
		return errTooLong()
	}
	if strings.ContainsAny(a.Name, `/\`) || strings.Contains(a.Name, "..") {
		return errInvalid("modelingapi: artifact name must be a bare name")
	}
	if a.Kind == "" {
		return errMissing("artifact.kind")
	}
	if hasControlChar(a.Kind) || len(a.Kind) > MaxIDBytes {
		return errTooLong()
	}
	if a.Bytes < 0 {
		return errInvalid("modelingapi: negative artifact bytes")
	}
	if err := ValidateOperationName(a.Operation); err != nil {
		return err
	}
	if a.Operation == "" {
		return errMissing("artifact.operation")
	}
	if !isLowerHex(a.Digest, 64) {
		return errInvalid("modelingapi: artifact digest must be sha256 hex")
	}
	if !strings.HasPrefix(a.Digest, string(a.ID)) {
		return errInvalid("modelingapi: artifact id does not match digest")
	}
	if a.CreatedAt.IsZero() {
		return errMissing("artifact.created_at")
	}
	return nil
}

// ValidateArtifactDescriptor validates one artifact projection so adapters can
// check converted values before assembling larger views.
func ValidateArtifactDescriptor(a ArtifactDescriptor) error {
	return validateArtifactDescriptor(a)
}

// ValidateArtifactContent checks a bounded read result and its descriptor.
func ValidateArtifactContent(c ArtifactContent) error {
	if err := validateArtifactDescriptor(c.Artifact); err != nil {
		return err
	}
	if c.Offset < 0 || c.Next < c.Offset {
		return errInvalid("modelingapi: invalid artifact content offsets")
	}
	if int64(len(c.Data)) != c.Next-c.Offset {
		return errInvalid("modelingapi: artifact content length does not match offsets")
	}
	if c.Next > c.Artifact.Bytes {
		return errInvalid("modelingapi: artifact content exceeds descriptor size")
	}
	if c.EOF != (c.Next == c.Artifact.Bytes) {
		return errInvalid("modelingapi: artifact content eof is inconsistent")
	}
	return nil
}

// ValidateProjectPage validates a List result page.
func ValidateProjectPage(p ProjectPage) error {
	if len(p.Projects) > MaxPageSize {
		return errTooLong()
	}
	for _, project := range p.Projects {
		if err := ValidateProjectView(project); err != nil {
			return err
		}
	}
	if p.NextCursor != "" && (hasControlChar(p.NextCursor) || len(p.NextCursor) > MaxIDBytes) {
		return errInvalid("modelingapi: invalid project cursor")
	}
	return nil
}

// ValidateEvidencePage validates a page of evidence descriptors.
func ValidateEvidencePage(p EvidencePage) error {
	if len(p.Evidence) > MaxPageSize {
		return errTooLong()
	}
	for _, evidence := range p.Evidence {
		if err := validateArtifactDescriptor(evidence); err != nil {
			return err
		}
		if evidence.Kind != "evidence" {
			return errInvalid("modelingapi: evidence page contains non-evidence artifact")
		}
	}
	if p.NextCursor != "" && (hasControlChar(p.NextCursor) || len(p.NextCursor) > MaxIDBytes) {
		return errInvalid("modelingapi: invalid evidence cursor")
	}
	return nil
}

// ValidateOperationResult validates an OperationResult.
func ValidateOperationResult(r OperationResult) error {
	if err := ValidateProjectView(r.Project); err != nil {
		return err
	}
	if err := ValidateOperationName(r.Operation); err != nil {
		return err
	}
	switch r.Status {
	case OperationSucceeded, OperationBlocked, OperationFailed:
	default:
		return errInvalid("modelingapi: unknown operation status: " + string(r.Status))
	}
	if err := ValidateSummary(r.Summary); err != nil {
		return err
	}
	for _, a := range r.Artifacts {
		if err := validateArtifactDescriptor(a); err != nil {
			return err
		}
	}
	for _, a := range r.Evidence {
		if err := validateArtifactDescriptor(a); err != nil {
			return err
		}
	}
	if r.Blocked && r.Reason == "" {
		return errMissing("reason")
	}
	if r.Reason != "" && hasControlChar(r.Reason) {
		return errControlChar()
	}
	if r.Blocked != (r.Status == OperationBlocked) {
		return errInvalid("modelingapi: blocked flag and status disagree")
	}
	if r.Apply != nil {
		if err := validateApplyOutcome(*r.Apply); err != nil {
			return err
		}
	}
	return nil
}

func validateApplyOutcome(a ApplyOutcome) error {
	if a.Partial && (len(a.Written) == 0 || len(a.Skipped) == 0) {
		return errInvalid("modelingapi: partial apply requires written and skipped paths")
	}
	if !a.Partial && len(a.Written) > 0 && len(a.Skipped) > 0 {
		return errInvalid("modelingapi: written and skipped paths imply partial apply")
	}
	for _, candidate := range append(append([]string(nil), a.Written...), a.Skipped...) {
		if err := validateRelativePath(candidate); err != nil {
			return err
		}
	}
	if a.Reason != "" && (hasControlChar(a.Reason) || len(a.Reason) > MaxIDBytes) {
		return errInvalid("modelingapi: invalid apply reason")
	}
	for _, evidence := range a.Evidence {
		if err := validateArtifactDescriptor(evidence); err != nil {
			return err
		}
	}
	return nil
}

// ValidateApplyPreview validates an ApplyPreview.
func ValidateApplyPreview(p ApplyPreview) error {
	if p.ID == "" {
		return errMissing("preview_id")
	}
	if hasControlChar(p.ID) || len(p.ID) > MaxIDBytes {
		return errTooLong()
	}
	if _, err := ParseProjectID(string(p.ProjectID)); err != nil {
		return err
	}
	if p.ProjectRevision < 0 {
		return errInvalid("modelingapi: negative preview revision")
	}
	if err := validateArtifactDescriptor(p.Diff); err != nil {
		return err
	}
	if len(p.Files) > MaxFileChanges {
		return errTooLong()
	}
	for _, f := range p.Files {
		if len(f.Path) > MaxSourceValueBytes {
			return errTooLong()
		}
		if err := validateRelativePath(f.Path); err != nil {
			return err
		}
		switch f.Kind {
		case "create":
			if f.OldDigest != "" || f.OldBytes != 0 {
				return errInvalid("modelingapi: create change has old content")
			}
		case "modify":
			if !isLowerHex(f.OldDigest, 64) {
				return errInvalid("modelingapi: modify change requires old digest")
			}
		default:
			return errInvalid("modelingapi: unknown file change kind: " + f.Kind)
		}
		if !isLowerHex(f.NewDigest, 64) || f.OldBytes < 0 || f.NewBytes < 0 {
			return errInvalid("modelingapi: invalid file change content")
		}
	}
	if len(p.Summary) > MaxPreviewSummaryBytes {
		return errTooLong()
	}
	if p.ExpiresAt.IsZero() {
		return errMissing("expires_at")
	}
	return nil
}
