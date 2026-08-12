package modelingapi

// request.go defines public identifiers and untrusted request DTOs.
//
// Design principles :
//   - Opaque IDs use distinct string types and Parse functions without exposing store paths.
//   - ProjectID currently accepts mp-<16hex>; this is API validation, not a storage promise.
//   - OperationName validates [a-z][a-z0-9._-]{0,63} and is not a fixed enum.
//   - SourceRef names a logical workspace source, never a trusted absolute path.
//   - Mutating requests carry ExpectedRevision; zero means not supplied during migration.

import "strings"

// ProjectID is an opaque project identifier validated by ParseProjectID.
type ProjectID string

// ArtifactID is an opaque artifact identifier.
type ArtifactID string

// OperationName is a discoverable operation name that permits future operations.
type OperationName string

// ParseProjectID validates and returns a ProjectID in the current mp-<16hex> form.
func ParseProjectID(s string) (ProjectID, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", errMissing("project_id")
	}
	if !strings.HasPrefix(t, "mp-") {
		return "", errInvalid("modelingapi: project id must start with mp-")
	}
	hex := t[3:]
	if len(hex) != 16 {
		return "", errInvalid("modelingapi: project id must be mp-<16hex>")
	}
	for _, r := range hex {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", errInvalid("modelingapi: project id must be lowercase hex")
		}
	}
	return ProjectID(t), nil
}

// ParseArtifactID validates an ArtifactID, currently the first 16 SHA-256 hex digits.
func ParseArtifactID(s string) (ArtifactID, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", errMissing("artifact_id")
	}
	if len(t) != 16 {
		return "", errInvalid("modelingapi: artifact id must be 16 hex")
	}
	for _, r := range t {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", errInvalid("modelingapi: artifact id must be lowercase hex")
		}
	}
	return ArtifactID(t), nil
}

// ParseOperationName validates and returns an OperationName.
func ParseOperationName(s string) (OperationName, error) {
	op := OperationName(s)
	if err := ValidateOperationName(op); err != nil {
		return "", err
	}
	return op, nil
}

// SourceRef is an untrusted source reference supplied by a caller.
//
// For Kind="workspace_path", Value is a logical workspace-relative path such as
// references/rm0041.pdf. A later adapter or Effect Port resolves it under policy.
type SourceRef struct {
	Kind   string
	Value  string
	Digest string
}

// CreateRequest is the stable DTO for /modeling new.
type CreateRequest struct {
	Title string
}

// ListRequest is the stable DTO for /modeling list.
type ListRequest struct {
	Limit  int    // <=0 requests the adapter default.
	Cursor string // The current store has no cursor; its adapter returns an empty NextCursor.
}

// ShowRequest is the stable DTO for /modeling show.
type ShowRequest struct {
	ProjectID ProjectID
}

// AdvanceRequest is the stable DTO for /modeling advance.
type AdvanceRequest struct {
	ProjectID        ProjectID
	Operation        OperationName // Empty selects the current or recommended operation.
	Instruction      string
	Sources          []SourceRef
	ExpectedRevision int
}

// ResetRequest is the stable DTO for /modeling reset.
type ResetRequest struct {
	ProjectID        ProjectID
	Operation        OperationName
	ExpectedRevision int
	Confirmation     string // A1 carries the token; A5 generates and validates it.
}

// ReadArtifactRequest is the stable bounded-read DTO used by diff and evidence.
type ReadArtifactRequest struct {
	ProjectID  ProjectID
	ArtifactID ArtifactID
	Offset     int64
	Limit      int // <=0 requests the adapter default.
}

// PlanApplyRequest is the preview phase of /modeling apply.
type PlanApplyRequest struct {
	ProjectID        ProjectID
	ExpectedRevision int
}

// ApplyRequest is the confirmed execution phase of /modeling apply.
type ApplyRequest struct {
	ProjectID        ProjectID
	ExpectedRevision int
	PreviewID        string
	ApprovalToken    string
}

// EvidenceRequest is the stable DTO for /modeling evidence.
type EvidenceRequest struct {
	ProjectID ProjectID
	Limit     int
	Cursor    string
}

