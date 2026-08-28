package runtime

import (
	"errors"
	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type PortDependencies struct {
	Projects  modeling.ProjectStore
	Artifacts modeling.ArtifactStore
	Completer modeling.Completer
	Tools     modeling.ToolRunner
	Event     pipelineapi.EventPublisher
	Scope     pipelineapi.Scope
}

func BuildRuntimePorts(dependencies PortDependencies) (pipelineapi.RuntimePorts, error) {
	if dependencies.Projects == nil {
		return pipelineapi.RuntimePorts{}, errors.New("runtime dependencies: project store is nil")
	}
	if dependencies.Artifacts == nil {
		return pipelineapi.RuntimePorts{}, errors.New("runtime dependencies: artifact store is nil")
	}
	if dependencies.Completer == nil {
		return pipelineapi.RuntimePorts{}, errors.New("runtime dependencies: completer is nil")
	}
	if dependencies.Tools == nil {
		return pipelineapi.RuntimePorts{}, errors.New("runtime dependencies: tool runner is nil")
	}
	if dependencies.Event == nil {
		return pipelineapi.RuntimePorts{}, errors.New("runtime dependencies: event publisher is nil")
	}
	ports := pipelineapi.RuntimePorts{Repository: NewRepositoryAdapter(dependencies.Projects, dependencies.Artifacts, dependencies.Scope), Completion: NewCompletionAdapter(dependencies.Completer), Effect: NewEffectAdapter(dependencies.Tools), Event: dependencies.Event}
	if err := pipelineapi.ValidatePorts(ports); err != nil {
		return pipelineapi.RuntimePorts{}, err
	}
	return ports, nil
}
