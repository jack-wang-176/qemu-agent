package pipelineapi

// error.go — pipelineapi 包内错误类型。
//
// 设计原则（v1-04 第六部分）：
//   - pipelineapi 不依赖 modelingapi，因此有独立的错误类型。
//   - Engine 内部错误用 EngineError 表达（见 event.go）。
//   - Port 缺失、Port 操作失败等运行时错误用 ErrMissingPort/PortError 表达。

// ErrMissingPort 表示 RuntimePorts 中某个 Port 未注入。
type ErrMissingPort PortName

// Error 实现 error 接口。
func (e ErrMissingPort) Error() string {
	return "pipelineapi: missing runtime port: " + string(e)
}

// PortError 表示某个 Port 操作失败。
//
// Port 字段标识哪个 Port 出错；
// Cause 是底层错误（可以是 nil）；
// Message 是人类可读的错误描述。
type PortError struct {
	Port    PortName
	Cause   error
	Message string
}

// Error 实现 error 接口，返回 Port + Message。
func (e *PortError) Error() string {
	if e == nil {
		return ""
	}
	return "pipelineapi: port " + string(e.Port) + " error: " + e.Message
}

// Unwrap 支持 errors.Is/As 链。
func (e *PortError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewPortError 构造一个 PortError。
func NewPortError(port PortName, message string, cause error) *PortError {
	return &PortError{
		Port:    port,
		Cause:   cause,
		Message: message,
	}
}
