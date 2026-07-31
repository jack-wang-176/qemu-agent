package contextmgr

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
)

type Compactor interface {
	Name() string
	Compact(ctx context.Context, model string, msgs []openai.ChatCompletionMessageParamUnion) ([]openai.ChatCompletionMessageParamUnion, bool, error)
}

type CompactorManager struct {
	MaxToken   int
	Tokenizer  Tokenizer
	Compactors []Compactor
}

func NewCompactorManager(maxtoken int, tokenizer Tokenizer, compactor ...Compactor) CompactorManager {
	return CompactorManager{
		MaxToken:   maxtoken,
		Tokenizer:  tokenizer,
		Compactors: compactor,
	}
}

func (c *CompactorManager) EnforceBudget(ctx context.Context, model string, msgs []openai.ChatCompletionMessageParamUnion) ([]openai.ChatCompletionMessageParamUnion, int, error) {
	currentMsgs := msgs
	currentToken := c.Tokenizer.Count(currentMsgs)
	if currentToken < c.MaxToken {
		return currentMsgs, currentToken, nil
	}
	for _, compactor := range c.Compactors {
		newMsg, success, err := compactor.Compact(ctx, model, currentMsgs)
		if err != nil || !success {
			return currentMsgs, currentToken, fmt.Errorf("compactor %s failed: %w", compactor.Name(), err)
		}
		currentMsgs = newMsg
	}
	currentToken = c.Tokenizer.Count(currentMsgs)
	if currentToken < c.MaxToken {
		return currentMsgs, currentToken, nil
	} else {
		return currentMsgs, currentToken, fmt.Errorf("use all compactor but still")
	}
}
