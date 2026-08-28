package modelingworkflow

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

type IntentKind string

const (
	IntentStart        IntentKind = "start"
	IntentContinue     IntentKind = "continue"
	IntentInspect      IntentKind = "inspect"
	IntentProvideInput IntentKind = "provide_input"
	IntentReadArtifact IntentKind = "read_artifact"
	IntentEvidence     IntentKind = "evidence"
	IntentStartNew     IntentKind = "start_new"
)

type Intent struct {
	Kind        IntentKind
	Title       string
	Instruction string
	Sources     []modelingapi.SourceRef
	ArtifactID  modelingapi.ArtifactID
}

type InterpretInput struct {
	Text        string
	History     []ConversationMsg
	HasHistory  bool
	Awaiting    AwaitingKind
	WorkspaceID string
	UserID      string
}

type Interpreter interface {
	Interpret(context.Context, InterpretInput) (Intent, error)
}

type Presentation struct {
	State     State
	Summary   string
	Question  string
	Project   *modelingapi.ProjectView
	Artifacts []modelingapi.ArtifactDescriptor
	Evidence  []modelingapi.ArtifactDescriptor
	Content   *modelingapi.ArtifactContent
	Failure   *modelingapi.PublicError
}

type Presenter interface {
	Present(context.Context, Presentation) (string, error)
}
