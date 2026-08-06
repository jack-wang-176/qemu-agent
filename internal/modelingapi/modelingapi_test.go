package modelingapi

// modelingapi_test.go contains the A1 contract tests.
//
// These tests do not implement Service; that belongs to A5 modelingapp. They cover
// CallContext, identifiers, requests, views, events, capabilities, defensive copies,
// and compile-time consumer completeness.

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validArtifact(kind string) ArtifactDescriptor {
	return ArtifactDescriptor{
		ID:        "0123456789abcdef",
		Operation: "plan",
		Name:      "plan.md",
		Kind:      kind,
		Bytes:     4,
		Digest:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CreatedAt: time.Unix(1, 0).UTC(),
	}
}

func TestCallContextValidate(t *testing.T) {
	base := CallContext{
		RequestID:      "req-001",
		TraceID:        "trace-001",
		WorkspaceID:    "ws-001",
		Channel:        "cli",
		IdempotencyKey: "idem-001",
	}
	if err := base.Validate(ReadOnly); err != nil {
		t.Fatalf("base readonly should pass: %v", err)
	}
	if err := base.Validate(Mutating); err != nil {
		t.Fatalf("base mutating should pass: %v", err)
	}

	missingIdem := base
	missingIdem.IdempotencyKey = ""
	if err := missingIdem.Validate(Mutating); err == nil {
		t.Fatal("mutating without idempotency should fail")
	}
	if err := missingIdem.Validate(ReadOnly); err != nil {
		t.Fatalf("readonly without idempotency should pass: %v", err)
	}

	missingReq := base
	missingReq.RequestID = ""
	if err := missingReq.Validate(ReadOnly); err == nil {
		t.Fatal("missing request_id should fail")
	}

	withControl := base
	withControl.RequestID = "req\x00bad"
	if err := withControl.Validate(ReadOnly); err == nil {
		t.Fatal("control char should fail")
	}

	tooLong := base
	tooLong.WorkspaceID = strings.Repeat("a", MaxIDBytes+1)
	if err := tooLong.Validate(ReadOnly); err == nil {
		t.Fatal("too long should fail")
	}
	whitespace := base
	whitespace.RequestID = " req-001 "
	if err := whitespace.Validate(ReadOnly); err == nil {
		t.Fatal("non-canonical identifier should fail")
	}
	if err := base.Validate(MutationKind(99)); err == nil {
		t.Fatal("unknown mutation kind should fail")
	}
}

func TestParseProjectID(t *testing.T) {
	valid := "mp-" + "0123456789abcdef"
	if _, err := ParseProjectID(valid); err != nil {
		t.Fatalf("valid id should pass: %v", err)
	}
	if _, err := ParseProjectID(""); err == nil {
		t.Fatal("empty should fail")
	}
	if _, err := ParseProjectID("ws-0123456789abcdef"); err == nil {
		t.Fatal("wrong prefix should fail")
	}
	if _, err := ParseProjectID("mp-short"); err == nil {
		t.Fatal("wrong length should fail")
	}
	if _, err := ParseProjectID("mp-0123456789ABCDEFFF"); err == nil {
		t.Fatal("uppercase hex should fail")
	}
}

func TestParseArtifactID(t *testing.T) {
	if _, err := ParseArtifactID("0123456789abcdef"); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	if _, err := ParseArtifactID(""); err == nil {
		t.Fatal("empty should fail")
	}
	if _, err := ParseArtifactID("0123456789abcde"); err == nil {
		t.Fatal("wrong length should fail")
	}
}

func TestValidateOperationName(t *testing.T) {
	cases := []struct {
		op    string
		valid bool
	}{
		{"plan", true},
		{"", true}, // Empty selects the current or recommended operation.
		{"a", true},
		{"a.b-c_d", true},
		{"A", false}, // The first character must be lowercase.
		{"1abc", false},
		{"a b", false}, // Spaces are forbidden.
		{string([]byte{'a', 0x01}), false},
	}
	for _, c := range cases {
		err := ValidateOperationName(OperationName(c.op))
		if c.valid && err != nil {
			t.Errorf("op=%q expected valid, got %v", c.op, err)
		}
		if !c.valid && err == nil {
			t.Errorf("op=%q expected invalid, got nil", c.op)
		}
	}
}

