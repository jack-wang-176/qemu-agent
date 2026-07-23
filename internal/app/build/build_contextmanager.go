package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

func BuildContextManager(
	agentConfig config.AgentConfig,
	contextConfig config.ContextConfig,
	provider llm.Provider,
) (*contextmgr.CompactorManager, error) {
	tokenizer, err := contextmgr.NewTokenizer(
		agentConfig.Model,
	)
	if err != nil {
		return nil, fmt.Errorf("build tokenizer: %w", err)
	}

	summarizer := contextmgr.NewLLMSummarizer(
		provider,
		contextConfig.KeepRecentTurns,
		agentConfig.Model,
	)
	manager := contextmgr.NewCompactorManager(
		contextConfig.MaxTokens,
		*tokenizer,
		summarizer,
	)
	return &manager, nil
}
