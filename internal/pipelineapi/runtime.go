package pipelineapi

// runtime.go - Runtime port aggregation and shared contracts.
//
// Runtime ports keep engines independent of concrete stores, model providers,
// effect executors, and event transports. modelingapp injects them per request.

// RuntimePorts contains all infrastructure required by one Execute invocation.
type RuntimePorts struct {
	Repository Repository
	Completion Completion
	Effect     Effect
	Event      EventPublisher
}

// PortName identifies a runtime port for errors and tests.
type PortName string

const (
	PortRepository PortName = "repository"
	PortCompletion PortName = "completion"
	PortEffect     PortName = "effect"
	PortEvent      PortName = "event"
)

// ValidatePorts reports whether all required runtime ports are present.
//
// Engines should call ValidatePorts before doing work so missing dependencies fail
// before any project or artifact state changes.
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
