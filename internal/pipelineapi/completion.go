package pipelineapi

// completion.go — Completion Port 契约。
//
// 设计原则（v1-04 第六部分）：
//   - Engine 通过 Completion Port 调用模型，不直接 import Provider Registry/Completer。
//   - 第一阶段 Stage prompt 和输出 schema 保持不变；后续 Pipeline 重构才能引入
//     Profile、Memory/Skill projection 或新的结构化输出策略。
//   - CompletionRequest 携带 Engine 构造的 prompt 与 schema；
//     CompletionResult 返回模型原始输出（按 schema 校验由 Stage 完成）。

import "context"

// Completion 是模型调用 Port。
type Completion interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error)
}

// CompletionRequest 是 Engine 调用模型时的入参。
type CompletionRequest struct {
	// Prompt 是 Engine 构造的提示词（system + user + context）。
	Prompt string

	// Schema 是期望的结构化输出 schema（JSON Schema 字符串）。
	// nil/空表示自由文本输出。
	Schema string

	// Sources 是 Engine 已注入 prompt 的来源引用（仅用于审计/事件）。
	Sources []SourceRef

	// MaxTokens 是输出 token 上限。
	MaxTokens int

	// Temperature 是采样温度，0 表示确定性。
	Temperature float64
}

// CompletionResult 是 Completion 的返回值。
type CompletionResult struct {
	// Content 是模型原始输出文本（按 schema 生成的内容）。
	Content string

	// TokensUsed 是本次调用消耗的 token 数（prompt + completion）。
	TokensUsed int

	// FinishReason 是模型停止的原因（"stop"、"length"、"content_filter" 等）。
	FinishReason string
}
