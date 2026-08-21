package modelingapp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type fakeEngine struct {
	view     pipelineapi.EngineView
	executed pipelineapi.ExecuteRequest
}

func (f *fakeEngine) Describe(context.Context, pipelineapi.DescribeRequest) (pipelineapi.Description, error) {
	return pipelineapi.Description{EngineName: "current-pipeline", EngineVersion: "1.0", APIVersion: "v1", Operations: []pipelineapi.OperationDescriptor{{Name: "plan"}}, ArtifactKinds: []string{"plan"}}, nil
}
func (f *fakeEngine) Inspect(context.Context, pipelineapi.InspectRequest) (pipelineapi.EngineView, error) {
	return f.view, nil
}
func (f *fakeEngine) Execute(_ context.Context, req pipelineapi.ExecuteRequest) (pipelineapi.ExecuteResult, error) {
	f.executed = req
	return pipelineapi.ExecuteResult{Project: f.view, Operation: req.Operation, Status: pipelineapi.OpSucceeded}, nil
}

type fakeQuery struct{}

func (fakeQuery) List(context.Context, pipelineapi.ListQuery) (pipelineapi.ProjectPage, error) {
	return pipelineapi.ProjectPage{}, nil
}
func (fakeQuery) ReadArtifact(context.Context, pipelineapi.ArtifactQuery) (pipelineapi.ArtifactContent, error) {
	return pipelineapi.ArtifactContent{}, errors.New("unused")
}
func (fakeQuery) Evidence(context.Context, pipelineapi.EvidenceQuery) (pipelineapi.ArtifactPage, error) {
	return pipelineapi.ArtifactPage{}, nil
}

type fakeRepository struct{}

func (fakeRepository) CreateProject(context.Context, pipelineapi.ProjectRecord) (pipelineapi.ProjectRecord, error) {
	return pipelineapi.ProjectRecord{}, errors.New("unused")
}
func (fakeRepository) GetProject(context.Context, pipelineapi.ProjectID, pipelineapi.Scope) (pipelineapi.ProjectRecord, error) {
	return pipelineapi.ProjectRecord{}, errors.New("unused")
}
func (fakeRepository) ListProjects(context.Context, pipelineapi.ProjectQuery) ([]pipelineapi.ProjectRecord, error) {
	return nil, errors.New("unused")
}
func (fakeRepository) CompareAndSaveProject(context.Context, pipelineapi.ProjectRecord, int) (pipelineapi.ProjectRecord, error) {
	return pipelineapi.ProjectRecord{}, errors.New("unused")
}
func (fakeRepository) StageArtifacts(context.Context, pipelineapi.ProjectID, []pipelineapi.ArtifactDraft) (pipelineapi.Batch, error) {
	return nil, errors.New("unused")
}
func (fakeRepository) CommitArtifacts(context.Context, pipelineapi.Batch) ([]pipelineapi.ArtifactDescriptor, error) {
	return nil, errors.New("unused")
}
func (fakeRepository) AbortArtifacts(context.Context, pipelineapi.Batch) error {
	return errors.New("unused")
}
func (fakeRepository) OpenArtifact(context.Context, pipelineapi.ProjectID, pipelineapi.ArtifactID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

type fakeCompletion struct{}

func (fakeCompletion) Complete(context.Context, pipelineapi.CompletionRequest) (pipelineapi.CompletionResult, error) {
	return pipelineapi.CompletionResult{}, nil
}

type fakeEffect struct{}

func (fakeEffect) Invoke(context.Context, pipelineapi.EffectRequest) (pipelineapi.EffectResult, error) {
	return pipelineapi.EffectResult{}, nil
}

type fakePublisher struct{}

func (fakePublisher) Publish(context.Context, pipelineapi.Event) error { return nil }

func testCall() modelingapi.CallContext {
	return modelingapi.CallContext{RequestID: "req-1", TraceID: "trace-1", WorkspaceID: "workspace-1", Channel: "cli", IdempotencyKey: "key-1"}
}
func testView() pipelineapi.EngineView {
	now := time.Unix(10, 0)
	return pipelineapi.EngineView{ProjectID: "mp-0123456789abcdef", Title: "demo", Revision: 3, Status: pipelineapi.StatusPending, CurrentOperation: "plan", CreatedAt: now, UpdatedAt: now}
}
func testPorts() pipelineapi.RuntimePorts {
	return pipelineapi.RuntimePorts{Repository: fakeRepository{}, Completion: fakeCompletion{}, Effect: fakeEffect{}, Event: fakePublisher{}}
}

func TestNewServiceValidatesDependencies(t *testing.T) {
	if _, err := NewService(Dependencies{}); err == nil {
		t.Fatal("expected invalid dependencies")
	}
	if _, err := NewService(Dependencies{RuntimePorts: testPorts(), QueryPort: fakeQuery{}}); err == nil {
		t.Fatal("expected missing engine")
	}
}

func TestShowProjectsLatestView(t *testing.T) {
	engine := &fakeEngine{view: testView()}
	service, err := NewService(Dependencies{RuntimePorts: testPorts(), QueryPort: fakeQuery{}, Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Show(context.Background(), testCall(), modelingapi.ShowRequest{ProjectID: "mp-0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 3 || view.ID != "mp-0123456789abcdef" {
		t.Fatalf("unexpected view: %#v", view)
	}
}

func TestAdvanceUsesInspectedSnapshotAndCurrentOperation(t *testing.T) {
	engine := &fakeEngine{view: testView()}
	service, err := NewService(Dependencies{RuntimePorts: testPorts(), QueryPort: fakeQuery{}, Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Advance(context.Background(), testCall(), modelingapi.AdvanceRequest{ProjectID: "mp-0123456789abcdef", ExpectedRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	if engine.executed.Project.Revision != 3 || engine.executed.Operation != "plan" {
		t.Fatalf("unexpected execute request: %#v", engine.executed)
	}
}

func TestUnsupportedApplyReturnsPublicUnavailable(t *testing.T) {
	service, err := NewService(Dependencies{RuntimePorts: testPorts(), QueryPort: fakeQuery{}, Engine: &fakeEngine{view: testView()}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Apply(context.Background(), testCall(), modelingapi.ApplyRequest{ProjectID: "mp-0123456789abcdef", PreviewID: "preview", ApprovalToken: "approved", ExpectedRevision: 3})
	var public *modelingapi.Error
	if !errors.As(err, &public) || public.Public.Code != modelingapi.ErrorUnavailable {
		t.Fatalf("unexpected error: %v", err)
	}
}
