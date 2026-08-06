package modeling

import (
	"testing"
	"time"
)

func projAt(status Status, current Stage, id string) Project {
	return Project{
		ID:          id,
		Title:       "t",
		WorkspaceID: "ws",
		Current:     current,
		Status:      status,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestCanAdvanceTransitions(t *testing.T) {
	cases := []struct {
		name    string
		proj    Project
		to      Stage
		wantErr bool
	}{
		{"running rejects any", projAt(StatusRunning, StagePlan, "mp-aaa"), StagePlan, true},
		{"redo pending", projAt(StatusPending, StagePlan, "mp-aaa"), StagePlan, false},
		{"redo blocked", projAt(StatusBlocked, StagePlan, "mp-aaa"), StagePlan, false},
		{"redo done", projAt(StatusDone, StagePlan, "mp-aaa"), StagePlan, false},
		{"next from pending rejected", projAt(StatusPending, StagePlan, "mp-aaa"), StageExtract, true},
		{"next from done ok", projAt(StatusDone, StagePlan, "mp-aaa"), StageExtract, false},
		{"skip two rejected", projAt(StatusDone, StagePlan, "mp-aaa"), StageInfer, true},
		{"back rejected", projAt(StatusDone, StageExtract, "mp-aaa"), StagePlan, true},
		{"unknown stage rejected", projAt(StatusPending, StagePlan, "mp-aaa"), Stage("nope"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.proj.CanAdvance(c.to)
			if c.wantErr && err == nil {
				t.Errorf("CanAdvance(%q): want error, got nil", c.to)
			}
			if !c.wantErr && err != nil {
				t.Errorf("CanAdvance(%q): unexpected err %v", c.to, err)
			}
		})
	}
}

func TestBeginSideEffects(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := projAt(StatusPending, StagePlan, "mp-aaa")
	p.LastError = "timeout"

	err := p.Begin(StagePlan, now)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if p.Current != StagePlan {
		t.Errorf("Current = %q, want plan", p.Current)
	}
	if p.Status != StatusRunning {
		t.Errorf("Status = %q, want running", p.Status)
	}
	if p.LastError != "" {
		t.Errorf("LastError = %q, want empty", p.LastError)
	}
	if !p.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", p.UpdatedAt, now)
	}
}

func TestBeginRejectsRunning(t *testing.T) {
	p := projAt(StatusRunning, StagePlan, "mp-aaa")
	if err := p.Begin(StagePlan, time.Now()); err == nil {
		t.Error("Begin on running project: want error, got nil")
	}
}

func TestBeginRejectsZeroTime(t *testing.T) {
	p := projAt(StatusPending, StagePlan, "mp-aaa")
	if err := p.Begin(StagePlan, time.Time{}); err == nil {
		t.Error("Begin with zero time: want error, got nil")
	}
}

func TestCompleteAdvancesToNext(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := projAt(StatusRunning, StagePlan, "mp-aaa")
	refs := []ArtifactRef{validRef(StagePlan, "plan.md")}

	err := p.Complete(StagePlan, refs, now)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if p.Current != StageExtract {
		t.Errorf("Current = %q, want extract", p.Current)
	}
	if p.Status != StatusPending {
		t.Errorf("Status = %q, want pending", p.Status)
	}
	if len(p.Artifacts[StagePlan]) != 1 {
		t.Errorf("Artifacts[plan] len = %d, want 1", len(p.Artifacts[StagePlan]))
	}
}

func TestCompleteLastStageSetsDone(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := projAt(StatusRunning, StageVerify, "mp-aaa")
	refs := []ArtifactRef{validRef(StageVerify, "qtest.log")}

	err := p.Complete(StageVerify, refs, now)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if p.Current != StageVerify {
		t.Errorf("Current = %q, want verify (stays)", p.Current)
	}
	if p.Status != StatusDone {
		t.Errorf("Status = %q, want done", p.Status)
	}
}

func validRef(stage Stage, name string) ArtifactRef {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return ArtifactRef{
		ID:      digest[:16],
		Stage:   stage,
		Name:    name,
		Kind:    KindPlan,
		Bytes:   1,
		Digest:  digest,
		Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}
