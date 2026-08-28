package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
)

func TestProjectConversionPreservesDurableFields(t *testing.T) {
	now := time.Unix(10, 0)
	p := modeling.Project{ID: "mp-0123456789abcdef", Title: "demo", WorkspaceID: "w", UserID: "u", Current: modeling.StageInfer, Status: modeling.StatusBlocked, Revision: 4, LastError: "schema_invalid", CreatedAt: now, UpdatedAt: now.Add(time.Minute), Artifacts: map[modeling.Stage][]modeling.ArtifactRef{modeling.StagePlan: {{ID: "a", Stage: modeling.StagePlan, Name: "plan", Kind: modeling.KindPlan, Bytes: 3, Digest: "d", Created: now}}}, Evidence: []modeling.ArtifactRef{{ID: "e", Stage: modeling.StageInfer, Name: "raw", Kind: modeling.KindEvidence, Bytes: 2, Digest: "ed", Created: now}}}
	r := projectToRecord(p)
	if len(r.Artifacts) != 1 || len(r.Evidence) != 1 || r.LastError != p.LastError {
		t.Fatalf("conversion lost fields: %#v", r)
	}
	back := recordToProject(r)
	if back.ID != p.ID || back.WorkspaceID != p.WorkspaceID || back.UserID != p.UserID || back.Revision != p.Revision || len(back.Artifacts[modeling.StagePlan]) != 1 || len(back.Evidence) != 1 {
		t.Fatalf("round trip mismatch: %#v", back)
	}
}

func TestMapCurrentErrorPreservesCause(t *testing.T) {
	err := modeling.ErrNotFound
	mapped := mapCurrentError(err)
	if !errors.Is(mapped, err) {
		t.Fatal("cause was not preserved")
	}
	if mapped.Error() != "runtime adapter: not_found" {
		t.Fatalf("unexpected error: %v", mapped)
	}
}

func TestBuildRuntimePortsRejectsNil(t *testing.T) {
	if _, err := BuildRuntimePorts(PortDependencies{}); err == nil {
		t.Fatal("expected missing dependency error")
	}
}
