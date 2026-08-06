package pipelineapi

// event.go — Event Port 契约。
//
// 设计原则（v1-04 第六、九部分）：
//   - Pipeline 只发布 operation 领域事件。
//   - Event 失败默认不改变 Pipeline 事务结果；关键 approval 不使用 Event 模拟。
//   - 不同 Adapter 转换事件为不同 transport：
//       CLI/Agent → runstream.Event
//       MCP       → progress/logging notification
//       test      → in-memory recorder
//   - Engine 只发布 operation_started/progress/completed，不发布 request-level run_started。

import "context"

// EventPublisher 是 Engine 发布 operation 领域事件的 Port。
//
// 命名为 EventPublisher 而非 Event，避免与下面的 Event 结构体重名。
type EventPublisher interface {
	Publish(ctx context.Context, evt Event) error
}

// EventKind 描述一次 operation 调用的稳定事件类别。
type EventKind string

const (
	EventOperationStarted   EventKind = "operation_started"
	EventOperationProgress  EventKind = "operation_progress"
	EventOperationCompleted EventKind = "operation_completed"
)

// Progress 描述 operation 进度。
type Progress struct {
	Text    string // 有界、无控制字符
	Current int
	Total   int
}

// ResultSummary 是 completed 事件的稳定结果摘要。
type ResultSummary struct {
	Status    OperationStatus
	Summary   string // 公开长度上限
	Artifacts []ArtifactDescriptor
	Evidence  []ArtifactDescriptor
}

// Event 是一次 operation 领域事件。
//
// 与 modelingapi.Event 的区别：
//   pipelineapi.Event 是 Engine 发布的领域事件，含 Engine 内部状态。
//   modelingapi.Event 是面向入口的稳定事件，由 modelingapp 从 pipelineapi.Event 映射而来。
type Event struct {
	Kind      EventKind
	ProjectID ProjectID
	Operation OperationName
	Progress  *Progress      // 仅 progress 事件
	Result    *ResultSummary // 仅 completed 成功事件
	Error     *EngineError   // 仅 completed 失败事件
}

// EngineError 是 Engine 内部错误，不是公开 PublicError。
//
// modelingapp 负责把 EngineError 映射为 modelingapi.PublicError。
type EngineError struct {
	Category string // Engine 内部错误类别
	Message  string // 内部错误描述（不对外公开）
}
