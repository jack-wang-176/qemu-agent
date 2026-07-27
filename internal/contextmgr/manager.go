package contextmgr

import (
	"context"
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

/* compact only deal on history.*/
type Compactor interface {
	Name() string
	Compact(ctx context.Context, model string, history session.History) (session.History, bool, error)
}

type CompactorManager struct {
	MaxToken   int
	Tokenizer  Tokenizer
	Compactors []Compactor
}

type ModelBudget struct {
	Ref        llm.ModelRef
	MaxContext int
}

func NewCompactorManager(maxtoken int, tokenizer Tokenizer, compactor ...Compactor) CompactorManager {
	return CompactorManager{
		MaxToken:   maxtoken,
		Tokenizer:  tokenizer,
		Compactors: compactor,
	}
}

/* the link between history and []llm.Message.*/
func (c *CompactorManager) EnforceBudget(ctx context.Context, budget ModelBudget, msgs []llm.Message) ([]llm.Message, int, error) {
	if budget.MaxContext <= 0 {
		return nil, 0, fmt.Errorf("model max context must be > 0")
	}
	effectiveMax := min(c.MaxToken, budget.MaxContext)
	currentMsgs := append([]llm.Message(nil), msgs...)
	currentToken := c.Tokenizer.Count(currentMsgs)
	if currentToken < effectiveMax {
		return currentMsgs, currentToken, nil
	}
	/* convert into history recover after compacting.*/
	history, err := session.ConvertIntoHistory(currentMsgs)
	if err != nil {
		return currentMsgs, currentToken, fmt.Errorf("convert messages into history: %w", err)
	}
	for _, compactor := range c.Compactors {
		newHistory, success, err := compactor.Compact(ctx, budget.Ref.Model, history)
		if err != nil {
			return currentMsgs, currentToken, fmt.Errorf("compactor %s failed: %w", compactor.Name(), err)
		}
		if !success {
			continue
		}
		history = newHistory
		currentMsgs = newHistory.History()
		currentToken = c.Tokenizer.Count(currentMsgs)
		if currentToken < effectiveMax {
			return currentMsgs, currentToken, nil
		}
	}
	currentToken = c.Tokenizer.Count(currentMsgs)
	if currentToken < effectiveMax {
		return currentMsgs, currentToken, nil
	}
	return currentMsgs, currentToken, fmt.Errorf("context remains over budget after all compactors: %d >= %d", currentToken, effectiveMax)
}
