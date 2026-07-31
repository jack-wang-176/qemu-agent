package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// modelingConfig builds a config whose modeling group is valid but disabled. Each
// test enables exactly the parts it is about, so a failure names one cause.
func modelingConfig(t *testing.T, mutate func(*config.Config)) config.Config {
	t.Helper()
	data := t.TempDir()
	cfg := config.Config{
		Paths: config.PathConfig{DataDir: data, SessionDir: filepath.Join(data, "sessions"), Workspace: t.TempDir()},
		Modeling: config.ModelingConfig{
			Dir:              filepath.Join(data, "modeling"),
			MaxProjects:      16,
			MaxArtifactBytes: 1 << 16,
			MaxProjectBytes:  1 << 18,
			StageTimeout:     time.Minute,
		},
		Tools: config.ToolConfig{ReadMaxLines: 200, Timeout: time.Minute, MaxOutputBytes: 4096},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

// stubExecutor records what it was asked to run and returns a fixed result.
type stubExecutor struct {
	calls []security.Invocation
	err   error
}

func (s *stubExecutor) Execute(_ context.Context, in security.Invocation) (security.Result, error) {
	s.calls = append(s.calls, in)
	if s.err != nil {
		return security.Result{}, s.err
	}
	return security.Result{InvocationID: in.ID, Output: "ok"}, nil
}

func TestBuildModelingDisabledIsUsableNotNil(t *testing.T) {
	components, err := BuildModeling(ModelingInput{Config: modelingConfig(t, nil), Logger: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	if components.Projects == nil || components.Artifacts == nil || components.Runner == nil || components.Applier == nil {
		t.Fatalf("components = %#v", components)
	}
	// Every seam is callable and every answer is ErrDisabled — which is the point:
	// the command layer maps that one sentinel to "modeling is disabled" instead of
	// each call site testing configuration or risking a nil dereference.
	projects, err := components.Runner.List(context.Background(), modeling.Query{WorkspaceID: components.WorkspaceID})
	if !errors.Is(err, modeling.ErrDisabled) {
		t.Fatalf("disabled list = %#v, %v; want ErrDisabled", projects, err)
	}
	if len(projects) != 0 {
		t.Errorf("disabled list returned %d projects", len(projects))
	}
	if _, err := components.Runner.Create(context.Background(), "device", modeling.Scope{
		WorkspaceID: components.WorkspaceID, UserID: "user-1",
	}); err == nil {
		t.Fatal("a disabled runner created a project")
	}
	if _, err := components.Applier.Apply(context.Background(), "mp-1", modeling.Scope{
		WorkspaceID: components.WorkspaceID, UserID: "user-1",
	}); err == nil {
		t.Fatal("a disabled applier applied")
	}
}

// TestBuildModelingWorkspaceIDMatchesKnowledge is the check that keeps two
// capabilities in one directory from disagreeing about scope. If these ever
// diverge, a project created by one becomes invisible to the other.
func TestBuildModelingWorkspaceIDMatchesKnowledge(t *testing.T) {
	cfg := modelingConfig(t, nil)
	modelingSide, err := BuildModeling(ModelingInput{Config: cfg, Logger: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := WorkspaceID(cfg.Paths.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if modelingSide.WorkspaceID != expected {
		t.Errorf("workspace id = %q; want %q", modelingSide.WorkspaceID, expected)
	}
	if strings.Contains(modelingSide.WorkspaceID, cfg.Paths.Workspace) {
		t.Error("workspace id leaks the workspace path")
	}
}

func TestBuildModelingRejectsMissingDependencies(t *testing.T) {
	enabled := func(cfg *config.Config) { cfg.Modeling.Enabled = true }
	cases := []struct {
		name  string
		input ModelingInput
		want  string
	}{
		{
			name:  "no executor",
			input: ModelingInput{Config: modelingConfig(t, enabled), Logger: testLogger(t), Completer: stubCompleter{}},
			want:  "tool executor",
		},
		{
			name:  "no completer",
			input: ModelingInput{Config: modelingConfig(t, enabled), Logger: testLogger(t), Executor: &stubExecutor{}},
			want:  "model",
		},
		{
			name:  "no logger",
			input: ModelingInput{Config: modelingConfig(t, enabled), Executor: &stubExecutor{}, Completer: stubCompleter{}},
			want:  "logger",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			components, err := BuildModeling(testCase.input)
			if err == nil {
				t.Fatal("build succeeded; want an error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error %q does not mention %q", err, testCase.want)
			}
			// Even on the error path the caller is not handed nil: a caller that
			// logs and continues must not panic on the next call.
			if components.Runner == nil || components.Applier == nil {
				t.Errorf("components = %#v", components)
			}
		})
	}
}

// TestBuildModelingWithoutQemuRootKeepsApplierDisabled covers the middle
// capability level: the pipeline works, but there is nowhere to land code, so
// apply refuses instead of failing halfway through a write.
func TestBuildModelingWithoutQemuRootKeepsApplierDisabled(t *testing.T) {
	cfg := modelingConfig(t, func(cfg *config.Config) { cfg.Modeling.Enabled = true })
	components, err := BuildModeling(ModelingInput{
		Config: cfg, Logger: testLogger(t), Executor: &stubExecutor{}, Completer: stubCompleter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := components.Applier.(modeling.DisabledApplier); !ok {
		t.Errorf("applier = %T; want DisabledApplier when QemuRoot is empty", components.Applier)
	}
	// The runner, by contrast, is real: a project can be created and listed.
	scope := modeling.Scope{WorkspaceID: components.WorkspaceID, UserID: "user-1"}
	project, err := components.Runner.Create(context.Background(), "k230 rmu", scope)
	if err != nil {
		t.Fatal(err)
	}
	if project.Current != modeling.StagePlan {
		t.Errorf("current = %q; want plan", project.Current)
	}
	// And it persisted where configuration said it would, at 0700.
	info, err := os.Stat(projectsRoot(cfg.Modeling.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("projects dir mode = %o; want 700", perm)
	}
}

func TestBuildModelingWithQemuRootBuildsRealApplier(t *testing.T) {
	qemuRoot := t.TempDir()
	cfg := modelingConfig(t, func(cfg *config.Config) {
		cfg.Modeling.Enabled = true
		cfg.Modeling.QemuRoot = qemuRoot
	})
	components, err := BuildModeling(ModelingInput{
		Config: cfg, Logger: testLogger(t), Executor: &stubExecutor{}, Completer: stubCompleter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := components.Applier.(modeling.DisabledApplier); ok {
		t.Fatal("applier is still disabled with a QemuRoot set")
	}
	// An unknown project is a not-found, not a panic: the applier is real but
	// holds no state of its own.
	if _, err := components.Applier.Plan(context.Background(), "mp-missing", modeling.Scope{
		WorkspaceID: components.WorkspaceID, UserID: "user-1",
	}); err == nil {
		t.Error("plan succeeded for an unknown project")
	}
}

// TestModelingToolsNeedsACallerIdentity is the fail-closed check. A tool call
// with no caller attached must not reach the executor at all: guessing the
// identity would either deny every write or route an approval prompt to a
// terminal nobody is watching.
func TestModelingToolsNeedsACallerIdentity(t *testing.T) {
	executor := &stubExecutor{}
	runner := newModelingTools(executor, func() string { return "inv-1" })

	if _, err := runner.Run(context.Background(), "write", map[string]any{"file_path": "x"}); err == nil {
		t.Fatal("run succeeded without a caller identity")
	}
	if len(executor.calls) != 0 {
		t.Errorf("executor was called %d times; want 0", len(executor.calls))
	}
}

// TestModelingToolsCarriesCallerIntoInvocation checks the translation itself:
// the request's identity has to arrive at the executor intact, because it is what
// the audit log and the approval routing are built on.
func TestModelingToolsCarriesCallerIntoInvocation(t *testing.T) {
	executor := &stubExecutor{}
	runner := newModelingTools(executor, func() string { return "inv-1" })
	caller := security.Caller{
		TraceID: "trace-9", SessionID: "sess-9", SessionKey: "cli:local", Channel: "cli", Interactive: true,
	}

	result, err := runner.Run(security.WithCaller(context.Background(), caller), "write", map[string]any{
		"file_path": "hw/misc/device.c",
		"content":   "// code\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ModelOutput != "ok" {
		t.Errorf("model output = %q", result.ModelOutput)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d; want 1", len(executor.calls))
	}
	call := executor.calls[0]
	if call.TraceID != caller.TraceID || call.SessionKey != caller.SessionKey || call.Channel != caller.Channel {
		t.Errorf("invocation identity = %#v", call)
	}
	if !call.Interactive {
		t.Error("interactive was not carried through; approvals would be denied")
	}
	if call.ToolName != "write" {
		t.Errorf("tool = %q; want write", call.ToolName)
	}
	// Arguments arrive as JSON, which is what the tools parse.
	if !strings.Contains(call.Arguments, `"file_path":"hw/misc/device.c"`) {
		t.Errorf("arguments = %q", call.Arguments)
	}
}

// TestModelingToolsPropagatesDenial keeps a policy denial recognisable. The
// modeling package classifies failures with errors.Is, so wrapping here would
// turn a denial into an unknown error and change the command's reply.
func TestModelingToolsPropagatesDenial(t *testing.T) {
	denied := errors.New("denied by policy")
	executor := &stubExecutor{err: denied}
	runner := newModelingTools(executor, func() string { return "inv-1" })
	caller := security.Caller{TraceID: "trace-9", Channel: "cli"}

	_, err := runner.Run(security.WithCaller(context.Background(), caller), "bash", map[string]any{"command": "ninja"})
	if !errors.Is(err, denied) {
		t.Errorf("error = %v; want the executor's error unwrapped", err)
	}
}

func TestBuildModelingToolManagerRejectsBadRoot(t *testing.T) {
	cfg := config.ToolConfig{ReadMaxLines: 10, Timeout: time.Minute, MaxOutputBytes: 1024}
	if _, err := BuildModelingToolManager("", cfg); err == nil {
		t.Error("empty root accepted")
	}
	if _, err := BuildModelingToolManager("relative/qemu", cfg); err == nil {
		t.Error("relative root accepted")
	}
	manager, err := BuildModelingToolManager(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read", "write", "bash"} {
		if _, ok := manager.Lookup(name); !ok {
			t.Errorf("tool %q is not registered", name)
		}
	}
}
