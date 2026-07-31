package modeling

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func at(seconds int64) time.Time { return time.Unix(seconds, 0).UTC() }

// ref builds a valid artifact reference whose id addresses its digest, so tests
// only have to break the field they are about.
func ref(stage Stage, name string, kind Kind, seed byte) ArtifactRef {
	digest := strings.Repeat(string("0123456789abcdef"[seed%16]), 64)
	return ArtifactRef{
		ID: digest[:16], Stage: stage, Name: name, Kind: kind,
		Bytes: 12, Digest: digest, Created: at(100),
	}
}

func newProject(t *testing.T) Project {
	t.Helper()
	return Project{
		ID: "mp-0123456789abcdef", Title: "pl011 clone", WorkspaceID: "ws-00112233",
		Current: FirstStage(), Status: StatusPending, CreatedAt: at(1), UpdatedAt: at(1),
	}
}

func TestCloneIsDeep(t *testing.T) {
	project := newProject(t)
	project.Artifacts = map[Stage][]ArtifactRef{StagePlan: {ref(StagePlan, "plan.md", KindPlan, 1)}}
	project.Evidence = []ArtifactRef{ref(StageVerify, "build.log", KindEvidence, 2)}

	clone := project.Clone()
	clone.Artifacts[StagePlan][0].Name = "other.md"
	clone.Artifacts[StageExtract] = []ArtifactRef{ref(StageExtract, "reg-ir.json", KindRegIR, 3)}
	clone.Evidence[0].Name = "elsewhere.log"

	if project.Artifacts[StagePlan][0].Name != "plan.md" {
		t.Fatalf("artifact leaked: %#v", project.Artifacts[StagePlan])
	}
	if _, exists := project.Artifacts[StageExtract]; exists {
		t.Fatal("the clone's new stage appeared in the original")
	}
	if project.Evidence[0].Name != "build.log" {
		t.Fatalf("evidence leaked: %#v", project.Evidence)
	}
}

func TestAdvanceFollowsStageOrder(t *testing.T) {
	project := newProject(t)
	// A redo of the current stage is always allowed; that is how a blocked stage
	// is retried without creating a second project.
	if err := project.CanAdvance(StagePlan); err != nil {
		t.Fatalf("redo of the current stage rejected: %v", err)
	}
	// Moving on before the current stage finished would leave a gap in the chain.
	if err := project.CanAdvance(StageExtract); err == nil {
		t.Fatal("advanced out of a pending stage")
	}
	if err := project.CanAdvance(StageEmit); err == nil {
		t.Fatal("skipped two stages")
	}

	if err := project.Begin(StagePlan, at(10)); err != nil {
		t.Fatal(err)
	}
	if project.Status != StatusRunning {
		t.Fatalf("status = %q", project.Status)
	}
	// Running is exclusive: a second advance while a stage runs would race two
	// writers into the same staging area.
	if err := project.CanAdvance(StagePlan); err == nil {
		t.Fatal("advanced while a stage was running")
	}
	if err := project.Complete(StagePlan, []ArtifactRef{ref(StagePlan, "plan.md", KindPlan, 1)}, at(20)); err != nil {
		t.Fatal(err)
	}
	if project.Current != StageExtract || project.Status != StatusPending {
		t.Fatalf("project = %q/%q", project.Current, project.Status)
	}
	if err := project.CanAdvance(StagePlan); err == nil {
		t.Fatal("moved backwards without a reset")
	}
}

func TestCompleteOnTheLastStageFinishesTheProject(t *testing.T) {
	project := newProject(t)
	project.Current, project.Status = StageVerify, StatusPending
	if err := project.Begin(StageVerify, at(10)); err != nil {
		t.Fatal(err)
	}
	refs := []ArtifactRef{
		ref(StageVerify, "build.log", KindEvidence, 4),
		ref(StageVerify, "device.diff", KindDiff, 5),
	}
	if err := project.Complete(StageVerify, refs, at(20)); err != nil {
		t.Fatal(err)
	}
	if project.Current != StageVerify || project.Status != StatusDone {
		t.Fatalf("project = %q/%q", project.Current, project.Status)
	}
	// Only evidence-kind artifacts become Evidence: a diff must never be
	// presented as proof that the device builds.
	if len(project.Evidence) != 1 || project.Evidence[0].Name != "build.log" {
		t.Fatalf("evidence = %#v", project.Evidence)
	}
}

