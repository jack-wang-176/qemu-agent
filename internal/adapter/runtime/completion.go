package runtime

// completion.go — Completion Port Adapter (A4).
//
// 把现有 modeling.Completer（或 llm 层的 single-shot 调用）包装为
// pipelineapi.Completion。第一阶段 Stage prompt 和输出 schema 保持不变；
// 后续 Pipeline 重构才能引入 Profile、Memory/Skill projection 或新的
// 结构化输出策略。

import (
	"context"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// CompletionAdapter 包住 modeling.Completer（single-shot Complete(system, user)）。
type CompletionAdapter struct {
	completer modeling.Completer
}

// NewCompletionAdapter 构造 Completion Port Adapter。
func NewCompletionAdapter(completer modeling.Completer) *CompletionAdapter {
	return &CompletionAdapter{completer: completer}
}

// Complete 实现 pipelineapi.Completion。
//
// pipelineapi.CompletionRequest 携带 Prompt + Schema；
// 现有 modeling.Completer 只接受 (system, user)，因此 Adapter：
//   1. 把 Schema 作为 user prompt 的追加指令（schema 引导）；
//   2. 把 Prompt 作为 user prompt（current Pipeline 通常把 system 留空）；
//   3. TokensUsed / FinishReason 在第一阶段无来源，留默认值。
func (a *CompletionAdapter) Complete(ctx context.Context, req pipelineapi.CompletionRequest) (pipelineapi.CompletionResult, error) {
	user := req.Prompt
	if req.Schema != "" {
		// 当前 Pipeline 的 Completer 没有显式 schema 参数；
		// Adapter 把 schema 作为对模型的结构化输出指令追加。
		user = fmt.Sprintf("%s\n\nRespond strictly according to this JSON schema:\n%s", user, req.Schema)
	}
	content, err := a.completer.Complete(ctx, "", user)
	if err != nil {
		return pipelineapi.CompletionResult{}, fmt.Errorf("completion adapter: %w", err)
	}
	return pipelineapi.CompletionResult{
		Content:     content,
		TokensUsed:  0, // 现有 Completer 不返回 token 计数
		FinishReason: "stop",
	}, nil
}

// Ensure CompletionAdapter satisfies pipelineapi.Completion at compile time.
var _ pipelineapi.Completion = (*CompletionAdapter)(nil)
