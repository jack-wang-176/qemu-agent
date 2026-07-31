package app

// modeling_integration_test.go is the cross-layer test §115.4 asks for: one
// project driven from `new` through all five stages to `diff` and `apply`, using a
// stub model and a recording executor.
//
// It exists because every other modeling test is scoped to one layer, and the
// properties that matter most are the ones no single layer can show:
//
//   - the QEMU tree is untouched until apply, so emit really is a staging step;
//   - every stage leaves an artifact behind, so a project's history is complete;
//   - a "restart" — a second store over the same directory — sees the same state,
//     so nothing important lived only in memory;
//   - the executor sees exactly the tool calls the pipeline claims to make, and
//     nothing bypasses it.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/app/build"
	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// integrationIR is the Reg-IR the stub model returns for extract. It is the same
// shape as internal/modeling's own fixture, restated here because a test in
// another package cannot reach that helper — and because pinning it locally means
// a change to the fixture cannot silently change what this test proves.
const integrationIR = `{
  "device": "acme_uart",
  "bus_kind": "sysbus",
  "mmio_size": 4096,
  "registers": [
    {"name": "CTRL", "offset": 0, "width": 32, "access": "rw", "reset": 0,
     "fields": [{"name": "ENABLE", "bit": 0, "width": 1, "description": "starts the device"}],
     "effect": "writing ENABLE starts the transmitter"},
    {"name": "STATUS", "offset": 4, "width": 32, "access": "ro"},
    {"name": "DATA", "offset": 8, "width": 8, "access": "wo"}
  ],
  "interrupts": [{"name": "irq", "index": 0, "description": "raised on receive"}]
}`

// scriptedCompleter replies from a fixed list, in order. A stage that calls the
// model more often than the script expects fails loudly rather than reusing the
// last answer, because "which stage asked for what" is part of what is under test.
type scriptedCompleter struct {
	replies []string
	calls   int
}

func (s *scriptedCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	if s.calls >= len(s.replies) {
		return "", errors.New("scripted completer ran out of replies")
	}
	reply := s.replies[s.calls]
	s.calls++
	return reply, nil
}

// recordingExecutor is the audited side-effect boundary. It performs real reads
// and writes — the point is to observe the *set* of tools used, not to stub the
// filesystem — and fakes only bash, because running ninja in a unit test is not
// the property being checked.
type recordingExecutor struct {
	names []string
	root  string
}

func (r *recordingExecutor) Execute(_ context.Context, in security.Invocation) (security.Result, error) {
	r.names = append(r.names, in.ToolName)
	switch in.ToolName {
	case "write":
		path, content, err := writeArgs(in.Arguments)
		if err != nil {
			return security.Result{}, err
		}
		if !strings.HasPrefix(path, r.root) {
			// The executor is the last line of defence, so it asserts the root too.
			return security.Result{}, errors.New("write outside the modeling root")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return security.Result{}, err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return security.Result{}, err
		}
		return security.Result{InvocationID: in.ID, Output: "written"}, nil
	case "bash":
		// Verify's build and qtest steps. Nothing is actually run — compiling QEMU in
		// a unit test is not the property under test — but the exit marker the stage
		// requires is reproduced, because "no marker means unknown status means not a
		// pass" is deliberate and a stub that omitted it would be testing that rule
		// instead of the pipeline.
		return security.Result{
			InvocationID: in.ID,
			Output:       "ninja: no work to do\nOK: 1 test\nqemu-agent-verify-exit: 0\n",
		}, nil
	case "read":
		return security.Result{InvocationID: in.ID, Output: "datasheet excerpt"}, nil
	default:
		return security.Result{}, errors.New("unexpected tool " + in.ToolName)
	}
}

// writeArgs decodes a write invocation's JSON. Kept tiny and local: the test has
// to look inside the arguments to perform the write, but nothing else does.
func writeArgs(arguments string) (string, string, error) {
	var decoded struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return "", "", err
	}
	if decoded.FilePath == "" {
		return "", "", errors.New("write invocation has no file_path")
	}
	return decoded.FilePath, decoded.Content, nil
}

// modelingHarness is the whole wired layer plus the seams a test asserts on.
//
// There are two executors because production has two: the pipeline's is rooted at
// the workspace so extract can read a datasheet, the applier's at the QEMU tree so
// a write can create hw/misc/foo.c. Keeping them distinct here is what lets the
// test assert that each tool arrived at the right one — a single shared double
// would pass no matter which root the wiring chose.
type modelingHarness struct {
	components build.ModelingComponents
	executor   *recordingExecutor
	applyExec  *recordingExecutor
	completer  *scriptedCompleter
	config     config.Config
	qemuRoot   string
	datasheet  string
}