func TestResetClearsLaterArtifacts(t *testing.T) {
	project := newProject(t)
	project.Artifacts = map[Stage][]ArtifactRef{
		StagePlan:    {ref(StagePlan, "plan.md", KindPlan, 1)},
		StageExtract: {ref(StageExtract, "reg-ir.json", KindRegIR, 2)},
		StageInfer:   {ref(StageInfer, "reg-ir.json", KindRegIR, 3)},
		StageEmit:    {ref(StageEmit, "device.c", KindCode, 4)},
		StageVerify:  {ref(StageVerify, "build.log", KindEvidence, 5)},
	}
	project.Evidence = []ArtifactRef{ref(StageVerify, "build.log", KindEvidence, 5)}
	project.Current, project.Status = StageVerify, StatusDone

	if err := project.Reset(StageExtract, at(30)); err != nil {
		t.Fatal(err)
	}
	if _, exists := project.Artifacts[StagePlan]; !exists {
		t.Fatal("reset dropped an earlier stage")
	}
	for _, stage := range []Stage{StageExtract, StageInfer, StageEmit, StageVerify} {
		if _, exists := project.Artifacts[stage]; exists {
			t.Fatalf("stage %q survived the reset", stage)
		}
	}
	if project.Evidence != nil {
		t.Fatalf("evidence survived the reset: %#v", project.Evidence)
	}
	if project.Current != StageExtract || project.Status != StatusPending {
		t.Fatalf("project = %q/%q", project.Current, project.Status)
	}
}

// Re-running a stage must replace its refs and invalidate everything downstream:
// code generated from the previous register map is no longer describable as
// output of this project.
func TestCompleteReplacesRefsAndInvalidatesDownstream(t *testing.T) {
	project := newProject(t)
	project.Current, project.Status = StageExtract, StatusPending
	project.Artifacts = map[Stage][]ArtifactRef{
		StagePlan: {ref(StagePlan, "plan.md", KindPlan, 1)},
		StageEmit: {ref(StageEmit, "device.c", KindCode, 4)},
	}
	if err := project.Begin(StageExtract, at(10)); err != nil {
		t.Fatal(err)
	}
	first := ref(StageExtract, "reg-ir.json", KindRegIR, 2)
	if err := project.Complete(StageExtract, []ArtifactRef{first}, at(20)); err != nil {
		t.Fatal(err)
	}
	if _, exists := project.Artifacts[StageEmit]; exists {
		t.Fatal("a re-extraction kept the code generated from the old IR")
	}

	project.Current, project.Status = StageExtract, StatusPending
	if err := project.Begin(StageExtract, at(30)); err != nil {
		t.Fatal(err)
	}
	second := ref(StageExtract, "reg-ir.json", KindRegIR, 7)
	if err := project.Complete(StageExtract, []ArtifactRef{second}, at(40)); err != nil {
		t.Fatal(err)
	}
	refs := project.Artifacts[StageExtract]
	if len(refs) != 1 || refs[0].Digest == first.Digest {
		t.Fatalf("refs = %#v; the same name must resolve to the newest content", refs)
	}
}

func TestCompleteRejectsForeignStageArtifact(t *testing.T) {
	project := newProject(t)
	if err := project.Begin(StagePlan, at(10)); err != nil {
		t.Fatal(err)
	}
	if err := project.Complete(StagePlan, []ArtifactRef{ref(StageEmit, "device.c", KindCode, 1)}, at(20)); err == nil {
		t.Fatal("a stage claimed another stage's artifact")
	}
}

