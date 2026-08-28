package build

import (
	"errors"
	"github.com/jack-wang-176/qemu-agent/internal/adapter/pipeline/current"
	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/modelingapp"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type ModelingServiceInput struct {
	Legacy  ModelingComponents
	Runtime modelingapp.RuntimeFactory
}

type ModelingServiceComponents struct {
	Service modelingapi.Service
	Engine  pipelineapi.Engine
	Query   pipelineapi.QueryPort
}

func BuildModelingService(in ModelingServiceInput) (ModelingServiceComponents, error) {
	if in.Runtime == nil {
		return ModelingServiceComponents{}, errors.New("build modeling service: runtime factory is nil")
	}

	// TODO: reject disabled/incomplete professional-agent configuration.
	// TODO: construct current.Engine from Legacy.Runner.
	// TODO: construct current.QueryAdapter from Legacy.Runner.
	// TODO: construct modelingapp.Service from Runtime, Query and Engine.
	// TODO: return only interfaces needed by later layers.
	deps := current.Dependencies{
		Engine: in.Legacy.Runner,
		Query:  in.Legacy.Runner,
	}
	engine, err := current.NewEngine(deps)
	if err != nil {
		return ModelingServiceComponents{}, err
	}
	adapter, err := current.NewQueryAdapter(in.Legacy.Runner)
	if err != nil {
		return ModelingServiceComponents{}, err
	}
	service, err := modelingapp.NewService(
		modelingapp.Dependencies{
			RuntimePorts: modelingapp.EventRuntimeFactory{Base: in.Runtime},
			Engine:       engine,
			QueryPort:    adapter,
		},
	)
	if err != nil {
		return ModelingServiceComponents{}, err
	}
	return ModelingServiceComponents{
		Service: service,
		Engine:  engine,
		Query:   adapter,
	}, nil
}
