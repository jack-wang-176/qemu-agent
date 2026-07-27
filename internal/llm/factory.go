package llm

import (
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/config"
)

type ProviderFactory interface {
	Build(name string) (Provider, error)
}

type ConfigProviderFactory struct {
	cfg config.ProviderConfig
}

func NewConfigProviderFactory(cfg config.ProviderConfig) *ConfigProviderFactory {
	return &ConfigProviderFactory{cfg: cfg}
}

func (f *ConfigProviderFactory) Build(name string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openrouter":
		return NewOpenAIProvider("openrouter", f.cfg.OpenRouter.APIKey, f.cfg.OpenRouter.BaseURL), nil
	case "openai":
		return NewOpenAIProvider("openai", f.cfg.OpenAI.APIKey, f.cfg.OpenAI.BaseURL), nil
	case "ollama":
		return NewOpenAIProvider("ollama", "ollama", f.cfg.Ollama.BaseURL), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", name)
	}
}
