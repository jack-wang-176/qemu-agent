package pipelineapi

// runtime.go — Runtime Ports 集合与公共契约。
//
// 设计原则（v1-04 第六部分）：
//   - Engine 不直接 import 现有 Store/Completer/Executor/runstream。
//   - 通过 Ports 注入可信运行时基础设施，实现可替换与可测试。
//   - Ports 由 modelingapp/Adapter 在调用 Engine 前注入。
//
// 与 modelingapi 的关系：
//   modelingapi 面向入口，稳定且产品化。
//   pipelineapi 面向 Engine，实现可替换。
//   modelingapi 不 import pipelineapi；modelingapp 桥接二者。
//
// 注意：本文件只定义 RuntimePorts 聚合与 Port 接口的公共辅助。
// 各 Port 的具体接口定义在 repository.go/completion.go/effect.go/event.go。

// RuntimePorts 是 Engine 一次 Execute 调用所需的全部运行时基础设施。
//
// Engine 不自己构造 RuntimePorts——由 modelingapp/Adapter 注入。
// 这样 Engine 实现可以保持"纯函数式"：相同 Project+Operation+Ports → 相同结果。
type RuntimePorts struct {
	Repository  Repository
	Completion  Completion
	Effect      Effect
	Event       EventPublisher
}

// PortName 列出所有 Port 的标识，便于错误信息与测试。
type PortName string

const (
	PortRepository PortName = "repository"
	PortCompletion PortName = "completion"
	PortEffect     PortName = "effect"
	PortEvent      PortName = "event"
)

// ValidatePorts 报告 RuntimePorts 是否齐全。
//
// Engine 实现在 Execute 入口应调用此函数，避免某个 Adapter 漏注入 Port
// 导致 Engine 内部 panic。
func ValidatePorts(p RuntimePorts) error {
	if p.Repository == nil {
		return ErrMissingPort(PortRepository)
	}
	if p.Completion == nil {
		return ErrMissingPort(PortCompletion)
	}
	if p.Effect == nil {
		return ErrMissingPort(PortEffect)
	}
	if p.Event == nil {
		return ErrMissingPort(PortEvent)
	}
	return nil
}