func TestValidateSources(t *testing.T) {
	if err := ValidateSources(nil); err != nil {
		t.Fatalf("nil should pass: %v", err)
	}
	if err := ValidateSources([]SourceRef{
		{Kind: "workspace_path", Value: "references/rm0041.pdf"},
	}); err != nil {
		t.Fatalf("valid source should pass: %v", err)
	}
	if err := ValidateSources([]SourceRef{
		{Kind: "workspace_path", Value: "a"},
		{Kind: "workspace_path", Value: "a"},
	}); err == nil {
		t.Fatal("duplicate should fail")
	}
	if err := ValidateSources([]SourceRef{{Kind: "", Value: "a"}}); err == nil {
		t.Fatal("missing kind should fail")
	}
	if err := ValidateSources([]SourceRef{{Kind: "workspace_path"}}); err == nil {
		t.Fatal("missing value should fail")
	}
	if err := ValidateSources([]SourceRef{{Kind: "workspace_path", Value: "../secret"}}); err == nil {
		t.Fatal("escaping workspace path should fail")
	}
	if err := ValidateSources([]SourceRef{{Kind: "workspace_path", Value: "refs/a", Digest: "bad"}}); err == nil {
		t.Fatal("invalid source digest should fail")
	}
	tooMany := make([]SourceRef, MaxSources+1)
	for i := range tooMany {
		tooMany[i] = SourceRef{Kind: "k", Value: "v"}
	}
	if err := ValidateSources(tooMany); err == nil {
		t.Fatal("too many should fail")
	}
}

func TestValidateAdvanceRequest(t *testing.T) {
	r := AdvanceRequest{
		ProjectID:        "mp-0123456789abcdef",
		Operation:        "plan",
		Instruction:      "add device",
		Sources:          []SourceRef{{Kind: "workspace_path", Value: "refs/rm.pdf"}},
		ExpectedRevision: 0,
	}
	if err := ValidateAdvanceRequest(r); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	bad := r
	bad.ProjectID = "bad"
	if err := ValidateAdvanceRequest(bad); err == nil {
		t.Fatal("bad project id should fail")
	}
	bad2 := r
	bad2.ExpectedRevision = -1
	if err := ValidateAdvanceRequest(bad2); err == nil {
		t.Fatal("negative revision should fail")
	}
}

func TestValidateApplyRequest(t *testing.T) {
	r := ApplyRequest{
		ProjectID:        "mp-0123456789abcdef",
		ExpectedRevision: 1,
		PreviewID:        "pv-001",
		ApprovalToken:    "tok-001",
	}
	if err := ValidateApplyRequest(r); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	if err := ValidateApplyRequest(ApplyRequest{ProjectID: "mp-0123456789abcdef"}); err == nil {
		t.Fatal("missing preview/approval should fail")
	}
}

func TestValidateProjectView(t *testing.T) {
	v := ProjectView{
		ID:               "mp-0123456789abcdef",
		Title:            "test project",
		Revision:         1,
		Status:           ProjectPending,
		CurrentOperation: "plan",
		Artifacts:        []ArtifactDescriptor{validArtifact("plan")},
		CreatedAt:        time.Unix(1, 0).UTC(),
		UpdatedAt:        time.Unix(2, 0).UTC(),
	}
	if err := ValidateProjectView(v); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	bad := v
	bad.Status = "weird"
	if err := ValidateProjectView(bad); err == nil {
		t.Fatal("unknown status should fail")
	}
	bad2 := v
	badArtifact := validArtifact("plan")
	badArtifact.Name = ""
	bad2.Artifacts = []ArtifactDescriptor{badArtifact}
	if err := ValidateProjectView(bad2); err == nil {
		t.Fatal("missing artifact name should fail")
	}
}

func TestValidateCapabilities(t *testing.T) {
	c := Capabilities{
		APIVersion:    "v1",
		EngineName:    "current-pipeline",
		EngineVersion: "1.0",
		Operations:    []OperationDescriptor{{Name: "extract"}, {Name: "plan"}},
		ArtifactKinds: []string{"code", "plan"},
	}
	if err := ValidateCapabilities(c); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	dup := c
	dup.Operations = []OperationDescriptor{{Name: "plan"}, {Name: "plan"}}
	if err := ValidateCapabilities(dup); err == nil {
		t.Fatal("duplicate operation should fail")
	}
	unsorted := c
	unsorted.Operations = []OperationDescriptor{{Name: "plan"}, {Name: "extract"}}
	if err := ValidateCapabilities(unsorted); err == nil {
		t.Fatal("unsorted operations should fail")
	}
}

