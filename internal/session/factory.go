package session

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

type Defaults struct {
	Model        string
	SystemPrompt string
}

type DefaultFactory struct {
	defaults Defaults
	traceID  func() string
}

func NewDefaultFactory(defaults Defaults, traceID func() string) (*DefaultFactory, error) {
	if strings.TrimSpace(defaults.Model) == "" {
		return nil, errors.New("default session model is empty")
	}
	if traceID == nil {
		traceID = uuid.NewString
	}
	return &DefaultFactory{defaults: defaults, traceID: traceID}, nil
}

func (f *DefaultFactory) New(traceID string) *Session {
	if strings.TrimSpace(traceID) == "" {
		traceID = f.traceID()
	}
	return NewSession(traceID, f.defaults.SystemPrompt, f.defaults.Model)
}
