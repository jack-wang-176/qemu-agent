package modelingapp

import (
	"context"
	"errors"

	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type RuntimeFactory interface {
	Build(
		ctx context.Context,
		scope pipelineapi.Scope,
		invocation pipelineapi.InvocationContext,
	) (pipelineapi.RuntimePorts, error)
}

// EventRuntimeFactory replaces the event port with the publisher attached to
// the current request. It prevents a publisher from leaking across sessions.
type EventRuntimeFactory struct {
	Base RuntimeFactory
}

func (f EventRuntimeFactory) Build(
	ctx context.Context,
	scope pipelineapi.Scope,
	invocation pipelineapi.InvocationContext,
) (pipelineapi.RuntimePorts, error) {
	if f.Base == nil {
		return pipelineapi.RuntimePorts{}, errors.New("modelingapp: base runtime factory is nil")
	}
	ports, err := f.Base.Build(ctx, scope, invocation)
	if err != nil {
		return pipelineapi.RuntimePorts{}, err
	}
	ports.Event = pipelineapi.EventPublisherFromContext(ctx)
	if err := pipelineapi.ValidatePorts(ports); err != nil {
		return pipelineapi.RuntimePorts{}, err
	}
	return ports, nil
}

var _ RuntimeFactory = EventRuntimeFactory{}