func TestValidateEvent(t *testing.T) {
	if err := ValidateEvent(Event{
		Kind:      EventOperationStarted,
		ProjectID: "mp-0123456789abcdef",
		Operation: "plan",
	}); err != nil {
		t.Fatalf("started should pass: %v", err)
	}
	if err := ValidateEvent(Event{
		Kind:      EventOperationStarted,
		ProjectID: "mp-0123456789abcdef",
		Operation: "plan",
		Result:    &ResultSummary{},
	}); err == nil {
		t.Fatal("started with result should fail")
	}
	if err := ValidateEvent(Event{
		Kind:      EventOperationProgress,
		ProjectID: "mp-0123456789abcdef",
		Operation: "plan",
	}); err == nil {
		t.Fatal("progress without Progress should fail")
	}
	if err := ValidateEvent(Event{
		Kind:      EventOperationCompleted,
		ProjectID: "mp-0123456789abcdef",
		Operation: "plan",
	}); err == nil {
		t.Fatal("completed without result/error should fail")
	}
	if err := ValidateEvent(Event{
		Kind:      EventOperationCompleted,
		ProjectID: "mp-0123456789abcdef",
		Operation: "plan",
		Result:    &ResultSummary{Status: OperationSucceeded},
	}); err != nil {
		t.Fatalf("completed success should pass: %v", err)
	}
	if err := ValidateEvent(Event{
		Kind:      EventOperationStarted,
		ProjectID: "mp-0123456789abcdef",
		Operation: "plan",
		Progress:  &Progress{Text: "bad"},
	}); err == nil {
		t.Fatal("started with progress should fail")
	}
}

func TestCloneCapabilities(t *testing.T) {
	c := Capabilities{
		APIVersion:    "v1",
		EngineName:    "current",
		EngineVersion: "1.0",
		Operations:    []OperationDescriptor{{Name: "plan"}},
		ArtifactKinds: []string{"plan"},
	}
	clone := CloneCapabilities(c)
	clone.Operations[0].Name = "extract"
	if c.Operations[0].Name == "extract" {
		t.Fatal("clone should be defensive")
	}
}

func TestCloneProjectView(t *testing.T) {
	v := ProjectView{
		ID:          "mp-0123456789abcdef",
		Title:       "t",
		Revision:    1,
		Status:      ProjectPending,
		Artifacts:   []ArtifactDescriptor{validArtifact("plan")},
		PublicError: &PublicError{Code: ErrorInternal, Message: "x", Details: map[string]string{"project_id": "p"}},
	}
	clone := CloneProjectView(v)
	clone.Artifacts[0].Name = "modified"
	if v.Artifacts[0].Name == "modified" {
		t.Fatal("artifact slice should be defensive")
	}
	clone.PublicError.Details["project_id"] = "modified"
	if v.PublicError.Details["project_id"] == "modified" {
		t.Fatal("details map should be defensive")
	}
}

func TestValidateApplyPreviewAndPartialOutcome(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	preview := ApplyPreview{
		ID:              "pv-001",
		ProjectID:       "mp-0123456789abcdef",
		ProjectRevision: 2,
		Diff:            validArtifact("diff"),
		Files: []FileChange{{
			Path:      "hw/misc/acme.c",
			Kind:      "create",
			NewBytes:  10,
			NewDigest: digest,
		}},
		ExpiresAt: time.Unix(60, 0).UTC(),
	}
	if err := ValidateApplyPreview(preview); err != nil {
		t.Fatalf("valid preview should pass: %v", err)
	}
	preview.Files[0].Path = "../escape"
	if err := ValidateApplyPreview(preview); err == nil {
		t.Fatal("escaping preview path should fail")
	}

	result := OperationResult{
		Project: ProjectView{
			ID:        "mp-0123456789abcdef",
			Title:     "device",
			Revision:  3,
			Status:    ProjectBlocked,
			CreatedAt: time.Unix(1, 0).UTC(),
			UpdatedAt: time.Unix(2, 0).UTC(),
		},
		Operation: "apply",
		Status:    OperationBlocked,
		Blocked:   true,
		Reason:    "partial",
		Apply: &ApplyOutcome{
			Written: []string{"hw/misc/acme.c"},
			Skipped: []string{"hw/misc/meson.build"},
			Partial: true,
			Reason:  "partial",
		},
	}
	if err := ValidateOperationResult(result); err != nil {
		t.Fatalf("partial result should retain exact outcome: %v", err)
	}
	clone := CloneOperationResult(result)
	clone.Apply.Written[0] = "changed"
	if result.Apply.Written[0] == "changed" {
		t.Fatal("apply outcome should be cloned")
	}
}

func TestCloneArtifactContent(t *testing.T) {
	c := ArtifactContent{Data: []byte("hello")}
	clone := CloneArtifactContent(c)
	clone.Data[0] = 'X'
	if c.Data[0] == 'X' {
		t.Fatal("data slice should be defensive")
	}
}

