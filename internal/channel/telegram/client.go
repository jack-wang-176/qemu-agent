package telegram

import (
	"context"
	"time"
)

type PollRequest struct {
	Offset  int64
	Timeout time.Duration
	Limit   int
}

type PollResult struct {
	Updates    []Update
	NextOffset int64
}

type SendRequest struct {
	Target Target
	Text   string
}

type EditRequest struct {
	ChatID    int64
	MessageID int64
	Text      string
}

type Client interface {
	Poll(context.Context, PollRequest) (PollResult, error)
	SendMessage(context.Context, SendRequest) (SentMessage, error)
	EditMessage(context.Context, EditRequest) (SentMessage, error)
}
