package runtime

import (
	"context"
	"errors"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// Factory owns stable infrastructure dependencies and derives request-scoped
// runtime ports from trusted scope, invocation, and event context.
type Factory struct {
	projects  modeling.ProjectStore
	artifacts modeling.ArtifactStore
	completer modeling.Completer
	tools     modeling.ToolRunner
}

type FactoryDependencies struct {
	Projects  modeling.ProjectStore
	Artifacts modeling.ArtifactStore
	Completer modeling.Completer
	Tools     modeling.ToolRunner
}

func NewFactory(deps FactoryDependencies) (*Factory, error) {
	switch {
	case deps.Projects == nil:
		return nil, errors.New("runtime factory: project store is nil")
	case deps.Artifacts == nil:
		return nil, errors.New("runtime factory: artifact store is nil")
	case deps.Completer == nil:
		return nil, errors.New("runtime factory: completer is nil")
	case deps.Tools == nil:
		return nil, errors.New("runtime factory: tool runner is nil")
	}
	return &Factory{
		projects: deps.Projects, artifacts: deps.Artifacts,
		completer: deps.Completer, tools: deps.Tools,
	}, nil
}

func (f *Factory) Build(
	ctx context.Context,
	scope pipelineapi.Scope,
	invocation pipelineapi.InvocationContext,
) (pipelineapi.RuntimePorts, error) {
	if f == nil {
		return pipelineapi.RuntimePorts{}, errors.New("runtime factory is nil")
	}
	if err := scope.Validate(); err != nil {
		return pipelineapi.RuntimePorts{}, err
	}
	if err := invocation.Validate(); err != nil {
		return pipelineapi.RuntimePorts{}, err
	}
	return BuildRuntimePorts(PortDependencies{
		Projects: f.projects, Artifacts: f.artifacts,
		Completer: f.completer, Tools: f.tools,
		Event: pipelineapi.EventPublisherFromContext(ctx), Scope: scope,
	})
}