func TestFailAndBlockStoreOnlyACategory(t *testing.T) {
	project := newProject(t)
	if err := project.Begin(StagePlan, at(10)); err != nil {
		t.Fatal(err)
	}
	// The whole point: a wrapped error message must not survive into a field
	// that /modeling show renders and the logger writes.
	if err := project.Fail(StagePlan, "model_failed: 401 from provider at https://example/v1", at(20)); err != nil {
		t.Fatal(err)
	}
	if project.LastError != "model_failed" {
		t.Fatalf("last error = %q", project.LastError)
	}
	if project.Status != StatusBlocked {
		t.Fatalf("status = %q", project.Status)
	}
	if err := project.Validate(); err != nil {
		t.Fatal(err)
	}

	if err := project.Begin(StagePlan, at(30)); err != nil {
		t.Fatal(err)
	}
	if err := project.Block(StagePlan, "awaiting_apply", at(40)); err != nil {
		t.Fatal(err)
	}
	if project.Status != StatusBlocked || project.LastError != "awaiting_apply" {
		t.Fatalf("project = %q/%q", project.Status, project.LastError)
	}
}

func TestHoldKeepsArtifactsAndStaysOnTheStage(t *testing.T) {
	project := newProject(t)
	if err := project.Begin(StagePlan, at(10)); err != nil {
		t.Fatal(err)
	}
	// Hold exists because "blocked" and "produced artifacts" are not mutually
	// exclusive: the emit stage writes a diff and then waits for a human to apply
	// it, and that diff has to be referenced or nobody can review it.
	refs := []ArtifactRef{ref(StagePlan, "plan.md", KindPlan, 1)}
	if err := project.Hold(StagePlan, refs, "awaiting apply: run /modeling apply", at(20)); err != nil {
		t.Fatal(err)
	}
	if project.Current != StagePlan || project.Status != StatusBlocked {
		t.Fatalf("project = %q/%q", project.Current, project.Status)
	}
	if len(project.Artifacts[StagePlan]) != 1 {
		t.Fatalf("artifacts = %#v", project.Artifacts)
	}
	// The reason is shortened the same way Fail shortens a category, so a held
	// project cannot smuggle a message into LastError either.
	if project.LastError != "awaiting" {
		t.Fatalf("last error = %q", project.LastError)
	}
	if err := project.Validate(); err != nil {
		t.Fatal(err)
	}
	// A redo is still legal after a hold, which is how /modeling advance retries.
	if err := project.CanAdvance(StagePlan); err != nil {
		t.Fatal(err)
	}
	// And Hold is only reachable from a running stage: it is a stage outcome, not
	// a state a command may set.
	if err := project.Hold(StagePlan, refs, "awaiting_apply", at(30)); err == nil {
		t.Fatal("held a stage that was not running")
	}
}

func TestFailRejectsAStageThatIsNotRunning(t *testing.T) {
	project := newProject(t)
	if err := project.Fail(StagePlan, "model_failed", at(20)); err == nil {
		t.Fatal("failed a stage that never started")
	}
}

func TestCanReplaceFromRejectsStaleRevision(t *testing.T) {
	old := newProject(t)
	old.Revision = 4

	next := old.Clone()
	next.Revision = 5
	if err := next.CanReplaceFrom(old); err != nil {
		t.Fatalf("a one-step revision bump was rejected: %v", err)
	}
	for _, broken := range []func(*Project){
		func(p *Project) { p.Revision = 4 },
		func(p *Project) { p.Revision = 6 },
		func(p *Project) { p.ID = "mp-ffffffffffffffff" },
		func(p *Project) { p.WorkspaceID = "ws-99887766" },
		func(p *Project) { p.UserID = "someone" },
		func(p *Project) { p.CreatedAt = at(2) },
	} {
		candidate := old.Clone()
		candidate.Revision = 5
		broken(&candidate)
		err := candidate.CanReplaceFrom(old)
		if err == nil {
			t.Fatalf("accepted %#v", candidate)
		}
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v; a caller must be able to retry on conflict", err)
		}
	}
}

func TestValidateRejectsUnsafeArtifactName(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "a/b", "..", "", ".hidden", "name with space"} {
		project := newProject(t)
		bad := ref(StagePlan, "plan.md", KindPlan, 1)
		bad.Name = name
		project.Artifacts = map[Stage][]ArtifactRef{StagePlan: {bad}}
		if err := project.Validate(); err == nil {
			t.Fatalf("accepted artifact name %q", name)
		}
	}
}

