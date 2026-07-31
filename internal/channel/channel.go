package channel

import (
	"context"
	"errors"

	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

type Request struct {
	Inbound      Inbound
	Capabilities Capabilities
	Events       runstream.EventSink
}

// Capabilities describes request-scoped transport capabilities. It is explicit
// so the application never infers security properties from a channel name.
type Capabilities struct {
	InteractiveApproval bool
}

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
	Handle(context.Context, Request) (Outbound, error)
}

// Channel adapts an external transport to the application Handler.
type Channel interface {
	Name() string
	Run(context.Context, Handler) error
}

// if this err is recoverable then out
type RecoverableError interface {
	error
	Recoverable() bool
}

func IsRecoverable(err error) bool {
	var target RecoverableError
	return errors.As(err, &target) && target.Recoverable()
}
