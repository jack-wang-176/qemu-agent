package pipelineapi

// effect.go — Effect Port 契约。
//
// 设计原则（v1-04 第六部分）：
//   - Engine 只声明逻辑 effect 名称和参数，不构造 security.Caller。
//   - 可信 identity 从 CallContext 经 Application/Adapter 注入。
//   - 第一阶段 Adapter（A4）复用 security.Executor。
//   - Effect 失败不直接 panic；Engine 通过 EffectResult.Status 决定是否进入 blocked。

import "context"

// Effect 是 Engine 调用受控副作用的 Port。
type Effect interface {
	Invoke(ctx context.Context, req EffectRequest) (EffectResult, error)
}

// EffectRequest 是 Engine 声明的逻辑 effect。
type EffectRequest struct {
	// Name 是逻辑 effect 名称（如 "render_c"、"write_manifest"）。
	// 它不是工具路径或 security.Caller——可信 identity 由 Adapter 注入。
	Name string

	// Args 是逻辑 effect 参数（JSON 或结构化）。
	Args []byte

	// Caller 是 Engine 传入的可信 caller 标识。
	// Adapter 在映射到 security.Executor 时会注入真正的 caller identity。
	Caller Caller
}

// Caller 是 Engine 视角的可信调用者标识。
//
// 它不是 security.Caller 本身——pipelineapi 不依赖 tools/security。
// Adapter 负责 Caller → security.Caller 的映射。
type Caller struct {
	WorkspaceID string
	UserID      string
	TraceID     string
}

// EffectResult 是 Effect 的返回值。
type EffectResult struct {
	// Status 是 effect 执行结果状态。
	Status EffectStatus

	// Output 是 effect 产生的结构化输出（JSON）。
	Output []byte

	// Err 是 effect 执行失败时的内部错误描述。
	// 注意：这是 Engine 内部错误，不是公开 PublicError。
	Err string
}

// EffectStatus 描述 effect 执行结果。
type EffectStatus string

const (
	// EffectSucceeded 表示 effect 成功完成。
	EffectSucceeded EffectStatus = "succeeded"
	// EffectBlocked 表示 effect 需要等待外部条件（如审批）。
	EffectBlocked EffectStatus = "blocked"
	// EffectFailed 表示 effect 执行失败。
	EffectFailed EffectStatus = "failed"
)