func TestValidateArtifactContentAndPages(t *testing.T) {
	artifact := validArtifact("plan")
	artifact.Bytes = 5
	content := ArtifactContent{
		Artifact: artifact,
		Data:     []byte("hello"),
		Offset:   0,
		Next:     5,
		EOF:      true,
	}
	if err := ValidateArtifactContent(content); err != nil {
		t.Fatalf("valid content should pass: %v", err)
	}
	content.Next = 4
	if err := ValidateArtifactContent(content); err == nil {
		t.Fatal("mismatched content offsets should fail")
	}

	project := ProjectView{
		ID:        "mp-0123456789abcdef",
		Title:     "device",
		Revision:  1,
		Status:    ProjectPending,
		CreatedAt: time.Unix(1, 0).UTC(),
		UpdatedAt: time.Unix(2, 0).UTC(),
	}
	if err := ValidateProjectPage(ProjectPage{Projects: []ProjectView{project}}); err != nil {
		t.Fatalf("valid project page should pass: %v", err)
	}
	evidence := validArtifact("evidence")
	if err := ValidateEvidencePage(EvidencePage{Evidence: []ArtifactDescriptor{evidence}}); err != nil {
		t.Fatalf("valid evidence page should pass: %v", err)
	}
	evidence.Kind = "code"
	if err := ValidateEvidencePage(EvidencePage{Evidence: []ArtifactDescriptor{evidence}}); err == nil {
		t.Fatal("non-evidence artifact should fail evidence page validation")
	}
}

func TestCloneAdvanceRequest(t *testing.T) {
	r := AdvanceRequest{
		ProjectID: "mp-0123456789abcdef",
		Sources:   []SourceRef{{Kind: "workspace_path", Value: "a"}},
	}
	clone := CloneAdvanceRequest(r)
	clone.Sources[0].Value = "modified"
	if r.Sources[0].Value == "modified" {
		t.Fatal("sources should be defensive")
	}
}

// TestCompileTimeConsumer proves that a caller needs only modelingapi types and
// does not need modeling, app, channel, or other implementation packages.
func TestCompileTimeConsumer(t *testing.T) {
	var s Service
	var call CallContext
	var req CreateRequest
	var view ProjectView
	var caps Capabilities
	var evt Event
	var err PublicError
	// Reference each contract type without invoking an implementation.
	_ = s
	_ = call
	_ = req
	_ = view
	_ = caps
	_ = evt
	_ = err
	// Cover every Service method name recognized by MutationKindOf.
	for _, m := range []string{
		"Capabilities", "Create", "List", "Show", "Advance",
		"Reset", "ReadArtifact", "PlanApply", "Apply", "Evidence",
	} {
		_ = MutationKindOf(m)
	}
	// Service signatures use context.Context.
	var ctx context.Context
	_ = ctx
	// View types use time.Time.
	var tm time.Time
	_ = tm
	// Keep the reflect import used by this compile-time consumer test.
	_ = reflect.TypeOf(t)
}

// TestFakeService proving CLI Adapter doesn't need current Pipeline.
type fakeService struct{}

func (fakeService) Capabilities(context.Context, CallContext) (Capabilities, error) {
	return Capabilities{}, nil
}
func (fakeService) Create(context.Context, CallContext, CreateRequest) (ProjectView, error) {
	return ProjectView{}, nil
}
func (fakeService) List(context.Context, CallContext, ListRequest) (ProjectPage, error) {
	return ProjectPage{}, nil
}
func (fakeService) Show(context.Context, CallContext, ShowRequest) (ProjectView, error) {
	return ProjectView{}, nil
}
func (fakeService) Advance(context.Context, CallContext, AdvanceRequest) (OperationResult, error) {
	return OperationResult{}, nil
}
func (fakeService) Reset(context.Context, CallContext, ResetRequest) (ProjectView, error) {
	return ProjectView{}, nil
}
func (fakeService) ReadArtifact(context.Context, CallContext, ReadArtifactRequest) (ArtifactContent, error) {
	return ArtifactContent{}, nil
}
func (fakeService) PlanApply(context.Context, CallContext, PlanApplyRequest) (ApplyPreview, error) {
	return ApplyPreview{}, nil
}
func (fakeService) Apply(context.Context, CallContext, ApplyRequest) (OperationResult, error) {
	return OperationResult{}, nil
}
func (fakeService) Evidence(context.Context, CallContext, EvidenceRequest) (EvidencePage, error) {
	return EvidencePage{}, nil
}

func TestFakeServiceImplementsContract(t *testing.T) {
	var s Service = fakeService{}
	_ = s
}
