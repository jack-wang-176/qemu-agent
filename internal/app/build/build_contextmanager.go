package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

func BuildContextManager(
	contextConfig config.ContextConfig,
	summaryModel llm.ResolvedModel,
) (*contextmgr.CompactorManager, error) {
	tokenizer, err := contextmgr.NewTokenizer(
		summaryModel.Definition.Ref.Model,
	)
	if err != nil {
		return nil, fmt.Errorf("build tokenizer: %w", err)
	}

	summarizer := contextmgr.NewLLMSummarizer(
		summaryModel.Provider,
		contextConfig.KeepRecentTurns,
		summaryModel.Definition.Ref.Model,
	)
	manager := contextmgr.NewCompactorManager(
		contextConfig.MaxTokens,
		*tokenizer,
		summarizer,
	)
	return &manager, nil
}
