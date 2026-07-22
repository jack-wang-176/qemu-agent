package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

func BuildContextManager(
	cfg config.Config,
	provider llm.Provider,
) (*contextmgr.CompactorManager, error) {
	tokenizer, err := contextmgr.NewTokenizer(
		cfg.Agent.Model,
	)
	if err != nil {
		return nil, fmt.Errorf("build tokenizer: %w", err)
	}

	summarizer := contextmgr.NewLLMSummarizer(
		provider,
		cfg.Agent.KeepRecentTurns,
		cfg.Agent.Model,
	)
	manager := contextmgr.NewCompactorManager(
		cfg.Agent.MaxContextTokens,
		*tokenizer,
		summarizer,
	)
	return &manager, nil
}
