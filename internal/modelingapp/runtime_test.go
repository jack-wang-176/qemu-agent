package modelingapp

import (
	"context"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type runtimeFactoryFunc func(context.Context, pipelineapi.Scope, pipelineapi.InvocationContext) (pipelineapi.RuntimePorts, error)

func (f runtimeFactoryFunc) Build(ctx context.Context, scope pipelineapi.Scope, invocation pipelineapi.InvocationContext) (pipelineapi.RuntimePorts, error) {
	return f(ctx, scope, invocation)
}

type runtimeRepository struct{ pipelineapi.Repository }
type runtimeCompletion struct{ pipelineapi.Completion }
type runtimeEffect struct{ pipelineapi.Effect }

type recordingPublisher struct{ calls int }

func (p *recordingPublisher) Publish(context.Context, pipelineapi.Event) error {
	p.calls++
	return nil
}

func TestEventRuntimeFactoryUsesRequestPublisher(t *testing.T) {
	basePublisher := &recordingPublisher{}
	base := runtimeFactoryFunc(func(context.Context, pipelineapi.Scope, pipelineapi.InvocationContext) (pipelineapi.RuntimePorts, error) {
		return pipelineapi.RuntimePorts{
			Repository: runtimeRepository{}, Completion: runtimeCompletion{}, Effect: runtimeEffect{}, Event: basePublisher,
		}, nil
	})
	factory := EventRuntimeFactory{Base: base}
	first := &recordingPublisher{}
	second := &recordingPublisher{}

	firstPorts, err := factory.Build(
		pipelineapi.WithEventPublisher(context.Background(), first),
		pipelineapi.Scope{}, pipelineapi.InvocationContext{},
	)
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	secondPorts, err := factory.Build(
		pipelineapi.WithEventPublisher(context.Background(), second),
		pipelineapi.Scope{}, pipelineapi.InvocationContext{},
	)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if firstPorts.Event != first || secondPorts.Event != second || firstPorts.Event == secondPorts.Event {
		t.Fatal("request event publishers were shared or replaced incorrectly")
	}
}
