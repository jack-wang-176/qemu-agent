package modelingworkflow

import (
	"context"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

type BindingKey struct {
	WorkspaceID    string
	UserID         string
	ConversationID string
}

type AwaitingKind string

const (
	AwaitingNone        AwaitingKind = ""
	AwaitingRequirement AwaitingKind = "requirement"
	AwaitingSource      AwaitingKind = "source"
)

type Binding struct {
	Key             BindingKey
	Version         int
	ActiveProjectID modelingapi.ProjectID
	Title           string
	Instruction     string
	Sources         []modelingapi.SourceRef
	Awaiting        AwaitingKind
	UpdatedAt       time.Time
}

type Store interface {
	Load(context.Context, BindingKey) (Binding, bool, error)
	CompareAndSave(context.Context, Binding, int) (Binding, error)
	Delete(context.Context, BindingKey, int) error
}