// newModelingHarness wires modeling exactly as production does, differing only in
// the two injected seams: a scripted model and a recording executor.
func newModelingHarness(t *testing.T) *modelingHarness {
	t.Helper()
	data := t.TempDir()
	qemuRoot := t.TempDir()
	// The renderer's manifest appends to the two build files every QEMU device has
	// to be registered in, so both must exist for a modify action to have a base.
	// A real checkout always has them; seeding them here is what makes this a test
	// of the apply rather than a test of a missing file.
	if err := os.MkdirAll(filepath.Join(qemuRoot, "hw", "misc"), 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := map[string]string{
		filepath.Join("hw", "misc", "meson.build"): "system_ss.add(files('other.c'))\n",
		filepath.Join("hw", "misc", "Kconfig"):     "config OTHER_DEVICE\n    bool\n",
	}
	for relative, body := range seeded {
		if err := os.WriteFile(filepath.Join(qemuRoot, relative), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Extract reads its sources through the read tool, so there has to be something
	// to read. It lives in the workspace, not in the QEMU tree: a datasheet is an
	// input the user supplies, not part of the checkout being modified.
	workspace := t.TempDir()
	datasheet := filepath.Join(workspace, "acme-uart.txt")
	if err := os.WriteFile(datasheet, []byte("CTRL 0x00 rw\nSTATUS 0x04 ro\nDATA 0x08 wo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Paths: config.PathConfig{DataDir: data, SessionDir: filepath.Join(data, "sessions"), Workspace: workspace},
		Modeling: config.ModelingConfig{
			Enabled:          true,
			Dir:              filepath.Join(data, "modeling"),
			QemuRoot:         qemuRoot,
			BuildDir:         filepath.Join(qemuRoot, "build"),
			MaxProjects:      16,
			MaxArtifactBytes: 1 << 18,
			MaxProjectBytes:  1 << 20,
			StageTimeout:     time.Minute,
		},
		Tools: config.ToolConfig{ReadMaxLines: 200, Timeout: time.Minute, MaxOutputBytes: 1 << 16},
	}
	// The applier hands the tool a symlink-resolved path, so the executor's own root
	// check has to be resolved the same way: on macOS t.TempDir() reports /var/...
	// while the resolved form is /private/var/....
	resolvedRoot, err := filepath.EvalSymlinks(qemuRoot)
	if err != nil {
		t.Fatal(err)
	}
	// The pipeline's double gets no root: it only ever reads and shells out, and a
	// write arriving here would mean the applier borrowed the wrong executor.
	executor := &recordingExecutor{}
	applyExec := &recordingExecutor{root: resolvedRoot}
	completer := &scriptedCompleter{replies: []string{
		// plan: prose, consumed as the plan artifact.
		"1. map CTRL/STATUS/DATA\n2. one irq line\n",
		// extract: the Reg-IR.
		integrationIR,
		// infer: the same map with behaviour filled in. Infer may not change the
		// register layout, so the reply repeats it verbatim.
		integrationIR,
	}}
	components, err := build.BuildModeling(build.ModelingInput{
		Config: cfg, Logger: testLogger(), Executor: executor,
		ApplyExecutor: applyExec, Completer: completer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &modelingHarness{
		components: components, executor: executor, applyExec: applyExec,
		completer: completer, config: cfg, qemuRoot: qemuRoot, datasheet: datasheet,
	}
}

// advance runs one stage with the harness's standard request and sources, so the
// tests below do not repeat the RunRequest literal five times.
func (h *modelingHarness) advance(t *testing.T, ctx context.Context, id string, scope modeling.Scope) modeling.RunResult {
	t.Helper()
	result, err := h.components.Runner.Advance(ctx, modeling.RunRequest{
		ProjectID: id,
		Scope:     scope,
		Request:   "model the acme uart at 0x1000, 4 KiB, one irq",
		Sources:   []string{h.datasheet},
		Events:    modeling.NopEmitter{},
	})
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	return result
}

// callerCtx attaches the identity the tool adapter requires. In production the
// /modeling command does this; a test that drives the Runner directly has to do
// the same, which is itself worth asserting — see TestModelingRefusesWithoutCaller.
func callerCtx() context.Context {
	return security.WithCaller(context.Background(), security.Caller{
		TraceID: "trace-integration", SessionID: "sess-1", SessionKey: "cli:local",
		Channel: "cli", Interactive: true,
	})
}

// treeSnapshot lists every file under root with its content digest, so "the tree
// did not change" is a comparison rather than a spot check.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[relative] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestModelingPipelineEndToEnd is the §115.4 scenario in one function, because
// the assertions are about the sequence and splitting it would mean re-running
// four stages to check the fifth.
func TestModelingPipelineEndToEnd(t *testing.T) {
	harness := newModelingHarness(t)
	ctx := callerCtx()
	scope := modeling.Scope{WorkspaceID: harness.components.WorkspaceID, UserID: "user-1"}
	before := treeSnapshot(t, harness.qemuRoot)

	project, err := harness.components.Runner.Create(ctx, "acme uart", scope)
	if err != nil {
		t.Fatal(err)
	}

	// (b) every stage leaves an artifact. Advance is called once per stage, and the
	// project's artifact map is checked after each one rather than at the end, so a
	// stage that produced nothing is reported as itself.
	//
	// The loop stops at emit on purpose: emit finishes blocked on "awaiting_apply",
	// so a further advance would re-run emit rather than move on. Verify is only
	// reachable once the code is actually in the tree, which is what makes the
	// apply below a step in the pipeline rather than an afterthought.
	for _, stage := range []modeling.Stage{
		modeling.StagePlan, modeling.StageExtract, modeling.StageInfer, modeling.StageEmit,
	} {
		result := harness.advance(t, ctx, project.ID, scope)
		if result.Stage != stage {
			t.Fatalf("advance ran %q; want %q", result.Stage, stage)
		}
		if len(result.Refs) == 0 {
			t.Errorf("stage %q produced no artifacts", stage)
		}
		// (a) the QEMU tree is untouched by every stage, emit included. Emit writes
		// into the artifact store, not into the checkout — that is the whole reason
		// apply is a separate, approved command.
		if got := treeSnapshot(t, harness.qemuRoot); !sameSnapshot(before, got) {
			t.Fatalf("stage %q changed the QEMU tree", stage)
		}
	}
	// Emit blocked rather than completed, and said why in a category.
	blocked, err := harness.components.Runner.Show(ctx, project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != modeling.StatusBlocked {
		t.Errorf("after emit status = %q; want blocked", blocked.Status)
	}
	if blocked.LastError == "" {
		t.Error("emit blocked without recording why")
	}

	// The diff is a real artifact rather than something the command computes, so it
	// is readable through the same digest-checked path as any other artifact.
	current, err := harness.components.Runner.Show(ctx, project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	diff, ok := findRef(current, modeling.StageEmit, "device.diff")
	if !ok {
		t.Fatal("emit left no device.diff")
	}
	body, err := harness.components.Runner.Read(ctx, project.ID, diff, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "acme_uart") {
		t.Errorf("diff does not mention the device: %q", firstLine(string(body)))
	}

	// The tree is still untouched right up to the apply call.
	if got := treeSnapshot(t, harness.qemuRoot); !sameSnapshot(before, got) {
		t.Fatal("the QEMU tree changed before apply")
	}

	result, err := harness.components.Applier.Apply(ctx, project.ID, scope)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Written) == 0 {
		t.Fatal("apply wrote nothing")
	}
	after := treeSnapshot(t, harness.qemuRoot)
	if sameSnapshot(before, after) {
		t.Fatal("apply reported writes but the tree is unchanged")
	}
	// The modify action appended rather than overwrote: the line that was already
	// in meson.build is still there.
	meson := after[filepath.Join("hw", "misc", "meson.build")]
	if !strings.Contains(meson, "other.c") {
		t.Errorf("apply overwrote meson.build: %q", meson)
	}

	// The apply unblocked the project, so verify is now the current stage: this is
	// the step that turns "code was generated" into "code builds and passes qtest".
	verified := harness.advance(t, ctx, project.ID, scope)
	if verified.Stage != modeling.StageVerify {
		t.Fatalf("after apply, advance ran %q; want verify", verified.Stage)
	}
	if len(verified.Refs) == 0 {
		t.Error("verify produced no evidence")
	}
	final, err := harness.components.Runner.Show(ctx, project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != modeling.StatusDone {
		t.Errorf("final status = %q; want done", final.Status)
	}

	// (d) each executor saw only the tools its root can serve. The applier's must
	// have seen the writes; the pipeline's must not have, because a write arriving at
	// a workspace-rooted tool would land device code outside the QEMU tree.
	if applyCalls := uniqueSorted(harness.applyExec.names); !contains(applyCalls, "write") {
		t.Errorf("apply executor never saw a write; calls = %v", applyCalls)
	}
	unique := uniqueSorted(harness.executor.names)
	for _, want := range []string{"read", "bash"} {
		if !contains(unique, want) {
			t.Errorf("pipeline executor never saw %q; calls = %v", want, unique)
		}
	}
	if contains(unique, "write") {
		t.Error("pipeline executor saw a write; the applier must use its own root")
	}
	for _, name := range unique {
		switch name {
		case "read", "bash":
		default:
			t.Errorf("executor saw an unexpected tool %q", name)
		}
	}
}

// TestModelingRestartSeesTheSameProject is (c): a second BuildModeling over the
// same directory is a process restart as far as the stores are concerned.
func TestModelingRestartSeesTheSameProject(t *testing.T) {
	harness := newModelingHarness(t)
	ctx := callerCtx()
	scope := modeling.Scope{WorkspaceID: harness.components.WorkspaceID, UserID: "user-1"}

	project, err := harness.components.Runner.Create(ctx, "acme uart", scope)
	if err != nil {
		t.Fatal(err)
	}
	harness.advance(t, ctx, project.ID, scope)
	first, err := harness.components.Runner.Show(ctx, project.ID, scope)
	if err != nil {
		t.Fatal(err)
	}

	restarted, err := build.BuildModeling(build.ModelingInput{
		Config: harness.config, Logger: testLogger(),
		Executor: harness.executor, ApplyExecutor: harness.applyExec,
		Completer: harness.completer,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := restarted.Runner.Show(ctx, project.ID, scope)
	if err != nil {
		t.Fatalf("the restarted build cannot see the project: %v", err)
	}
	if second.Current != first.Current || second.Status != first.Status || second.Revision != first.Revision {
		t.Errorf("restart sees %s/%s rev %d; want %s/%s rev %d",
			second.Current, second.Status, second.Revision, first.Current, first.Status, first.Revision)
	}
	if len(second.Artifacts[modeling.StagePlan]) != len(first.Artifacts[modeling.StagePlan]) {
		t.Error("restart lost the plan artifacts")
	}
	// A different workspace is a different scope, and an id from one must not be
	// visible in the other even though the store file is right there on disk.
	if _, err := restarted.Runner.Show(ctx, project.ID, modeling.Scope{
		WorkspaceID: "ws-somebody-else", UserID: "user-1",
	}); err == nil {
		t.Error("a project was visible from another workspace")
	}
}

// TestModelingRefusesWithoutCaller is the fail-closed property from the other
// side: a stage that needs a tool cannot run on a context with no identity, so a
// future caller that forgets security.WithCaller gets an error rather than an
// approval prompt aimed at the wrong terminal.
func TestModelingRefusesWithoutCaller(t *testing.T) {
	harness := newModelingHarness(t)
	scope := modeling.Scope{WorkspaceID: harness.components.WorkspaceID, UserID: "user-1"}

	project, err := harness.components.Runner.Create(context.Background(), "acme uart", scope)
	if err != nil {
		t.Fatal(err)
	}
	// Advance through the stages that do not need a tool, then apply — which does.
	ctx := callerCtx()
	for range 4 {
		harness.advance(t, ctx, project.ID, scope)
	}
	before := treeSnapshot(t, harness.qemuRoot)

	if _, err := harness.components.Applier.Apply(context.Background(), project.ID, scope); err == nil {
		t.Fatal("apply succeeded with no caller identity")
	}
	if got := treeSnapshot(t, harness.qemuRoot); !sameSnapshot(before, got) {
		t.Error("a refused apply still changed the tree")
	}
}

// --- small helpers, kept here so the assertions above read as prose ---

func findRef(project modeling.Project, stage modeling.Stage, name string) (modeling.ArtifactRef, bool) {
	var found modeling.ArtifactRef
	ok := false
	for _, ref := range project.Artifacts[stage] {
		if ref.Name == name {
			found, ok = ref, true
		}
	}
	return found, ok
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for path, body := range a {
		if b[path] != body {
			return false
		}
	}
	return true
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

// compile-time assurance that the recording executor satisfies what the wiring
// needs; a signature change should break here rather than inside a subtest.
var _ build.ToolExecutor = (*recordingExecutor)(nil)
var _ modeling.Completer = (*scriptedCompleter)(nil)
var _ = tools.ExecutionResult{}
