package pipelineapi

// engine.go — 可替换流水线 Engine 契约。
//
// 设计原则（v1-04 第五、六部分）：
//   - Engine 面向 modelingapp，实现可替换。current Pipeline 与未来 Pipeline 都可接入。
//   - 只表达 Pipeline operation，不表达 CLI command、MCP request 或 Agent tool call。
//   - 不依赖 app/channel/session/runstream/modeling。
//   - Execute 只发布 operation 领域事件，不发布 request-level run_started（那是入口外壳协议）。
//   - 组合根是唯一创建 current Engine 实现的位置。
//
// 与 modelingapi 的关系：
//   modelingapi 面向入口（CLI/Agent/MCP），稳定且产品化。
//   pipelineapi 面向 Engine，实现可替换。
//   modelingapi 不 import pipelineapi；modelingapp 桥接二者。

import (
	"context"
	"time"
)

// Scope 是 Repository 的授权 scope（opaque workspace ID）。
type Scope string

// ProjectID 是 opaque 项目标识。
type ProjectID string

// ArtifactID 是 opaque artifact 标识。
type ArtifactID string

// OperationName 是可发现的 operation 名。
type OperationName string

// Engine 是可替换流水线契约。
//
// modelingapp 通过 Engine 完成具体 operation；current Adapter（A3）将 modeling.Pipeline
// 包装为 Engine，未来 Pipeline（B 系列）实现同一接口。
type Engine interface {
	// Describe 返回当前 Engine 的能力描述，不依赖具体 Project。
	Describe(ctx context.Context, req DescribeRequest) (Description, error)

	// Inspect 读取一个已存在 Project 的当前快照，不执行 operation。
	Inspect(ctx context.Context, req InspectRequest) (EngineView, error)

	// Execute 在给定 Project 上执行一次 operation。
	// 返回 ExecuteResult，包含产生的 Artifact、状态变化与摘要。
	// operation 领域事件通过 ExecuteRequest.Ports.Event 发布。
	Execute(ctx context.Context, req ExecuteRequest) (ExecuteResult, error)
}

// DescribeRequest 是 Describe 的入参。
type DescribeRequest struct {
	Scope Scope
}

// Description 是 Describe 的返回值。
type Description struct {
	EngineName    string
	EngineVersion string
	APIVersion    string // 例如 "v1"，与 modelingapi.Capabilities.APIVersion 对齐
	Operations    []OperationDescriptor
	ArtifactKinds []string
	// 支持特性（与 modelingapi.Capabilities 对齐，但由 Engine 决定）
	SupportsApply    bool
	SupportsEvidence bool
	SupportsCancel   bool
	SupportsProgress bool
}

// OperationDescriptor 描述一个可发现的 operation。
type OperationDescriptor struct {
	Name            OperationName
	DisplayName     string
	Description     string
	RequiresSources bool
	Mutating        bool
	MayBlock        bool
}

// InspectRequest 是 Inspect 的入参。
type InspectRequest struct {
	Scope     Scope
	ProjectID ProjectID
	Revision  int // <=0 表示读最新
}

// EngineView 是 Inspect 的返回值，Engine 视角的项目快照。
//
// 与 modelingapi.ProjectView 的区别：
//   EngineView 面向 Engine/modelingapp，可包含 Engine 内部状态（Stage、运行中临时字段）。
//   modelingapp 负责把 EngineView 映射为对外稳定的 modelingapi.ProjectView。
type EngineView struct {
	ProjectID        ProjectID
	Title            string
	Revision         int
	Status           ProjectStatus
	CurrentOperation OperationName
	Recommended      []OperationDescriptor
	Artifacts        []ArtifactDescriptor
	EvidenceCount    int
	LastError        string // Engine 内部错误类别，不是公开 PublicError
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ExecuteRequest 是 Execute 的入参。
//
// Pipeline 只声明逻辑 operation，不构造 CLI/MCP 调用。
// Ports 由 modelingapp/Adapter 注入可信运行时基础设施。
type ExecuteRequest struct {
	Project          ProjectSnapshot
	Operation        OperationName
	Instruction      string
	Sources          []SourceRef
	ExpectedRevision int
	Ports            RuntimePorts
}

// ProjectSnapshot 是 Execute 的入参项目快照（Engine 从此快照开始执行）。
type ProjectSnapshot struct {
	ID       ProjectID
	Scope    Scope
	Revision int
}

// SourceRef 是外部传入的不可信来源引用。
type SourceRef struct {
	Kind   string
	Value  string
	Digest string
}

// ExecuteResult 是 Execute 的返回值。
type ExecuteResult struct {
	Project   EngineView
	Operation OperationName
	Status    OperationStatus
	Artifacts []ArtifactDescriptor
	Evidence  []ArtifactDescriptor
	Summary   string
	Blocked   bool
	Reason    string
}

// ProjectStatus 是 Engine 视角的内部状态（保留 modeling 内部语义）。
type ProjectStatus string

const (
	StatusPending ProjectStatus = "pending"
	StatusRunning ProjectStatus = "running"
	StatusBlocked ProjectStatus = "blocked"
	StatusDone    ProjectStatus = "done" // Engine 内部用 done，modelingapp 映射为 completed
)

// OperationStatus 是一次 operation 调用的结果状态。
type OperationStatus string

const (
	OpSucceeded OperationStatus = "succeeded"
	OpBlocked   OperationStatus = "blocked"
	OpFailed    OperationStatus = "failed"
)

// ArtifactDescriptor 是 Engine 视角的 artifact 描述。
type ArtifactDescriptor struct {
	ID        ArtifactID
	Operation OperationName
	Name      string
	Kind      string
	Bytes     int64
	Digest    string
	CreatedAt time.Time
}