func TestValidateRejectsBrokenState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Project)
	}{
		{name: "bad id", mutate: func(p *Project) { p.ID = "project-1" }},
		{name: "empty title", mutate: func(p *Project) { p.Title = " " }},
		{name: "no workspace", mutate: func(p *Project) { p.WorkspaceID = "" }},
		{name: "unknown stage", mutate: func(p *Project) { p.Current = "polish" }},
		{name: "unknown status", mutate: func(p *Project) { p.Status = "waiting" }},
		{name: "negative revision", mutate: func(p *Project) { p.Revision = -1 }},
		{name: "no timestamps", mutate: func(p *Project) { p.UpdatedAt = time.Time{} }},
		{
			name: "unknown artifact stage",
			mutate: func(p *Project) {
				p.Artifacts = map[Stage][]ArtifactRef{"polish": {ref("polish", "x.md", KindPlan, 1)}}
			},
		},
		{
			name: "misfiled artifact",
			mutate: func(p *Project) {
				p.Artifacts = map[Stage][]ArtifactRef{StagePlan: {ref(StageEmit, "device.c", KindCode, 1)}}
			},
		},
		{
			name: "id does not address content",
			mutate: func(p *Project) {
				bad := ref(StagePlan, "plan.md", KindPlan, 1)
				bad.ID = "0000000000000000"
				bad.Digest = strings.Repeat("a", 64)
				p.Artifacts = map[Stage][]ArtifactRef{StagePlan: {bad}}
			},
		},
		{
			name:   "non evidence in evidence list",
			mutate: func(p *Project) { p.Evidence = []ArtifactRef{ref(StageEmit, "device.c", KindCode, 1)} },
		},
		{
			name:   "raw error text",
			mutate: func(p *Project) { p.LastError = "model failed: 401 from provider" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := newProject(t)
			test.mutate(&project)
			if err := project.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestValidateAcceptsAFullProject(t *testing.T) {
	project := newProject(t)
	project.Current, project.Status = StageVerify, StatusDone
	project.Artifacts = map[Stage][]ArtifactRef{
		StagePlan:   {ref(StagePlan, "plan.md", KindPlan, 1)},
		StageVerify: {ref(StageVerify, "build.log", KindEvidence, 2)},
	}
	project.Evidence = []ArtifactRef{ref(StageVerify, "build.log", KindEvidence, 2)}
	project.LastError = "build_failed"
	if err := project.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// The id is rendered in listings and becomes a file name, so it must not carry
// the title or any part of the operator's layout.
func TestProjectIDIsOpaque(t *testing.T) {
	project := newProject(t)
	for _, fragment := range []string{"pl011", "clone", "/"} {
		if strings.Contains(project.ID, fragment) {
			t.Fatalf("project id %q leaks %q", project.ID, fragment)
		}
	}
	if err := ValidateProjectID("mp-0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	// A malformed id and an unknown id must be indistinguishable.
	for _, bad := range []string{"", "mp-", "mp-XYZ", "mp-0123456789abcdeff", "../mp-0123456789abcdef"} {
		if err := ValidateProjectID(bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ValidateProjectID(%q) = %v", bad, err)
		}
	}
}

func TestStageOrderIsTheOnlyOrdering(t *testing.T) {
	if got := FirstStage(); got != StagePlan {
		t.Fatalf("first stage = %q", got)
	}
	if got := LastStage(); got != StageVerify {
		t.Fatalf("last stage = %q", got)
	}
	for index, stage := range StageOrder {
		next, ok := NextStage(stage)
		if index == len(StageOrder)-1 {
			if ok {
				t.Fatalf("the last stage has a successor %q", next)
			}
			continue
		}
		if !ok || next != StageOrder[index+1] {
			t.Fatalf("NextStage(%q) = %q, %v", stage, next, ok)
		}
	}
	if _, err := ParseStage(" EXTRACT "); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseStage("polish"); err == nil {
		t.Fatal("ParseStage invented a stage")
	}
}
