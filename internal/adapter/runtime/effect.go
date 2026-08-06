package runtime

// effect.go — Effect Port Adapter (A4).
//
// 把现有 modeling.ToolRunner（基于 security.Executor）包装为
// pipelineapi.Effect。Engine 只声明逻辑 effect 名和参数；
// 可信 identity 从 CallContext 经 Application/Adapter 注入。
//
// 设计原则（v1-04 第六部分）：
//   - 第一阶段 Adapter 复用 security.Executor；
//   - Engine 不构造 security.Caller，由 Adapter 映射。

import (
	"context"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// EffectAdapter 包住 modeling.ToolRunner。
type EffectAdapter struct {
	tools modeling.ToolRunner
}

// NewEffectAdapter 构造 Effect Port Adapter。
func NewEffectAdapter(tools modeling.ToolRunner) *EffectAdapter {
	return &EffectAdapter{tools: tools}
}

// Invoke 实现 pipelineapi.Effect。
//
// pipelineapi.EffectRequest 携带 Name + Args + Caller；
// 现有 modeling.ToolRunner.Run 接受 (name string, args map[string]any)，
// 因此 Adapter：
//   1. 把 EffectRequest.Args 当作 JSON map 传给 ToolRunner；
//   2. Caller 在第一阶段不透传给 ToolRunner（current Pipeline 不用 caller identity）；
//   3. ToolRunner 返回的 tools.ExecutionResult 映射为 EffectResult。
//
// 注意：EffectRequest.Args 是 []byte（JSON）。
// 这里先把它解析为 map[string]any 再传给 ToolRunner。
func (a *EffectAdapter) Invoke(ctx context.Context, req pipelineapi.EffectRequest) (pipelineapi.EffectResult, error) {
	// Args 是 []byte JSON；parse 为 map[string]any 供 ToolRunner 使用。
	args, err := parseArgsToMap(req.Args)
	if err != nil {
		return pipelineapi.EffectResult{
			Status: pipelineapi.EffectFailed,
			Err:    fmt.Sprintf("effect adapter: parse args: %v", err),
		}, nil
	}

	result, err := a.tools.Run(ctx, req.Name, args)
	if err != nil {
		return pipelineapi.EffectResult{
			Status: pipelineapi.EffectFailed,
			Err:    fmt.Sprintf("effect adapter: tool run: %v", err),
		}, nil
	}

	// tools.ExecutionResult 只含 ModelOutput/PersistentOutput，无 OK/Stdout/Stderr。
	// 失败信号通过 err 上面已处理；这里视为 succeeded。
	return pipelineapi.EffectResult{
		Status: pipelineapi.EffectSucceeded,
		Output: []byte(result.ModelOutput),
	}, nil
}

// Ensure EffectAdapter satisfies pipelineapi.Effect at compile time.
var _ pipelineapi.Effect = (*EffectAdapter)(nil)
