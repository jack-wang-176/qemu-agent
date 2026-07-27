package app

import (
	"context"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"testing"
)

type appTestProvider struct{}

func (appTestProvider) Name() string { return "ollama" }
func (appTestProvider) Capability() llm.Capabilities {
	return llm.Capabilities{Tools: true, MaxContext: 4096}
}
func (appTestProvider) Complete(context.Context, llm.Request) (*llm.Response, error) { return nil, nil }
func (appTestProvider) Stream(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, nil
}
func newTestModels(t *testing.T) *llm.ModelRegistry {
	t.Helper()
	r := llm.NewModelRegistry()
	p := appTestProvider{}
	for _, m := range []string{"test-model", "model"} {
		if err := r.Register(llm.ModelDefinition{Ref: llm.ModelRef{Provider: "ollama", Model: m}, Aliases: []string{m}, MaxContext: 4096, Tools: true}, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	return r
}
