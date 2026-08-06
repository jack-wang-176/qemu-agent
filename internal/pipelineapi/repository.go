package pipelineapi

// repository.go — Repository Port 契约。
//
// 设计原则（v1-04 第六部分）：
//   - Engine 通过 Repository Port 读写 Project/Artifact，不直接 import 现有 Store。
//   - Repository 只表达领域持久化语义，不暴露磁盘路径或 manifest 路径。
//   - 第一阶段 Adapter（A4）包住现有 FileProjectStore/FileArtifactStore，不重写磁盘格式。
//   - CompareAndSave 实现 CAS（revision 乐观锁）。

import (
	"context"
	"io"
	"time"
)

// Repository 是 Engine 视角的持久化 Port。
//
// 它包含 Project 与 Artifact 两类资源：
//   - Project 资源通过 ProjectRecord 表达，含状态机与 CAS。
//   - Artifact 资源通过 ArtifactDraft/ArtifactDescriptor 表达，含分批写入与读取。
type Repository interface {
	// Project 资源操作
	CreateProject(ctx context.Context, rec ProjectRecord) (ProjectRecord, error)
	GetProject(ctx context.Context, id ProjectID, scope Scope) (ProjectRecord, error)
	ListProjects(ctx context.Context, q ProjectQuery) ([]ProjectRecord, error)
	CompareAndSaveProject(ctx context.Context, rec ProjectRecord, expectedRevision int) (ProjectRecord, error)

	// Artifact 资源操作
	StageArtifacts(ctx context.Context, projectID ProjectID, drafts []ArtifactDraft) (Batch, error)
	CommitArtifacts(ctx context.Context, b Batch) ([]ArtifactDescriptor, error)
	AbortArtifacts(ctx context.Context, b Batch) error
	OpenArtifact(ctx context.Context, projectID ProjectID, artifactID ArtifactID) (io.ReadCloser, error)
}

// ProjectRecord 是 Repository 视角的项目记录。
//
// 它是 Engine/Repository 之间的领域记录，不是 modelingapi.ProjectView。
// Engine 内部状态（Stage enum、运行中临时字段）可以保留。
type ProjectRecord struct {
	ID          ProjectID
	Scope       Scope
	Title       string
	Current     OperationName // 当前 operation（Engine 内部语义）
	Status      ProjectStatus
	Revision    int
	Artifacts   []ArtifactDescriptor // 已提交的 artifact
	Evidence    []ArtifactDescriptor
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ProjectQuery 是 ListProjects 的查询参数。
type ProjectQuery struct {
	Scope Scope
	Limit int
	// Cursor 当前 Store 不支持；current Adapter 可先忽略。
}

// ArtifactDraft 是 Engine 产生、待提交的 artifact 草稿。
//
// Engine 在 Stage/Commit 流程中：
//   1. 调用 StageArtifacts(drafts) 获得一个 Batch（临时暂存）。
//   2. 调用 CommitArtifacts(batch) 提交为正式 ArtifactDescriptor。
//   3. 出错时调用 AbortArtifacts(batch) 丢弃。
type ArtifactDraft struct {
	Operation OperationName
	Name      string
	Kind      string
	Bytes     []byte // 完整内容（Stage 阶段会落盘）
}

// Batch 是 StageArtifacts 返回的暂存句柄，对 Engine opaque。
//
// Engine 只需把 Batch 传给 CommitArtifacts 或 AbortArtifacts。
// Batch 的内部结构由 Adapter/Repository 实现决定（可以是 manifest 路径、内存句柄等）。
type Batch interface {
	// IsOpaque 仅是占位方法，证明 Batch 对 Engine 是 opaque 句柄。
	// 实际方法签名由 Adapter 决定，但不应暴露内部路径。
	IsOpaque()
}
