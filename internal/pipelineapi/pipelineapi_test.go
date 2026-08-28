package pipelineapi

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestScopeValidate(t *testing.T) {
	tests := []struct {
		name    string
		scope   Scope
		wantErr bool
	}{
		{name: "workspace", scope: Scope{WorkspaceID: "ws-1"}},
		{name: "owner", scope: Scope{WorkspaceID: "ws-1", UserID: "alice"}},
		{name: "missing workspace", scope: Scope{}, wantErr: true},
		{name: "noncanonical", scope: Scope{WorkspaceID: " ws-1"}, wantErr: true},
		{name: "control character", scope: Scope{WorkspaceID: "ws\n1"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.scope.Validate(); (got != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", got, test.wantErr)
			}
		})
	}
}

func TestExecuteRequestValidate(t *testing.T) {
	valid := ExecuteRequest{
		Project: ProjectSnapshot{
			ID:       "mp-0123456789abcdef",
			Scope:    Scope{WorkspaceID: "ws-1", UserID: "alice"},
			Revision: 3,
		},
		Operation:        "plan",
		ExpectedRevision: 3,
		Invocation: InvocationContext{
			RequestID: "req-1",
			TraceID:   "trace-1",
			Channel:   "test",
		},
		Sources: []SourceRef{{Kind: "workspace_path", Value: "references/device.pdf"}},
		Ports: RuntimePorts{
			Repository: repositoryStub{},
			Completion: completionStub{},
			Effect:     effectStub{},
			Event:      eventStub{},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ExecuteRequest)
	}{
		{name: "unknown operation", mutate: func(r *ExecuteRequest) { r.Operation = "PLAN" }},
		{name: "revision mismatch", mutate: func(r *ExecuteRequest) { r.ExpectedRevision = 2 }},
		{name: "missing invocation", mutate: func(r *ExecuteRequest) { r.Invocation.RequestID = "" }},
		{name: "invalid source", mutate: func(r *ExecuteRequest) { r.Sources[0].Value = "" }},
		{name: "missing port", mutate: func(r *ExecuteRequest) { r.Ports.Event = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid.Clone()
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want error")
			}
		})
	}
}

func TestCloneIsolation(t *testing.T) {
	request := ExecuteRequest{Sources: []SourceRef{{Kind: "workspace_path", Value: "a"}}}
	requestClone := request.Clone()
	requestClone.Sources[0].Value = "b"
	if request.Sources[0].Value != "a" {
		t.Fatal("ExecuteRequest.Clone shared the Sources slice")
	}

	result := ExecuteResult{
		Project: EngineView{
			Recommended: []OperationDescriptor{{Name: "plan"}},
			Artifacts:   []ArtifactDescriptor{{ID: "artifact-1"}},
		},
		Artifacts: []ArtifactDescriptor{{ID: "artifact-2"}},
		Evidence:  []ArtifactDescriptor{{ID: "artifact-3"}},
		Apply: &ApplyOutcome{
			Written:  []string{"hw/device.c"},
			Skipped:  []string{"hw/meson.build"},
			Evidence: []ArtifactDescriptor{{ID: "artifact-4"}},
		},
	}
	resultClone := result.Clone()
	resultClone.Project.Recommended[0].Name = "emit"
	resultClone.Project.Artifacts[0].ID = "changed"
	resultClone.Artifacts[0].ID = "changed"
	resultClone.Evidence[0].ID = "changed"
	resultClone.Apply.Written[0] = "changed"
	resultClone.Apply.Skipped[0] = "changed"
	resultClone.Apply.Evidence[0].ID = "changed"
	if result.Project.Recommended[0].Name != "plan" ||
		result.Project.Artifacts[0].ID != "artifact-1" ||
		result.Artifacts[0].ID != "artifact-2" ||
		result.Evidence[0].ID != "artifact-3" ||
		result.Apply.Written[0] != "hw/device.c" ||
		result.Apply.Skipped[0] != "hw/meson.build" ||
		result.Apply.Evidence[0].ID != "artifact-4" {
		t.Fatal("ExecuteResult.Clone shared mutable slices")
	}

	content := ArtifactContent{Data: []byte("body")}
	contentClone := content.Clone()
	contentClone.Data[0] = 'B'
	if string(content.Data) != "body" {
		t.Fatal("ArtifactContent.Clone shared the data slice")
	}
}

func TestValidatePorts(t *testing.T) {
	ports := RuntimePorts{
		Repository: repositoryStub{},
		Completion: completionStub{},
		Effect:     effectStub{},
		Event:      eventStub{},
	}
	if err := ValidatePorts(ports); err != nil {
		t.Fatalf("ValidatePorts(valid) = %v", err)
	}
	ports.Repository = nil
	var missing ErrMissingPort
	if err := ValidatePorts(ports); !errors.As(err, &missing) || missing != ErrMissingPort(PortRepository) {
		t.Fatalf("ValidatePorts() = %v, want repository ErrMissingPort", err)
	}
}

func TestPortErrorUnwrap(t *testing.T) {
	cause := errors.New("store unavailable")
	err := NewPortError(PortRepository, "get project", cause)
	if !errors.Is(err, cause) {
		t.Fatal("PortError does not unwrap its cause")
	}
	if !strings.Contains(err.Error(), "repository") {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestErrorCategoryUsesTypedErrors(t *testing.T) {
	if got := ErrorCategory(ErrMissingPort(PortEvent)); got != "unavailable" {
		t.Fatalf("missing port category = %q", got)
	}
	cause := errors.New("provider endpoint and token")
	if got := ErrorCategory(NewPortError(PortCompletion, "complete", cause)); got != "unavailable" {
		t.Fatalf("port error category = %q", got)
	}
}

type repositoryStub struct{}

func (repositoryStub) CreateProject(context.Context, ProjectRecord) (ProjectRecord, error) {
	return ProjectRecord{}, nil
}
func (repositoryStub) GetProject(context.Context, ProjectID, Scope) (ProjectRecord, error) {
	return ProjectRecord{}, nil
}
func (repositoryStub) ListProjects(context.Context, ProjectQuery) ([]ProjectRecord, error) {
	return nil, nil
}
func (repositoryStub) CompareAndSaveProject(context.Context, ProjectRecord, int) (ProjectRecord, error) {
	return ProjectRecord{}, nil
}
func (repositoryStub) StageArtifacts(context.Context, ProjectID, []ArtifactDraft) (Batch, error) {
	return batchStub{}, nil
}
func (repositoryStub) CommitArtifacts(context.Context, Batch) ([]ArtifactDescriptor, error) {
	return nil, nil
}
func (repositoryStub) AbortArtifacts(context.Context, Batch) error { return nil }
func (repositoryStub) OpenArtifact(context.Context, ProjectID, ArtifactID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

type batchStub struct{}

func (batchStub) IsOpaque() {}

type completionStub struct{}

func (completionStub) Complete(context.Context, CompletionRequest) (CompletionResult, error) {
	return CompletionResult{}, nil
}

type effectStub struct{}

func (effectStub) Invoke(context.Context, EffectRequest) (EffectResult, error) {
	return EffectResult{}, nil
}

type eventStub struct{}

func (eventStub) Publish(context.Context, Event) error { return nil }
