package current

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type queryFake struct {
	project modeling.Project
	body    []byte
}

func (f *queryFake) List(context.Context, modeling.Query) ([]modeling.Project, error) {
	return []modeling.Project{f.project}, nil
}

func (f *queryFake) Show(_ context.Context, _ string, scope modeling.Scope) (modeling.Project, error) {
	if scope.WorkspaceID != f.project.WorkspaceID {
		return modeling.Project{}, modeling.ErrNotFound
	}
	return f.project, nil
}
func (f *queryFake) Read(context.Context, string, modeling.ArtifactRef, modeling.Scope) ([]byte, error) {
	return append([]byte(nil), f.body...), nil
}

func queryProject() (modeling.Project, modeling.ArtifactRef) {
	ref := modeling.ArtifactRef{ID: "artifact-1", Stage: modeling.StagePlan, Name: "plan.md", Kind: modeling.KindPlan, Bytes: 10, Digest: "digest", Created: time.Unix(1, 0)}
	return modeling.Project{ID: "project-1", WorkspaceID: "workspace-1", Artifacts: map[modeling.Stage][]modeling.ArtifactRef{modeling.StagePlan: {ref}}, Evidence: []modeling.ArtifactRef{ref}}, ref
}

func TestQueryListAndEvidenceContracts(t *testing.T) {
	project, _ := queryProject()
	adapter, err := NewQueryAdapter(&queryFake{project: project, body: []byte("0123456789")})
	if err != nil {
		t.Fatal(err)
	}
	page, err := adapter.List(context.Background(), pipelineapi.ListQuery{Scope: pipelineapi.Scope{WorkspaceID: "workspace-1"}, Limit: 1})
	if err != nil || len(page.Projects) != 1 {
		t.Fatalf("List = %#v, %v", page, err)
	}
	evidence, err := adapter.Evidence(context.Background(), pipelineapi.EvidenceQuery{Scope: pipelineapi.Scope{WorkspaceID: "workspace-1"}, ProjectID: "project-1", Limit: 1})
	if err != nil || len(evidence.Artifacts) != 1 {
		t.Fatalf("Evidence = %#v, %v", evidence, err)
	}
}

func TestReadArtifactRangeAndOwnership(t *testing.T) {
	project, ref := queryProject()
	adapter, _ := NewQueryAdapter(&queryFake{project: project, body: []byte("0123456789")})
	content, err := adapter.ReadArtifact(context.Background(), pipelineapi.ArtifactQuery{Scope: pipelineapi.Scope{WorkspaceID: "workspace-1"}, ProjectID: "project-1", ArtifactID: pipelineapi.ArtifactID(ref.ID), Offset: 3, Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if string(content.Data) != "3456" || content.Next != 7 || content.EOF {
		t.Fatalf("content = %#v", content)
	}
	_, err = adapter.ReadArtifact(context.Background(), pipelineapi.ArtifactQuery{Scope: pipelineapi.Scope{WorkspaceID: "other"}, ProjectID: "project-1", ArtifactID: pipelineapi.ArtifactID(ref.ID), Limit: 4})
	if !errors.Is(err, modeling.ErrNotFound) {
		t.Fatalf("wrong scope error = %v, want not found-compatible error", err)
	}
}

func TestQueryErrorPreservesCauseAndCategory(t *testing.T) {
	wrapped := mapCurrentError(modeling.ErrNotFound)
	if !errors.Is(wrapped, modeling.ErrNotFound) || currentErrorCategory(wrapped) != "not_found" {
		t.Fatalf("mapped error = %v, category = %q", wrapped, currentErrorCategory(wrapped))
	}
}
