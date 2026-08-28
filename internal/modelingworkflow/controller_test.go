package modelingworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

type handleService struct {
	modelingapi.Service
	project       modelingapi.ProjectView
	evidencePage  modelingapi.EvidencePage
	showCalls     int
	evidenceCalls int
	capabilityErr error
}

func (s *handleService) Evidence(context.Context, modelingapi.CallContext, modelingapi.EvidenceRequest) (modelingapi.EvidencePage, error) {
	s.evidenceCalls++
	return modelingapi.CloneEvidencePage(s.evidencePage), nil
}

func (s *handleService) Capabilities(context.Context, modelingapi.CallContext) (modelingapi.Capabilities, error) {
	if s.capabilityErr != nil {
		return modelingapi.Capabilities{}, s.capabilityErr
	}
	return modelingapi.Capabilities{
		APIVersion:    "v1",
		EngineName:    "test-engine",
		EngineVersion: "v1",
		Operations: []modelingapi.OperationDescriptor{
			{Name: "plan", DisplayName: "Plan"},
		},
	}, nil
}

func TestHandleMapsModelingErrorWithoutLosingCause(t *testing.T) {
	cause := errors.New("provider token and /private/path")
	service := &handleService{capabilityErr: &modelingapi.Error{
		Public: modelingapi.NewPublicError(modelingapi.ErrorUnavailable, "Modeling is temporarily unavailable.", true, nil),
		Cause:  cause,
	}}
	controller := &Controller{modeling: service}
	_, err := controller.Handle(context.Background(), validWorkflowCall(), Request{Text: "model it"})
	var workflowErr *Error
	if !errors.As(err, &workflowErr) || workflowErr.Kind != ErrorUnavailable || !workflowErr.Retryable {
		t.Fatalf("Handle() error = %#v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("Handle() error lost the original cause")
	}
	if err.Error() == cause.Error() {
		t.Fatal("Handle() exposed the internal cause")
	}
}

func (s *handleService) Show(context.Context, modelingapi.CallContext, modelingapi.ShowRequest) (modelingapi.ProjectView, error) {
	s.showCalls++
	return modelingapi.CloneProjectView(s.project), nil
}

type handleStore struct {
	binding Binding
	found   bool
}

func (s handleStore) Load(context.Context, BindingKey) (Binding, bool, error) {
	return cloneBinding(s.binding), s.found, nil
}

func (handleStore) CompareAndSave(context.Context, Binding, int) (Binding, error) {
	panic("unexpected CompareAndSave call")
}

func (handleStore) Delete(context.Context, BindingKey, int) error {
	panic("unexpected Delete call")
}

type fixedInterpreter struct {
	intent Intent
}

func (i fixedInterpreter) Interpret(context.Context, InterpretInput) (Intent, error) {
	return i.intent, nil
}

type summaryPresenter struct{}

func (summaryPresenter) Present(_ context.Context, presentation Presentation) (string, error) {
	return presentation.Summary, nil
}

func TestHandleInspectUsesBoundProject(t *testing.T) {
	project := modelingapi.ProjectView{
		ID:               "mp-0123456789abcdef",
		Title:            "Test project",
		Revision:         2,
		Status:           modelingapi.ProjectRunning,
		CurrentOperation: "plan",
		CreatedAt:        time.Unix(1, 0),
		UpdatedAt:        time.Unix(2, 0),
	}
	service := &handleService{project: project}
	controller := &Controller{
		modeling:    service,
		binding:     handleStore{found: true, binding: Binding{ActiveProjectID: project.ID}},
		Interpreter: fixedInterpreter{intent: Intent{Kind: IntentInspect}},
		Presenter:   summaryPresenter{},
	}

	result, err := controller.Handle(context.Background(), validWorkflowCall(), Request{Text: "show it"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if service.showCalls != 1 {
		t.Fatalf("Show calls = %d, want 1", service.showCalls)
	}
	if result.State != StateWorking {
		t.Fatalf("state = %q, want %q", result.State, StateWorking)
	}
	if result.Project == nil || result.Project.ID != project.ID {
		t.Fatalf("project = %#v, want %q", result.Project, project.ID)
	}
}

func TestHandleEvidenceDoesNotClaimEmptyPageIsVerified(t *testing.T) {
	project := modelingapi.ProjectView{
		ID: "mp-0123456789abcdef", Title: "Test project", Revision: 2,
		Status: modelingapi.ProjectRunning, CurrentOperation: "plan",
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	}
	service := &handleService{project: project}
	controller := &Controller{
		modeling: service, binding: handleStore{found: true, binding: Binding{ActiveProjectID: project.ID}},
		Interpreter: fixedInterpreter{intent: Intent{Kind: IntentEvidence}}, Presenter: summaryPresenter{},
	}
	result, err := controller.Handle(context.Background(), validWorkflowCall(), Request{Text: "show evidence"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if service.evidenceCalls != 1 || len(result.Evidence) != 0 {
		t.Fatalf("evidence calls/result = %d/%#v", service.evidenceCalls, result.Evidence)
	}
	if result.Reply != "No verification evidence is available for this project." {
		t.Fatalf("reply = %q", result.Reply)
	}
}

func TestHandleInspectWithoutProjectNeedsInput(t *testing.T) {
	service := &handleService{}
	controller := &Controller{
		modeling:    service,
		binding:     handleStore{},
		Interpreter: fixedInterpreter{intent: Intent{Kind: IntentInspect}},
		Presenter:   summaryPresenter{},
	}

	result, err := controller.Handle(context.Background(), validWorkflowCall(), Request{Text: "show it"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if result.State != StateNeedsInput {
		t.Fatalf("state = %q, want %q", result.State, StateNeedsInput)
	}
	if result.Project != nil {
		t.Fatalf("project = %#v, want nil", result.Project)
	}
}

func TestChildCallPreservesIdentityAndDerivesAtomicIDs(t *testing.T) {
	parent := validWorkflowCall()
	parent.IdempotencyKey = "turn-key"

	first := childCall(parent, "Advance", "mp-0123456789abcdef", "plan", 0)
	retry := childCall(parent, "Advance", "mp-0123456789abcdef", "plan", 0)
	next := childCall(parent, "Advance", "mp-0123456789abcdef", "plan", 1)

	if first != retry {
		t.Fatalf("same atomic call derived different contexts: %#v != %#v", first, retry)
	}
	if first.RequestID == next.RequestID || first.IdempotencyKey == next.IdempotencyKey {
		t.Fatal("different ordinals derived identical atomic identifiers")
	}
	if first.UserID != parent.UserID || first.SessionID != parent.SessionID || first.WorkspaceID != parent.WorkspaceID {
		t.Fatalf("identity was not preserved: %#v", first)
	}
}

func validWorkflowCall() CallContext {
	return CallContext{
		RequestID:   "request-1",
		TraceID:     "trace-1",
		WorkspaceID: "workspace-1",
		UserID:      "user-1",
		SessionID:   "session-1",
		SessionKey:  "cli:default",
		Channel:     "cli",
		Interactive: true,
	}
}
