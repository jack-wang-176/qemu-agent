package session

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type Defaults struct {
	ModelRef     llm.ModelRef
	SystemPrompt string
}

type DefaultFactory struct {
	defaults Defaults
	traceID  func() string
}

func NewDefaultFactory(defaults Defaults, traceID func() string) (*DefaultFactory, error) {
	ref, err := llm.NormalizeModelRef(defaults.ModelRef)
	if err != nil {
		return nil, fmt.Errorf("default session model: %w", err)
	}
	defaults.ModelRef = ref
	if traceID == nil {
		traceID = uuid.NewString
	}
	return &DefaultFactory{defaults: defaults, traceID: traceID}, nil
}

func (f *DefaultFactory) New(traceID string) *Session {
	if strings.TrimSpace(traceID) == "" {
		traceID = f.traceID()
	}
	return NewSession(traceID, f.defaults.SystemPrompt, f.defaults.ModelRef)
}
