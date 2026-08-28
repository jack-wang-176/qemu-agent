package modelingworkflow

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

type CallContext struct {
	RequestID      string
	TraceID        string
	WorkspaceID    string
	UserID         string
	SessionID      string
	SessionKey     string
	Channel        string
	IdempotencyKey string
	Interactive    bool
}

type ConversationMsg struct {
	Role string
	Text string
}

type Request struct {
	History    []ConversationMsg
	Text       string
	Hashistory bool
}

type State string

const (
	StateNeedsInput    State = "needs_input"
	StateWorking       State = "working"
	StateAwaitingApply State = "awaiting_apply"
	StateCompleted     State = "completed"
	StateFailed        State = "failed"
)

type Result struct {
	Reply    string
	State    State
	Project  *modelingapi.ProjectView
	Artifact []modelingapi.ArtifactDescriptor
	Evidence []modelingapi.ArtifactDescriptor
}

type Service interface {
	Handle(context.Context, CallContext, Request) (Result, error)
}