// ValidateAdvanceRequest validates an AdvanceRequest.
func ValidateAdvanceRequest(r AdvanceRequest) error {
	if _, err := ParseProjectID(string(r.ProjectID)); err != nil {
		return err
	}
	if err := ValidateOperationName(r.Operation); err != nil {
		return err
	}
	if err := ValidateInstruction(r.Instruction); err != nil {
		return err
	}
	if err := ValidateSources(r.Sources); err != nil {
		return err
	}
	if r.ExpectedRevision < 0 {
		return errInvalid("modelingapi: negative expected_revision")
	}
	return nil
}

// ValidateReadArtifactRequest validates a ReadArtifactRequest.
func ValidateReadArtifactRequest(r ReadArtifactRequest) error {
	if _, err := ParseProjectID(string(r.ProjectID)); err != nil {
		return err
	}
	if _, err := ParseArtifactID(string(r.ArtifactID)); err != nil {
		return err
	}
	if r.Offset < 0 {
		return errInvalid("modelingapi: negative offset")
	}
	if r.Limit < 0 {
		return errInvalid("modelingapi: negative limit")
	}
	if r.Limit > MaxPageSize {
		return errTooLong()
	}
	return nil
}

// ValidateApplyRequest validates an ApplyRequest.
func ValidateApplyRequest(r ApplyRequest) error {
	if _, err := ParseProjectID(string(r.ProjectID)); err != nil {
		return err
	}
	if r.PreviewID == "" {
		return errMissing("preview_id")
	}
	if hasControlChar(r.PreviewID) || len(r.PreviewID) > MaxIDBytes {
		return errTooLong()
	}
	if r.ApprovalToken == "" {
		return errMissing("approval_token")
	}
	if hasControlChar(r.ApprovalToken) || len(r.ApprovalToken) > MaxIDBytes {
		return errTooLong()
	}
	if r.ExpectedRevision < 0 {
		return errInvalid("modelingapi: negative expected_revision")
	}
	return nil
}

// ValidateResetRequest validates a ResetRequest.
func ValidateResetRequest(r ResetRequest) error {
	if _, err := ParseProjectID(string(r.ProjectID)); err != nil {
		return err
	}
	if err := ValidateOperationName(r.Operation); err != nil {
		return err
	}
	if r.ExpectedRevision < 0 {
		return errInvalid("modelingapi: negative expected_revision")
	}
	if r.Confirmation != "" {
		if hasControlChar(r.Confirmation) || len(r.Confirmation) > MaxIDBytes {
			return errTooLong()
		}
	}
	return nil
}

// ValidateCreateRequest validates a CreateRequest.
func ValidateCreateRequest(r CreateRequest) error {
	return ValidateTitle(r.Title)
}

// ValidateListRequest validates a ListRequest.
func ValidateListRequest(r ListRequest) error {
	if r.Limit < 0 {
		return errInvalid("modelingapi: negative limit")
	}
	if r.Limit > MaxPageSize {
		return errTooLong()
	}
	if r.Cursor != "" && (hasControlChar(r.Cursor) || len(r.Cursor) > MaxIDBytes) {
		return errTooLong()
	}
	return nil
}

// ValidateEvidenceRequest validates an EvidenceRequest.
func ValidateEvidenceRequest(r EvidenceRequest) error {
	if _, err := ParseProjectID(string(r.ProjectID)); err != nil {
		return err
	}
	if err := ValidatePageSize(r.Limit); err != nil {
		return err
	}
	if r.Cursor != "" && (hasControlChar(r.Cursor) || len(r.Cursor) > MaxIDBytes) {
		return errTooLong()
	}
	return nil
}

// ValidatePlanApplyRequest validates a PlanApplyRequest.
func ValidatePlanApplyRequest(r PlanApplyRequest) error {
	if _, err := ParseProjectID(string(r.ProjectID)); err != nil {
		return err
	}
	if r.ExpectedRevision < 0 {
		return errInvalid("modelingapi: negative expected_revision")
	}
	return nil
}

// ValidateShowRequest validates a ShowRequest.
func ValidateShowRequest(r ShowRequest) error {
	if _, err := ParseProjectID(string(r.ProjectID)); err != nil {
		return err
	}
	return nil
}
