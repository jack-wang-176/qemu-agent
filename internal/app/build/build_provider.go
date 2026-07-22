package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

/* this function build llm provider which provides basic config of model connect
 * cater for three condition openrouter openai and ollama.
 */
func BuildProvider(cfg config.Config) (llm.Provider, error) {
	switch cfg.Agent.Provider {
	case "openrouter":
		return llm.NewOpenAIProvider(
			"openrouter",
			cfg.OpenRouter.APIKey,
			cfg.OpenRouter.BaseURL,
		), nil
	case "openai":
		return llm.NewOpenAIProvider(
			"openai",
			cfg.OpenAI.APIKey,
			cfg.OpenAI.BaseURL,
		), nil
	case "ollama":
		return llm.NewOpenAIProvider(
			"ollama",
			"ollama",
			cfg.Ollama.BaseURL,
		), nil
	default:
		return nil, fmt.Errorf(
			"unsupported provider %q",
			cfg.Agent.Provider,
		)
	}
}
