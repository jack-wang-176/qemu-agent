package build

import (
	"context"
	"errors"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
)

// ProviderCompleter adapts one resolved model to memory.Completer. The memory
// package must not import the provider layer, and the extraction call must not
// look like a normal turn: no tools are offered and no session is involved, so
// an extraction can never execute anything or grow the transcript.
type ProviderCompleter struct {
	provider  llm.Provider
	model     string
	maxTokens int
}

var _ memory.Completer = (*ProviderCompleter)(nil)

func NewProviderCompleter(resolved llm.ResolvedModel, maxTokens int) (*ProviderCompleter, error) {
	if resolved.Provider == nil {
		return nil, errors.New("completer provider is nil")
	}
	if strings.TrimSpace(resolved.Definition.Ref.Model) == "" {
		return nil, errors.New("completer model is empty")
	}
	if maxTokens <= 0 {
		return nil, errors.New("completer max tokens must be > 0")
	}
	if definition := resolved.Definition; definition.MaxOutput > 0 && maxTokens > definition.MaxOutput {
		maxTokens = definition.MaxOutput
	}
	return &ProviderCompleter{provider: resolved.Provider, model: resolved.Definition.Ref.Model, maxTokens: maxTokens}, nil
}

func (c *ProviderCompleter) Complete(ctx context.Context, system, user string) (string, error) {
	response, err := c.provider.Complete(ctx, llm.Request{
		Model: c.model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: system},
			{Role: llm.RoleUser, Content: user},
		},
		MaxTokens: c.maxTokens,
	})
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", errors.New("completer received an empty response")
	}
	// Tool calls are ignored rather than executed: this path has no executor and
	// no approval, so the only safe reading of a tool call here is "no output".
	return response.Message.Content, nil
}
