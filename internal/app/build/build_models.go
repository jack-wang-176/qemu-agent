package build

import (
	"errors"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

func BuildModelRegistry(cfg config.Config, factory llm.ProviderFactory) (*llm.ModelRegistry, llm.ModelRef, error) {
	if factory == nil {
		return nil, llm.ModelRef{}, errors.New("provider factory is nil")
	}
	registry := llm.NewModelRegistry()
	providers := make(map[string]llm.Provider)
	for _, item := range cfg.Models.Definitions {
		ref, err := llm.NormalizeModelRef(llm.ModelRef{Provider: item.Provider, Model: item.Name})
		if err != nil {
			return nil, llm.ModelRef{}, fmt.Errorf("normalize model: %w", err)
		}
		provider := providers[ref.Provider]
		if provider == nil {
			provider, err = factory.Build(ref.Provider)
			if err != nil {
				return nil, llm.ModelRef{}, fmt.Errorf("build provider %q: %w", ref.Provider, err)
			}
			providers[ref.Provider] = provider
		}
		def := llm.ModelDefinition{Ref: ref, DisplayName: item.DisplayName, Aliases: item.Aliases, MaxContext: item.MaxContext, MaxOutput: item.MaxOutput, Tools: item.Tools, Streaming: item.Streaming}
		def, err = registry.EffectiveDefinition(def, provider)
		if err != nil {
			return nil, llm.ModelRef{}, err
		}
		if err := registry.Register(def, provider); err != nil {
			return nil, llm.ModelRef{}, fmt.Errorf("register model %q: %w", ref.String(), err)
		}
	}
	if err := registry.Seal(); err != nil {
		return nil, llm.ModelRef{}, err
	}
	defaultRef, err := llm.NormalizeModelRef(llm.ModelRef{Provider: cfg.Agent.Provider, Model: cfg.Agent.Model})
	if err != nil {
		return nil, llm.ModelRef{}, err
	}
	if _, err := registry.Resolve(defaultRef); err != nil {
		return nil, llm.ModelRef{}, fmt.Errorf("resolve default model: %w", err)
	}
	return registry, defaultRef, nil
}
