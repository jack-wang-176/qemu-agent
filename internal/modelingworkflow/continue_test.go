package modelingworkflow

import (
	"context"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

type advanceService struct {
	modelingapi.Service
	results       []modelingapi.OperationResult
	calls         []modelingapi.CallContext
	requests      []modelingapi.AdvanceRequest
	content       modelingapi.ArtifactContent
	artifactReads int
}

func (s *advanceService) Advance(
	_ context.Context,
	call modelingapi.CallContext,
	req modelingapi.AdvanceRequest,
) (modelingapi.OperationResult, error) {
	s.calls = append(s.calls, call)
	s.requests = append(s.requests, modelingapi.CloneAdvanceRequest(req))
	result := s.results[0]
	s.results = s.results[1:]
	return modelingapi.CloneOperationResult(result), nil
}

func (s *advanceService) ReadArtifact(
	_ context.Context,
	_ modelingapi.CallContext,
	req modelingapi.ReadArtifactRequest,
) (modelingapi.ArtifactContent, error) {
	s.artifactReads++
	if req.Offset != 0 || req.ArtifactID != s.content.Artifact.ID {
		panic("unexpected artifact request")
	}
	return modelingapi.CloneArtifactContent(s.content), nil
}

type advanceStore struct {
	saved Binding
	saves int
}

func (*advanceStore) Load(context.Context, BindingKey) (Binding, bool, error) {
	panic("unexpected Load call")
}

func (s *advanceStore) CompareAndSave(_ context.Context, next Binding, expected int) (Binding, error) {
	s.saves++
	next.Version = expected + 1
	s.saved = cloneBinding(next)
	return cloneBinding(next), nil
}

func (*advanceStore) Delete(context.Context, BindingKey, int) error {
	panic("unexpected Delete call")
}

func TestAdvanceUntilBoundaryUsesOrdinalAndStopsAtCompleted(t *testing.T) {
	initial := advanceProject("plan", modelingapi.ProjectPending, 0)
	afterPlan := advanceProject("infer", modelingapi.ProjectRunning, 2)
	completed := advanceProject("", modelingapi.ProjectCompleted, 4)
	service := &advanceService{results: []modelingapi.OperationResult{
		{Project: afterPlan, Operation: "plan", Status: modelingapi.OperationSucceeded},
		{Project: completed, Operation: "infer", Status: modelingapi.OperationSucceeded},
	}}
	controller := &Controller{modeling: service, binding: &advanceStore{}, maxOperations: 3}

	presentation, err := controller.advanceUntilBoundary(
		context.Background(),
		validStartCall(),
		Binding{Instruction: "Model the device."},
		initial,
		advanceCapabilities(),
	)
	if err != nil {
		t.Fatalf("advanceUntilBoundary() error = %v", err)
	}
	if len(service.calls) != 2 {
		t.Fatalf("Advance calls = %d, want 2", len(service.calls))
	}
	if service.calls[0].RequestID == service.calls[1].RequestID ||
		service.calls[0].IdempotencyKey == service.calls[1].IdempotencyKey {
		t.Fatal("different ordinals produced identical atomic identifiers")
	}
	if service.requests[0].ExpectedRevision != 0 || service.requests[1].ExpectedRevision != 2 {
		t.Fatalf("expected revisions = %d/%d", service.requests[0].ExpectedRevision, service.requests[1].ExpectedRevision)
	}
	if presentation.State != StateCompleted || presentation.Project == nil || presentation.Project.Revision != 4 {
		t.Fatalf("presentation = %#v", presentation)
	}
}

func TestAdvanceUntilBoundaryWaitsForRequiredSources(t *testing.T) {
	service := &advanceService{}
	store := &advanceStore{}
	controller := &Controller{modeling: service, binding: store, maxOperations: 2}
	capabilities := advanceCapabilities()
	capabilities.Operations[1].RequiresSources = true

	presentation, err := controller.advanceUntilBoundary(
		context.Background(),
		validStartCall(),
		Binding{Version: 3, Instruction: "Model the device."},
		advanceProject("plan", modelingapi.ProjectPending, 0),
		capabilities,
	)
	if err != nil {
		t.Fatalf("advanceUntilBoundary() error = %v", err)
	}
	if len(service.calls) != 0 {
		t.Fatalf("Advance calls = %d, want 0", len(service.calls))
	}
	if store.saves != 1 || store.saved.Awaiting != AwaitingSource {
		t.Fatalf("saved binding = %#v", store.saved)
	}
	if presentation.State != StateNeedsInput {
		t.Fatalf("state = %q, want %q", presentation.State, StateNeedsInput)
	}
}

func TestAdvanceUntilBoundaryReturnsBlockedPresentation(t *testing.T) {
	initial := advanceProject("plan", modelingapi.ProjectPending, 0)
	blocked := advanceProject("plan", modelingapi.ProjectBlocked, 2)
	service := &advanceService{results: []modelingapi.OperationResult{{
		Project: blocked, Operation: "plan", Status: modelingapi.OperationBlocked,
		Blocked: true, Reason: "awaiting_human",
	}}}
	controller := &Controller{modeling: service, binding: &advanceStore{}, maxOperations: 2}

	presentation, err := controller.advanceUntilBoundary(
		context.Background(), validStartCall(), Binding{Instruction: "Model it."}, initial, advanceCapabilities(),
	)
	if err != nil {
		t.Fatalf("advanceUntilBoundary() error = %v", err)
	}
	if presentation.State != StateNeedsInput || presentation.Question == "" {
		t.Fatalf("presentation = %#v", presentation)
	}
}

func TestAdvanceUntilBoundaryProjectsAwaitingApplyDiff(t *testing.T) {
	initial := advanceProject("plan", modelingapi.ProjectPending, 0)
	body := []byte("patch")
	diff := artifactDescriptor("diff", "plan", body)
	blocked := advanceProject("plan", modelingapi.ProjectBlocked, 2)
	blocked.Artifacts = []modelingapi.ArtifactDescriptor{diff}
	service := &advanceService{
		results: []modelingapi.OperationResult{{
			Project: blocked, Operation: "plan", Status: modelingapi.OperationBlocked,
			Artifacts: []modelingapi.ArtifactDescriptor{diff}, Blocked: true, Reason: "awaiting_apply",
		}},
		content: modelingapi.ArtifactContent{
			Artifact: diff, Data: body, Offset: 0, Next: int64(len(body)), EOF: true,
		},
	}
	controller := &Controller{modeling: service, binding: &advanceStore{}, maxOperations: 2, artifactReadLimit: 64}

	presentation, err := controller.advanceUntilBoundary(
		context.Background(), validStartCall(), Binding{Instruction: "Model it."}, initial, advanceCapabilities(),
	)
	if err != nil {
		t.Fatalf("advanceUntilBoundary() error = %v", err)
	}
	if presentation.State != StateAwaitingApply || presentation.Content == nil {
		t.Fatalf("presentation = %#v", presentation)
	}
	if service.artifactReads != 1 {
		t.Fatalf("ReadArtifact calls = %d, want 1", service.artifactReads)
	}
	if len(presentation.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v, want one de-duplicated descriptor", presentation.Artifacts)
	}
}

func TestAdvanceUntilBoundaryRejectsNonIncreasingRevision(t *testing.T) {
	initial := advanceProject("plan", modelingapi.ProjectPending, 2)
	unchanged := advanceProject("infer", modelingapi.ProjectRunning, 2)
	service := &advanceService{results: []modelingapi.OperationResult{{
		Project: unchanged, Operation: "plan", Status: modelingapi.OperationSucceeded,
	}}}
	controller := &Controller{modeling: service, binding: &advanceStore{}, maxOperations: 1}

	if _, err := controller.advanceUntilBoundary(
		context.Background(), validStartCall(), Binding{Instruction: "Model it."}, initial, advanceCapabilities(),
	); err == nil {
		t.Fatal("advanceUntilBoundary() error = nil, want revision validation error")
	}
}

func advanceProject(
	operation modelingapi.OperationName,
	status modelingapi.ProjectStatus,
	revision int,
) modelingapi.ProjectView {
	return modelingapi.ProjectView{
		ID:               "mp-0123456789abcdef",
		Title:            "UART model",
		Revision:         revision,
		Status:           status,
		CurrentOperation: operation,
		CreatedAt:        time.Unix(1, 0),
		UpdatedAt:        time.Unix(int64(revision+1), 0),
	}
}

func advanceCapabilities() modelingapi.Capabilities {
	return modelingapi.Capabilities{
		APIVersion:    "v1",
		EngineName:    "test-engine",
		EngineVersion: "v1",
		Operations: []modelingapi.OperationDescriptor{
			{Name: "infer", Mutating: true},
			{Name: "plan", Mutating: true},
		},
	}
}
