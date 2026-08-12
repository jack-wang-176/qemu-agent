package pipelineapi

// repository.go - Persistence contract for projects and artifacts.
//
// The repository exposes domain persistence semantics without revealing file,
// staging, manifest, or lock paths. CompareAndSaveProject is an optimistic CAS.

import (
	"context"
	"io"
	"time"
)

// Repository persists project records and immutable artifacts for an Engine.
type Repository interface {
	// Project operations.
	CreateProject(ctx context.Context, rec ProjectRecord) (ProjectRecord, error)
	GetProject(ctx context.Context, id ProjectID, scope Scope) (ProjectRecord, error)
	ListProjects(ctx context.Context, q ProjectQuery) ([]ProjectRecord, error)
	CompareAndSaveProject(ctx context.Context, rec ProjectRecord, expectedRevision int) (ProjectRecord, error)

	// Artifact operations.
	StageArtifacts(ctx context.Context, projectID ProjectID, drafts []ArtifactDraft) (Batch, error)
	CommitArtifacts(ctx context.Context, b Batch) ([]ArtifactDescriptor, error)
	AbortArtifacts(ctx context.Context, b Batch) error
	OpenArtifact(ctx context.Context, projectID ProjectID, artifactID ArtifactID) (io.ReadCloser, error)
}

// ProjectRecord is the internal persistence value shared by Engine and Repository.
type ProjectRecord struct {
	ID        ProjectID
	Scope     Scope
	Title     string
	Current   OperationName // Current engine operation.
	Status    ProjectStatus
	Revision  int
	Artifacts []ArtifactDescriptor // Committed artifacts.
	Evidence  []ArtifactDescriptor
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectQuery selects projects visible to a trusted scope.
type ProjectQuery struct {
	Scope Scope
	Limit int
	// Cursor may be ignored by adapters whose current store has no cursor support.
}

// ArtifactDraft is engine-produced content that has not been committed.
type ArtifactDraft struct {
	Operation OperationName
	Name      string
	Kind      string
	Bytes     []byte // Complete artifact content to stage.
}

// Batch is an opaque staging handle returned by StageArtifacts.
type Batch interface {
	// IsOpaque prevents engines from constructing or inspecting concrete batches.
	IsOpaque()
}
