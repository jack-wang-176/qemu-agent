package pipelineapi

// request.go defines validation, cloning, and read-only query contracts for the
// values that cross the modelingapp-to-engine boundary.

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxIdentifierBytes  = 128
	maxOperationBytes   = 64
	maxInstructionRunes = 16 * 1024
)

// QueryPort contains read-only operations that do not belong in Engine.Execute.
type QueryPort interface {
	List(ctx context.Context, query ListQuery) (ProjectPage, error)
	ReadArtifact(ctx context.Context, query ArtifactQuery) (ArtifactContent, error)
	Evidence(ctx context.Context, query EvidenceQuery) (ArtifactPage, error)
}

// ListQuery requests a bounded page of projects visible to Scope.
type ListQuery struct {
	Scope  Scope
	Limit  int
	Cursor string
}

// ArtifactQuery requests bounded content for one project artifact.
type ArtifactQuery struct {
	Scope      Scope
	ProjectID  ProjectID
	ArtifactID ArtifactID
	Offset     int64
	Limit      int
}

// EvidenceQuery requests a bounded page of evidence descriptors.
type EvidenceQuery struct {
	Scope     Scope
	ProjectID ProjectID
	Limit     int
	Cursor    string
}

// ProjectPage is a stable page of internal engine projections.
type ProjectPage struct {
	Projects   []EngineView
	NextCursor string
}

// ArtifactPage is a stable page of artifact descriptors.
type ArtifactPage struct {
	Artifacts  []ArtifactDescriptor
	NextCursor string
}

// ArtifactContent is a bounded artifact read. Data is defensively copied.
type ArtifactContent struct {
	Artifact ArtifactDescriptor
	Data     []byte
	Offset   int64
	Next     int64
	EOF      bool
}

// Validate checks the trusted scope identifiers.
func (s Scope) Validate() error {
	if err := validateRequiredIdentifier("workspace_id", s.WorkspaceID); err != nil {
		return err
	}
	return validateOptionalIdentifier("user_id", s.UserID)
}

// Validate checks trusted invocation identifiers and canonical formatting.
func (i InvocationContext) Validate() error {
	if err := validateRequiredIdentifier("request_id", i.RequestID); err != nil {
		return err
	}
	if err := validateRequiredIdentifier("trace_id", i.TraceID); err != nil {
		return err
	}
	if err := validateRequiredIdentifier("channel", i.Channel); err != nil {
		return err
	}
	if err := validateOptionalIdentifier("session_id", i.SessionID); err != nil {
		return err
	}
	return validateOptionalIdentifier("session_key", i.SessionKey)
}

// Validate checks whether an ExecuteRequest is safe to dispatch to an Engine.
func (r ExecuteRequest) Validate() error {
	if err := r.Project.Validate(); err != nil {
		return err
	}
	if err := validateOperation(r.Operation); err != nil {
		return err
	}
	if r.ExpectedRevision < 0 {
		return fmt.Errorf("pipelineapi: expected revision must not be negative")
	}
	if r.ExpectedRevision > 0 && r.ExpectedRevision != r.Project.Revision {
		return fmt.Errorf("pipelineapi: expected revision does not match project snapshot")
	}
	if utf8.RuneCountInString(r.Instruction) > maxInstructionRunes || hasControl(r.Instruction) {
		return fmt.Errorf("pipelineapi: instruction is invalid")
	}
	for index, source := range r.Sources {
		if strings.TrimSpace(source.Kind) == "" || strings.TrimSpace(source.Value) == "" {
			return fmt.Errorf("pipelineapi: source %d is incomplete", index)
		}
		if hasControl(source.Kind) || hasControl(source.Value) || hasControl(source.Digest) {
			return fmt.Errorf("pipelineapi: source %d contains control characters", index)
		}
	}
	if err := r.Invocation.Validate(); err != nil {
		return err
	}
	return ValidatePorts(r.Ports)
}

// Validate checks the identity and revision of an execution snapshot.
func (s ProjectSnapshot) Validate() error {
	if err := validateRequiredIdentifier("project_id", string(s.ID)); err != nil {
		return err
	}
	if err := s.Scope.Validate(); err != nil {
		return err
	}
	if s.Revision < 0 {
		return fmt.Errorf("pipelineapi: project revision must not be negative")
	}
	return nil
}

// Clone returns a defensive copy of an ExecuteRequest.
func (r ExecuteRequest) Clone() ExecuteRequest {
	clone := r
	clone.Sources = append([]SourceRef(nil), r.Sources...)
	return clone
}

// Clone returns a defensive copy of an ExecuteResult.
func (r ExecuteResult) Clone() ExecuteResult {
	clone := r
	clone.Project = r.Project.Clone()
	clone.Artifacts = cloneArtifacts(r.Artifacts)
	clone.Evidence = cloneArtifacts(r.Evidence)
	if r.Apply != nil {
		outcome := *r.Apply
		outcome.Written = append([]string(nil), r.Apply.Written...)
		outcome.Skipped = append([]string(nil), r.Apply.Skipped...)
		outcome.Evidence = cloneArtifacts(r.Apply.Evidence)
		clone.Apply = &outcome
	}
	return clone
}

// Clone returns a defensive copy of an ApplyPreview.
func (p ApplyPreview) Clone() ApplyPreview {
	clone := p
	clone.Files = append([]FileChange(nil), p.Files...)
	return clone
}

// Clone returns a defensive copy of an EngineView.
func (v EngineView) Clone() EngineView {
	clone := v
	clone.Recommended = append([]OperationDescriptor(nil), v.Recommended...)
	clone.Artifacts = cloneArtifacts(v.Artifacts)
	return clone
}

// Clone returns a defensive copy of an ArtifactContent.
func (c ArtifactContent) Clone() ArtifactContent {
	clone := c
	clone.Data = append([]byte(nil), c.Data...)
	return clone
}

func cloneArtifacts(values []ArtifactDescriptor) []ArtifactDescriptor {
	return append([]ArtifactDescriptor(nil), values...)
}

func validateOperation(operation OperationName) error {
	raw := string(operation)
	if raw == "" {
		return fmt.Errorf("pipelineapi: operation is required")
	}
	if strings.TrimSpace(raw) != raw || len(raw) > maxOperationBytes || hasControl(raw) {
		return fmt.Errorf("pipelineapi: operation is invalid")
	}
	for index, r := range raw {
		if index == 0 {
			if r < 'a' || r > 'z' {
				return fmt.Errorf("pipelineapi: operation is invalid")
			}
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return fmt.Errorf("pipelineapi: operation is invalid")
		}
	}
	return nil
}

func validateRequiredIdentifier(name, value string) error {
	if value == "" {
		return fmt.Errorf("pipelineapi: %s is required", name)
	}
	return validateOptionalIdentifier(name, value)
}

func validateOptionalIdentifier(name, value string) error {
	if strings.TrimSpace(value) != value || len(value) > maxIdentifierBytes || hasControl(value) {
		return fmt.Errorf("pipelineapi: %s is invalid", name)
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
