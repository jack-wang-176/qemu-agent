package modelingworkflow

import (
	"context"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

type startService struct {
	modelingapi.Service
	project    modelingapi.ProjectView
	createCall modelingapi.CallContext
	createReq  modelingapi.CreateRequest
	creates    int
	shows      int
}

func (s *startService) Create(
	_ context.Context,
	call modelingapi.CallContext,
	req modelingapi.CreateRequest,
) (modelingapi.ProjectView, error) {
	s.creates++
	s.createCall = call
	s.createReq = req
	return modelingapi.CloneProjectView(s.project), nil
}

func (s *startService) Show(
	_ context.Context,
	_ modelingapi.CallContext,
	_ modelingapi.ShowRequest,
) (modelingapi.ProjectView, error) {
	s.shows++
	return modelingapi.CloneProjectView(s.project), nil
}

type startStore struct {
	saved    Binding
	expected int
	saves    int
}

func (*startStore) Load(context.Context, BindingKey) (Binding, bool, error) {
	panic("unexpected Load call")
}

func (s *startStore) CompareAndSave(_ context.Context, next Binding, expected int) (Binding, error) {
	s.saves++
	s.expected = expected
	next.Version = expected + 1
	s.saved = cloneBinding(next)
	return cloneBinding(next), nil
}

func (*startStore) Delete(context.Context, BindingKey, int) error {
	panic("unexpected Delete call")
}

func TestStartSavesIntakeWithoutCreatingProject(t *testing.T) {
	service := &startService{}
	store := &startStore{}
	controller := &Controller{modeling: service, binding: store, now: func() time.Time { return time.Unix(10, 0) }}
	key := BindingKey{WorkspaceID: "workspace-1", UserID: "user-1", ConversationID: "session-1"}

	presentation, err := controller.start(
		context.Background(),
		validStartCall(),
		key,
		Binding{},
		false,
		false,
		startCapabilities(false),
		Intent{Kind: IntentStart, Title: "UART model"},
	)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if service.creates != 0 {
		t.Fatalf("Create calls = %d, want 0", service.creates)
	}
	if store.saves != 1 || store.saved.Awaiting != AwaitingRequirement {
		t.Fatalf("saved binding = %#v", store.saved)
	}
	if store.saved.Title != "UART model" || store.saved.Instruction != "" {
		t.Fatalf("saved intake = %#v", store.saved)
	}
	if presentation.State != StateNeedsInput || presentation.Question == "" {
		t.Fatalf("presentation = %#v", presentation)
	}
}

func TestStartCreatesSavesAndInspectsProject(t *testing.T) {
	project := validCreatedProject()
	service := &startService{project: project}
	store := &startStore{}
	controller := &Controller{modeling: service, binding: store, now: func() time.Time { return time.Unix(10, 0) }}
	key := BindingKey{WorkspaceID: "workspace-1", UserID: "user-1", ConversationID: "session-1"}

	presentation, err := controller.start(
		context.Background(),
		validStartCall(),
		key,
		Binding{},
		false,
		false,
		startCapabilities(false),
		Intent{Kind: IntentStart, Title: "UART model", Instruction: "Model a UART device."},
	)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if service.creates != 1 || service.shows != 1 {
		t.Fatalf("Create/Show calls = %d/%d, want 1/1", service.creates, service.shows)
	}
	if service.createReq.Title != "UART model" {
		t.Fatalf("Create request = %#v", service.createReq)
	}
	if service.createCall.IdempotencyKey == "" || service.createCall.SessionID != "session-1" {
		t.Fatalf("Create call = %#v", service.createCall)
	}
	if store.saved.ActiveProjectID != project.ID || store.saved.Instruction != "Model a UART device." {
		t.Fatalf("saved binding = %#v", store.saved)
	}
	if presentation.State != StateWorking || presentation.Project == nil || presentation.Project.ID != project.ID {
		t.Fatalf("presentation = %#v", presentation)
	}
}

func TestStartWaitsForSourcesRequiredByCreatedOperation(t *testing.T) {
	project := validCreatedProject()
	service := &startService{project: project}
	store := &startStore{}
	controller := &Controller{modeling: service, binding: store}

	presentation, err := controller.start(
		context.Background(),
		validStartCall(),
		BindingKey{WorkspaceID: "workspace-1", ConversationID: "session-1"},
		Binding{},
		false,
		false,
		startCapabilities(true),
		Intent{Kind: IntentStart, Title: "UART model", Instruction: "Model a UART device."},
	)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if store.saved.Awaiting != AwaitingSource {
		t.Fatalf("awaiting = %q, want %q", store.saved.Awaiting, AwaitingSource)
	}
	if presentation.State != StateNeedsInput || presentation.Project == nil {
		t.Fatalf("presentation = %#v", presentation)
	}
}

func TestCollectStartInputForStartNewDoesNotInheritOldInput(t *testing.T) {
	key := BindingKey{WorkspaceID: "workspace-1", ConversationID: "session-1"}
	current := Binding{
		Key:             key,
		Version:         7,
		ActiveProjectID: "mp-fedcba9876543210",
		Title:           "Old title",
		Instruction:     "Old instruction",
		Sources:         []modelingapi.SourceRef{{Kind: "workspace_path", Value: "old/source.md"}},
	}

	next, expected, err := collectStartInput(key, current, true, true, Intent{
		Kind:        IntentStartNew,
		Title:       "New title",
		Instruction: "New instruction",
	})
	if err != nil {
		t.Fatalf("collectStartInput() error = %v", err)
	}
	if expected != 7 || next.ActiveProjectID != "" || next.Title != "New title" || next.Instruction != "New instruction" || next.Sources != nil {
		t.Fatalf("next binding = %#v, expected version = %d", next, expected)
	}
}

func validStartCall() CallContext {
	return CallContext{
		RequestID:      "request-1",
		TraceID:        "trace-1",
		WorkspaceID:    "workspace-1",
		UserID:         "user-1",
		SessionID:      "session-1",
		SessionKey:     "cli:default",
		Channel:        "cli",
		IdempotencyKey: "turn-key",
		Interactive:    true,
	}
}

func validCreatedProject() modelingapi.ProjectView {
	return modelingapi.ProjectView{
		ID:               "mp-0123456789abcdef",
		Title:            "UART model",
		Revision:         0,
		Status:           modelingapi.ProjectPending,
		CurrentOperation: "plan",
		CreatedAt:        time.Unix(1, 0),
		UpdatedAt:        time.Unix(1, 0),
	}
}

func startCapabilities(requiresSources bool) modelingapi.Capabilities {
	return modelingapi.Capabilities{
		APIVersion:    "v1",
		EngineName:    "test-engine",
		EngineVersion: "v1",
		Operations: []modelingapi.OperationDescriptor{
			{Name: "plan", RequiresSources: requiresSources, Mutating: true},
		},
	}
}
