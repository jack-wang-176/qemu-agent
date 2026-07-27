package build

import (
	"context"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type countingFactory struct{ calls map[string]int }

func (f *countingFactory) Build(name string) (llm.Provider, error) {
	f.calls[name]++
	return buildTestProvider{name: name}, nil
}

type buildTestProvider struct{ name string }

func (p buildTestProvider) Name() string { return p.name }
func (buildTestProvider) Capability() llm.Capabilities {
	return llm.Capabilities{Tools: true, Streaming: true, MaxContext: 8192}
}
func (buildTestProvider) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return nil, nil
}
func (buildTestProvider) Stream(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, nil
}

func TestBuildModelRegistryReusesProvider(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{Provider: "ollama", Model: "one"}, Models: config.ModelConfig{Definitions: []config.ModelDefinitionConfig{
		{Provider: "ollama", Name: "one", MaxContext: 4096, Tools: true},
		{Provider: "ollama", Name: "two", MaxContext: 4096, Tools: true},
	}}}
	factory := &countingFactory{calls: map[string]int{}}
	registry, defaultRef, err := BuildModelRegistry(cfg, factory)
	if err != nil {
		t.Fatal(err)
	}
	if factory.calls["ollama"] != 1 {
		t.Fatalf("build calls = %d", factory.calls["ollama"])
	}
	one, _ := registry.Resolve(defaultRef)
	two, _ := registry.Resolve(llm.ModelRef{Provider: "ollama", Model: "two"})
	if one.Provider != two.Provider {
		t.Fatal("models did not share provider instance")
	}
}
