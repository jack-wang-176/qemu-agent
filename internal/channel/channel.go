package channel

import (
	"context"
)

// Inbound is a channel-neutral request passed to the application layer.
type Inbound struct {
	SessionKey string
	Channel    string
	UserID     string
	Text       string
	Metadata   map[string]string
}

type Action string

const (
	ActionReply Action = "reply"
	ActionExit  Action = "exit"
)

// Outbound is a channel-neutral response returned by the application layer.
type Outbound struct {
	SessionKey string
	Text       string
	Action     Action
}

// Handler processes one inbound channel request.
type Handler interface {
	Handle(ctx context.Context, in Inbound) (Outbound, error)
}

// Channel adapts an external transport to the application Handler.
type Channel interface {
	Name() string
	Run(context.Context, Handler) error
}
