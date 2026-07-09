package session

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
)

type Session struct {
	mu           sync.Mutex
	Id           string
	TraceId      string
	TokenUsage   int
	SystemPrompt string
	Msg          []openai.ChatCompletionMessageParamUnion
	UpdatedAt    time.Time
	CreatedAt    time.Time
}

func NewSession(traceId, prompt string) Session {
	return Session{
		Id:           uuid.NewString(),
		TraceId:      traceId,
		TokenUsage:   0,
		SystemPrompt: prompt,
		Msg:          make([]openai.ChatCompletionMessageParamUnion, 0),
		UpdatedAt:    time.Now(),
		CreatedAt:    time.Now(),
	}
}
