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

func NewCompactorManager(maxtoken int, tokenizer Tokenizer, compactor ...Compactor) CompactorManager {
	return CompactorManager{
		MaxToken:   maxtoken,
		Tokenizer:  tokenizer,
		Compactors: compactor,
	}
}

/* the link between history and []llm.Message.*/
func (c *CompactorManager) EnforceBudget(ctx context.Context, model string, msgs []llm.Message) ([]llm.Message, int, error) {
	currentMsgs := append([]llm.Message(nil), msgs...)
	currentToken := c.Tokenizer.Count(currentMsgs)
	if currentToken < c.MaxToken {
		return currentMsgs, currentToken, nil
	}
	/* convert into history recover after compacting.*/
	history, err := session.ConvertIntoHistory(currentMsgs)
	if err != nil {
		return currentMsgs, currentToken, fmt.Errorf("fail to converinto history")
	}
	for _, compactor := range c.Compactors {
		newHistory, success, err := compactor.Compact(ctx, model, history)
		if err != nil || !success {
			return currentMsgs, currentToken, fmt.Errorf("compactor %s failed: %w", compactor.Name(), err)
		}
		currentMsgs = newHistory.History()
	}
	currentToken = c.Tokenizer.Count(currentMsgs)
	if currentToken < c.MaxToken {
		return currentMsgs, currentToken, nil
	} else {
		return currentMsgs, currentToken, fmt.Errorf("use all compactor but still")
	}
}
