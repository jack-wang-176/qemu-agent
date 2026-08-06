package runtime

// repository.go — Repository Port Adapter (A4).
//
// 把现有 modeling.ProjectStore + modeling.ArtifactStore 包装为
// pipelineapi.Repository。第一阶段不重写磁盘格式，仅做接口适配。
//
// 设计原则（v1-04 第十部分 A4）：
//   - 包住现有 FileProjectStore/FileArtifactStore；
//   - 全部 Port 可用 fake 替换；
//   - 当前实现测试仍通过。

import (
	"context"
	"fmt"
	"io"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// RepositoryAdapter 包住 modeling.ProjectStore + ArtifactStore。
type RepositoryAdapter struct {
	projects  modeling.ProjectStore
	artifacts modeling.ArtifactStore
	scope     pipelineapi.Scope
}

// NewRepositoryAdapter 构造 Repository Port Adapter。
// scope 是 Engine 调用时的授权 scope（由组合根/Adapter 注入）。
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

// Scope 实现 scopeProvider，让 current Engine 从 RuntimePorts.Repository 取 Scope。
func (a *RepositoryAdapter) Scope() pipelineapi.Scope { return a.scope }

// CreateProject 创建一个新项目记录。
func (a *RepositoryAdapter) CreateProject(ctx context.Context, rec pipelineapi.ProjectRecord) (pipelineapi.ProjectRecord, error) {
	p := modeling.Project{
		Title:       rec.Title,
		WorkspaceID: string(a.scope),
		Current:     modeling.StagePlan,
		Status:      modeling.StatusPending,
	}
	created, err := a.projects.Create(ctx, p)
	if err != nil {
		return pipelineapi.ProjectRecord{}, fmt.Errorf("repository create: %w", err)
	}
	return projectToRecord(created), nil
}

// GetProject 按 ID + scope 读取项目记录。
func (a *RepositoryAdapter) GetProject(ctx context.Context, id pipelineapi.ProjectID, scope pipelineapi.Scope) (pipelineapi.ProjectRecord, error) {
	p, err := a.projects.Get(ctx, string(id), toModelingScope(scope))
	if err != nil {
		return pipelineapi.ProjectRecord{}, fmt.Errorf("repository get: %w", err)
	}
	return projectToRecord(p), nil
}

// ListProjects 列出符合 scope 的项目记录。
func (a *RepositoryAdapter) ListProjects(ctx context.Context, q pipelineapi.ProjectQuery) ([]pipelineapi.ProjectRecord, error) {
	mq := modeling.Query{
		WorkspaceID: string(q.Scope),
		Limit:       q.Limit,
	}
	projects, err := a.projects.List(ctx, mq)
	if err != nil {
		return nil, fmt.Errorf("repository list: %w", err)
	}
	out := make([]pipelineapi.ProjectRecord, len(projects))
	for i, p := range projects {
		out[i] = projectToRecord(p)
	}
	return out, nil
}

// CompareAndSaveProject 实现 CAS（revision 乐观锁）。
// modeling.ProjectStore.Save 不带 CAS；这里通过传入的 expectedRevision
// 与 Save 前的 Get 比对实现。注意：这是一个 best-effort CAS，
// 真正的原子 CAS 需要在 Store 层加 revision 检查（后续 B 系列重构）。
func (a *RepositoryAdapter) CompareAndSaveProject(ctx context.Context, rec pipelineapi.ProjectRecord, expectedRevision int) (pipelineapi.ProjectRecord, error) {
	if rec.Revision != expectedRevision {
		return pipelineapi.ProjectRecord{}, fmt.Errorf("repository CAS: revision mismatch (got %d, expected %d)",
			rec.Revision, expectedRevision)
	}
	p := recordToProject(rec)
	p.Revision = expectedRevision + 1
	saved, err := a.projects.Save(ctx, p)
	if err != nil {
		return pipelineapi.ProjectRecord{}, fmt.Errorf("repository save: %w", err)
	}
	return projectToRecord(saved), nil
}

// StageArtifacts 暂存一批 artifact 草稿，返回一个 Batch 句柄。
func (a *RepositoryAdapter) StageArtifacts(ctx context.Context, projectID pipelineapi.ProjectID, drafts []pipelineapi.ArtifactDraft) (pipelineapi.Batch, error) {
	modelingDrafts := make([]modeling.Draft, len(drafts))
	for i, d := range drafts {
		modelingDrafts[i] = modeling.Draft{
			Name: d.Name,
			Kind: modeling.Kind(d.Kind),
			Body: d.Bytes,
		}
	}
	batch, err := a.artifacts.Stage(ctx, string(projectID), modelingDrafts)
	if err != nil {
		return nil, fmt.Errorf("repository stage: %w", err)
	}
	return &batchWrapper{batch: batch}, nil
}

// CommitArtifacts 提交暂存的 artifact 为正式 descriptor。
func (a *RepositoryAdapter) CommitArtifacts(ctx context.Context, b pipelineapi.Batch) ([]pipelineapi.ArtifactDescriptor, error) {
	bw, ok := b.(*batchWrapper)
	if !ok {
		return nil, fmt.Errorf("repository commit: invalid batch type %T", b)
	}
	refs, err := a.artifacts.Commit(ctx, bw.batch)
	if err != nil {
		return nil, fmt.Errorf("repository commit: %w", err)
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

// AbortArtifacts 丢弃暂存的 artifact 草稿。
func (a *RepositoryAdapter) AbortArtifacts(ctx context.Context, b pipelineapi.Batch) error {
	bw, ok := b.(*batchWrapper)
	if !ok {
		return fmt.Errorf("repository abort: invalid batch type %T", b)
	}
	return a.artifacts.Discard(ctx, bw.batch)
}

// OpenArtifact 打开并读取一个已提交 artifact 的内容。
func (a *RepositoryAdapter) OpenArtifact(ctx context.Context, projectID pipelineapi.ProjectID, artifactID pipelineapi.ArtifactID) (io.ReadCloser, error) {
	// 需要先 Get 项目，找到对应的 ArtifactRef（验证引用关系）。
	p, err := a.projects.Get(ctx, string(projectID), toModelingScope(a.scope))
	if err != nil {
		return nil, fmt.Errorf("repository open: get project: %w", err)
	}
	ref, ok := findArtifactRef(p, string(artifactID))
	if !ok {
		return nil, fmt.Errorf("repository open: artifact %s not referenced by project %s",
			artifactID, projectID)
	}
	return a.artifacts.Open(ctx, string(projectID), ref)
}

// Ensure RepositoryAdapter satisfies pipelineapi.Repository at compile time.
var _ pipelineapi.Repository = (*RepositoryAdapter)(nil)

// batchWrapper 把 modeling.Batch 包装为 pipelineapi.Batch。
type batchWrapper struct {
	batch modeling.Batch
}

// IsOpaque 满足 pipelineapi.Batch 的占位契约。
func (b *batchWrapper) IsOpaque() {}

// findArtifactRef 在 Project 的 Artifacts + Evidence 中查找指定 ID 的 ArtifactRef。
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

// projectToRecord 把 modeling.Project 映射为 pipelineapi.ProjectRecord。
func projectToRecord(p modeling.Project) pipelineapi.ProjectRecord {
	return pipelineapi.ProjectRecord{
		ID:        pipelineapi.ProjectID(p.ID),
		Scope:     pipelineapi.Scope(p.WorkspaceID),
		Title:     p.Title,
		Current:   pipelineapi.OperationName(string(p.Current)),
		Status:    engineStatusFromModeling(p.Status),
		Revision:  p.Revision,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// recordToProject 把 pipelineapi.ProjectRecord 映射回 modeling.Project。
func recordToProject(rec pipelineapi.ProjectRecord) modeling.Project {
	return modeling.Project{
		ID:          string(rec.ID),
		Title:       rec.Title,
		WorkspaceID: string(rec.Scope),
		Current:     modeling.Stage(string(rec.Current)),
		Status:      modeling.Status(string(rec.Status)),
		Revision:    rec.Revision,
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
	}
}

// engineStatusFromModeling 把 modeling.Status 映射为 pipelineapi.ProjectStatus。
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

// toModelingScope 把 pipelineapi.Scope 转为 modeling.Scope。
func toModelingScope(s pipelineapi.Scope) modeling.Scope {
	return modeling.Scope{WorkspaceID: string(s)}
}
