package llm

import (
	"context"
	"errors"
	"testing"
)

type registryTestProvider struct{ name string }

func (p registryTestProvider) Name() string { return p.name }
func (registryTestProvider) Capability() Capabilities {
	return Capabilities{Tools: true, Streaming: true, MaxContext: 4096}
}
func (registryTestProvider) Complete(context.Context, Request) (*Response, error) { return nil, nil }
func (registryTestProvider) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	return nil, nil
}

func TestModelRegistryResolveAliasAndNativeRef(t *testing.T) {
	registry := NewModelRegistry()
	provider := registryTestProvider{name: "ollama"}
	def := ModelDefinition{
		Ref:        ModelRef{Provider: " OLLAMA ", Model: "qwen2.5:7b"},
		Aliases:    []string{" Local "},
		MaxContext: 4096,
		Tools:      true,
	}
	if err := registry.Register(def, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	for _, query := range []string{"local", "ollama:qwen2.5:7b"} {
		resolved, err := registry.ResolveName(query)
		if err != nil {
			t.Fatalf("ResolveName(%q) error = %v", query, err)
		}
		if resolved.Definition.Ref.Model != "qwen2.5:7b" || resolved.Provider != provider {
			t.Fatalf("ResolveName(%q) = %#v", query, resolved)
		}
	}
}

func TestModelRegistryRejectsConflictsAndIsDefensive(t *testing.T) {
	registry := NewModelRegistry()
	provider := registryTestProvider{name: "openai"}
	def := ModelDefinition{
		Ref:        ModelRef{Provider: "openai", Model: "gpt-test"},
		Aliases:    []string{"test"},
		MaxContext: 4096,
	}
	if err := registry.Register(def, provider); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(def, provider); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	listed := registry.List()
	listed[0].Aliases[0] = "changed"
	listedAgain := registry.List()
	if listedAgain[0].Aliases[0] != "test" {
		t.Fatalf("registry aliases were mutated: %#v", listedAgain)
	}
	if err := registry.Register(ModelDefinition{}, provider); err == nil {
		t.Fatal("register after seal succeeded")
	}
}

func TestModelRegistryNotFoundClassification(t *testing.T) {
	registry := NewModelRegistry()
	_, err := registry.ResolveName("missing")
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("ResolveName() error = %v, want ErrModelNotFound", err)
	}
}
