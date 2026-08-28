package runtime

// repository.go — Repository Port Adapter (A4).
//
// Wraps the existing modeling.ProjectStore and modeling.ArtifactStore as
// pipelineapi.Repository. This phase adapts interfaces without changing the disk format.
//
// Design rules (v1-04, section ten, A4): wrap the existing file stores, keep
// every port replaceable with a fake, and preserve current behavior.

import (
	"context"
	"fmt"
	"io"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// RepositoryAdapter wraps modeling.ProjectStore and ArtifactStore.
type RepositoryAdapter struct {
	projects  modeling.ProjectStore
	artifacts modeling.ArtifactStore
	scope     pipelineapi.Scope
}

// NewRepositoryAdapter constructs the repository port adapter.
// scope is the authorization scope supplied by the composition root/adapter.
func NewRepositoryAdapter(
	projects modeling.ProjectStore,
	artifacts modeling.ArtifactStore,
	scope pipelineapi.Scope,
) *RepositoryAdapter {
	return &RepositoryAdapter{
		projects:  projects,
		artifacts: artifacts,
		scope:     scope,
	}
}

// CreateProject creates a new project record.
func (a *RepositoryAdapter) CreateProject(ctx context.Context, rec pipelineapi.ProjectRecord) (pipelineapi.ProjectRecord, error) {
	p := modeling.Project{
		ID:          string(rec.ID),
		Title:       rec.Title,
		WorkspaceID: rec.Scope.WorkspaceID,
		UserID:      rec.Scope.UserID,
		Current:     modeling.Stage(string(rec.Current)),
		Status:      modeling.Status(string(rec.Status)),
		Revision:    rec.Revision,
	}
	if p.Current == "" {
		p.Current = modeling.StagePlan
	}
	if p.Status == "" {
		p.Status = modeling.StatusPending
	}
	created, err := a.projects.Create(ctx, p)
	if err != nil {
		return pipelineapi.ProjectRecord{}, mapCurrentError(fmt.Errorf("repository create: %w", err))
	}
	return projectToRecord(created), nil
}

// GetProject reads a project by ID and scope.
func (a *RepositoryAdapter) GetProject(ctx context.Context, id pipelineapi.ProjectID, scope pipelineapi.Scope) (pipelineapi.ProjectRecord, error) {
	p, err := a.projects.Get(ctx, string(id), toModelingScope(scope))
	if err != nil {
		return pipelineapi.ProjectRecord{}, mapCurrentError(fmt.Errorf("repository get: %w", err))
	}
	return projectToRecord(p), nil
}

// ListProjects lists project records visible to the query scope.
func (a *RepositoryAdapter) ListProjects(ctx context.Context, q pipelineapi.ProjectQuery) ([]pipelineapi.ProjectRecord, error) {
	mq := modeling.Query{
		WorkspaceID: q.Scope.WorkspaceID,
		UserID:      q.Scope.UserID,
		Limit:       q.Limit,
	}
	projects, err := a.projects.List(ctx, mq)
	if err != nil {
		return nil, mapCurrentError(fmt.Errorf("repository list: %w", err))
	}
	out := make([]pipelineapi.ProjectRecord, len(projects))
	for i, p := range projects {
		out[i] = projectToRecord(p)
	}
	return out, nil
}

// CompareAndSaveProject performs the currently available best-effort
// optimistic revision check. modeling.ProjectStore.Save has no CAS primitive;
// a truly atomic check must be implemented by the store in a later refactor.
func (a *RepositoryAdapter) CompareAndSaveProject(ctx context.Context, rec pipelineapi.ProjectRecord, expectedRevision int) (pipelineapi.ProjectRecord, error) {
	if rec.Revision != expectedRevision {
		return pipelineapi.ProjectRecord{}, mapCurrentError(fmt.Errorf("repository CAS: revision mismatch (got %d, expected %d)", rec.Revision, expectedRevision))
	}
	p := recordToProject(rec)
	p.Revision = expectedRevision + 1
	saved, err := a.projects.Save(ctx, p)
	if err != nil {
		return pipelineapi.ProjectRecord{}, mapCurrentError(fmt.Errorf("repository save: %w", err))
	}
	return projectToRecord(saved), nil
}

// StageArtifacts stages artifact drafts and returns an opaque Batch handle.
func (a *RepositoryAdapter) StageArtifacts(ctx context.Context, projectID pipelineapi.ProjectID, drafts []pipelineapi.ArtifactDraft) (pipelineapi.Batch, error) {
	modelingDrafts := make([]modeling.Draft, len(drafts))
	for i, d := range drafts {
		modelingDrafts[i] = modeling.Draft{
			Stage: modeling.Stage(d.Operation),
			Name:  d.Name,
			Kind:  modeling.Kind(d.Kind),
			Body:  d.Bytes,
		}
	}
	batch, err := a.artifacts.Stage(ctx, string(projectID), modelingDrafts)
	if err != nil {
		return nil, mapCurrentError(fmt.Errorf("repository stage: %w", err))
	}
	return &batchWrapper{batch: batch}, nil
}

// CommitArtifacts commits staged artifacts and returns durable descriptors.
func (a *RepositoryAdapter) CommitArtifacts(ctx context.Context, b pipelineapi.Batch) ([]pipelineapi.ArtifactDescriptor, error) {
	bw, ok := b.(*batchWrapper)
	if !ok {
		return nil, mapCurrentError(fmt.Errorf("repository commit: invalid batch type %T", b))
	}
	refs, err := a.artifacts.Commit(ctx, bw.batch)
	if err != nil {
		return nil, mapCurrentError(fmt.Errorf("repository commit: %w", err))
	}
	out := make([]pipelineapi.ArtifactDescriptor, len(refs))
	for i, r := range refs {
		out[i] = pipelineapi.ArtifactDescriptor{
			ID:        pipelineapi.ArtifactID(r.ID),
			Operation: pipelineapi.OperationName(string(r.Stage)),
			Name:      r.Name,
			Kind:      string(r.Kind),
			Bytes:     r.Bytes,
			Digest:    r.Digest,
			CreatedAt: r.Created,
		}
	}
	return out, nil
}

// AbortArtifacts discards staged artifact drafts.
func (a *RepositoryAdapter) AbortArtifacts(ctx context.Context, b pipelineapi.Batch) error {
	bw, ok := b.(*batchWrapper)
	if !ok {
		return mapCurrentError(fmt.Errorf("repository abort: invalid batch type %T", b))
	}
	return a.artifacts.Discard(ctx, bw.batch)
}

// OpenArtifact opens a committed artifact after checking its project reference.
func (a *RepositoryAdapter) OpenArtifact(ctx context.Context, projectID pipelineapi.ProjectID, artifactID pipelineapi.ArtifactID) (io.ReadCloser, error) {
	// Load the project first and verify that it references the artifact.
	p, err := a.projects.Get(ctx, string(projectID), toModelingScope(a.scope))
	if err != nil {
		return nil, mapCurrentError(fmt.Errorf("repository open: get project: %w", err))
	}
	ref, ok := findArtifactRef(p, string(artifactID))
	if !ok {
		return nil, mapCurrentError(fmt.Errorf("repository open: artifact %s not referenced by project %s", artifactID, projectID))
	}
	return a.artifacts.Open(ctx, string(projectID), ref)
}

// Ensure RepositoryAdapter satisfies pipelineapi.Repository at compile time.
var _ pipelineapi.Repository = (*RepositoryAdapter)(nil)

// batchWrapper wraps modeling.Batch as pipelineapi.Batch.
type batchWrapper struct {
	batch modeling.Batch
}

// IsOpaque satisfies the opaque pipelineapi.Batch contract.
func (b *batchWrapper) IsOpaque() {}

// findArtifactRef searches both Project.Artifacts and Project.Evidence by ID.
func findArtifactRef(p modeling.Project, id string) (modeling.ArtifactRef, bool) {
	for _, refs := range p.Artifacts {
		for _, r := range refs {
			if r.ID == id {
				return r, true
			}
		}
	}
	for _, r := range p.Evidence {
		if r.ID == id {
			return r, true
		}
	}
	return modeling.ArtifactRef{}, false
}

// projectToRecord maps modeling.Project to pipelineapi.ProjectRecord.
func projectToRecord(p modeling.Project) pipelineapi.ProjectRecord {
	artifacts := make([]pipelineapi.ArtifactDescriptor, 0)
	for stage, refs := range p.Artifacts {
		for _, r := range refs {
			artifacts = append(artifacts, descriptorFromRef(r))
		}
		_ = stage
	}
	evidence := make([]pipelineapi.ArtifactDescriptor, 0, len(p.Evidence))
	for _, r := range p.Evidence {
		evidence = append(evidence, descriptorFromRef(r))
	}
	return pipelineapi.ProjectRecord{
		ID:        pipelineapi.ProjectID(p.ID),
		Scope:     pipelineapi.Scope{WorkspaceID: p.WorkspaceID, UserID: p.UserID},
		Title:     p.Title,
		Current:   pipelineapi.OperationName(string(p.Current)),
		Status:    engineStatusFromModeling(p.Status),
		Revision:  p.Revision,
		Artifacts: artifacts,
		Evidence:  evidence,
		LastError: p.LastError,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// recordToProject maps pipelineapi.ProjectRecord back to modeling.Project.
func recordToProject(rec pipelineapi.ProjectRecord) modeling.Project {
	artifacts := make(map[modeling.Stage][]modeling.ArtifactRef)
	for _, d := range rec.Artifacts {
		r := refFromDescriptor(d)
		artifacts[r.Stage] = append(artifacts[r.Stage], r)
	}
	evidence := make([]modeling.ArtifactRef, 0, len(rec.Evidence))
	for _, d := range rec.Evidence {
		evidence = append(evidence, refFromDescriptor(d))
	}
	return modeling.Project{
		ID:          string(rec.ID),
		Title:       rec.Title,
		WorkspaceID: rec.Scope.WorkspaceID,
		UserID:      rec.Scope.UserID,
		Current:     modeling.Stage(string(rec.Current)),
		Status:      modeling.Status(string(rec.Status)),
		Revision:    rec.Revision,
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
		Artifacts:   artifacts,
		Evidence:    evidence,
		LastError:   rec.LastError,
	}
}

func descriptorFromRef(r modeling.ArtifactRef) pipelineapi.ArtifactDescriptor {
	return pipelineapi.ArtifactDescriptor{ID: pipelineapi.ArtifactID(r.ID), Operation: pipelineapi.OperationName(string(r.Stage)), Name: r.Name, Kind: string(r.Kind), Bytes: r.Bytes, Digest: r.Digest, CreatedAt: r.Created}
}

func refFromDescriptor(d pipelineapi.ArtifactDescriptor) modeling.ArtifactRef {
	return modeling.ArtifactRef{ID: string(d.ID), Stage: modeling.Stage(d.Operation), Name: d.Name, Kind: modeling.Kind(d.Kind), Bytes: d.Bytes, Digest: d.Digest, Created: d.CreatedAt}
}

// engineStatusFromModeling maps modeling.Status to pipelineapi.ProjectStatus.
func engineStatusFromModeling(s modeling.Status) pipelineapi.ProjectStatus {
	switch s {
	case modeling.StatusPending:
		return pipelineapi.StatusPending
	case modeling.StatusRunning:
		return pipelineapi.StatusRunning
	case modeling.StatusBlocked:
		return pipelineapi.StatusBlocked
	case modeling.StatusDone:
		return pipelineapi.StatusDone
	default:
		return pipelineapi.StatusPending
	}
}

// toModelingScope converts pipelineapi.Scope to modeling.Scope.
func toModelingScope(s pipelineapi.Scope) modeling.Scope {
	return modeling.Scope{WorkspaceID: s.WorkspaceID, UserID: s.UserID}
}
