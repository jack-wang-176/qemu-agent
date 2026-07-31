package modeling

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

// TestApplyRejectsPathEscape ensures no symlink or path component can lead a
// write outside the configured QEMU root.
func TestApplyRejectsPathEscape(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(root string) error
		path     string
		wantFail string
	}{
		{
			name:     "absolute_path",
			path:     "/etc/passwd",
			wantFail: "must be relative",
		},
		{
			name:     "parent_escape",
			path:     "../etc/passwd",
			wantFail: "escapes the QEMU root",
		},
		{
			name: "symlink_to_outside",
			setup: func(root string) error {
				return os.Symlink("/tmp", filepath.Join(root, "hw"))
			},
			path:     "hw/misc/device.c",
			wantFail: "outside the QEMU source tree",
		},
		{
			name: "symlink_parent_to_outside",
			setup: func(root string) error {
				if err := os.MkdirAll(filepath.Join(root, "hw", "misc"), 0755); err != nil {
					return err
				}
				return os.Symlink("/tmp", filepath.Join(root, "hw", "misc", "link"))
			},
			path:     "hw/misc/link/device.c",
			wantFail: "outside the QEMU source tree",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.setup != nil {
				if err := tc.setup(root); err != nil {
					t.Fatal(err)
				}
			}

			store := openStore(t, filepath.Join(root, "modeling"), 10)
			artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 4096, 1<<20)
			applier, project := setupApplier(t, root, store, artifacts, nil)

			// Inject a malicious manifest
			manifest := applyManifest{
				Device: "test",
				Files: []applyEntry{{
					Artifact: "device.c",
					Path:     tc.path,
					Action:   ApplyCreate,
				}},
			}
			injectEmitArtifacts(t, store, artifacts, project, manifest)

			_, err := applier.Apply(context.Background(), project.ID, scopeOf(project))
			if err == nil {
				t.Fatal("apply succeeded; want error")
			}
			if !strings.Contains(err.Error(), tc.wantFail) {
				t.Errorf("error %q does not mention %q", err, tc.wantFail)
			}
		})
	}
}

// TestApplyRejectsExistingFileForCreate ensures a create action fails when the
// target already exists.
func TestApplyRejectsExistingFileForCreate(t *testing.T) {
	root := t.TempDir()
	qemuRoot := filepath.Join(root, "qemu")
	if err := os.MkdirAll(filepath.Join(qemuRoot, "hw", "misc"), 0755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(qemuRoot, "hw", "misc", "device.c")
	if err := os.WriteFile(existing, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	store := openStore(t, filepath.Join(root, "modeling"), 10)
	artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 4096, 1<<20)
	applier, project := setupApplier(t, qemuRoot, store, artifacts, nil)

	manifest := applyManifest{
		Device: "test",
		Files: []applyEntry{{
			Artifact: "device.c",
			Path:     "hw/misc/device.c",
			Action:   ApplyCreate,
		}},
	}
	injectEmitArtifacts(t, store, artifacts, project, manifest)

	_, err := applier.Apply(context.Background(), project.ID, scopeOf(project))
	if err == nil {
		t.Fatal("apply succeeded; want error for existing file")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q does not mention already exists", err)
	}

	// Tree must be unchanged
	content, _ := os.ReadFile(existing)
	if string(content) != "old content" {
		t.Errorf("file was modified; content = %q", content)
	}
}

// TestComposeRejectsChangedBaseDigest covers the check that makes an approved
// modify safe to execute: the bytes being appended to must be the bytes the plan
// measured. This is tested at the compose level on purpose. Apply re-plans, so it
// recomputes BaseDigest from the current file and an edit before the call is
// absorbed; the digest only guards the window between planning and writing inside
// one Apply, which cannot be forced deterministically from the outside.
func TestComposeRejectsChangedBaseDigest(t *testing.T) {
	planned := []byte("old line\n")
	change := FileChange{
		Path:       "hw/misc/meson.build",
		Action:     ApplyModify,
		BaseDigest: digestOf(planned),
	}

	changed := []byte("someone else edited this\n")
	if _, err := compose(change, []byte("fragment\n"), changed, true); err == nil {
		t.Fatal("compose succeeded; want rejection for changed base")
	} else if !errors.Is(err, ErrApplyRejected) {
		t.Errorf("error = %v; want ErrApplyRejected", err)
	} else if !strings.Contains(err.Error(), "changed since the plan was made") {
		t.Errorf("error %q does not mention changed file", err)
	}

	// The same change against the measured content is accepted, so the rejection
	// above is the digest check and not a blanket refusal of modify.
	merged, err := compose(change, []byte("fragment\n"), planned, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != "old line\nfragment\n" {
		t.Errorf("merged = %q", merged)
	}
}

// TestApplyModifyAppendsToExistingFile ensures a modify entry survives the whole
// pipeline: manifest to plan to a write that keeps what was already in the file.
func TestApplyModifyAppendsToExistingFile(t *testing.T) {
	root := t.TempDir()
	qemuRoot := filepath.Join(root, "qemu")
	if err := os.MkdirAll(filepath.Join(qemuRoot, "hw", "misc"), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(qemuRoot, "hw", "misc", "meson.build")
	if err := os.WriteFile(target, []byte("existing_ss.add(files('other.c'))\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := openStore(t, filepath.Join(root, "modeling"), 10)
	artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 4096, 1<<20)
	applier, project := setupApplier(t, qemuRoot, store, artifacts, &callCountTools{limit: 10})

	manifest := applyManifest{
		Device: "test",
		Files: []applyEntry{{
			Artifact: "meson.fragment",
			Path:     "hw/misc/meson.build",
			Action:   ApplyModify,
		}},
	}
	injectEmitArtifacts(t, store, artifacts, project, manifest)

	result, err := applier.Apply(context.Background(), project.ID, scopeOf(project))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) != 1 {
		t.Fatalf("written = %v; want one file", result.Written)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "existing_ss.add(files('other.c'))\n// generated content for hw/misc/meson.build\n"
	if string(got) != want {
		t.Errorf("file = %q; want %q", got, want)
	}
}

// TestApplyGoesThroughExecutor ensures every write is an audited tool call.
func TestApplyGoesThroughExecutor(t *testing.T) {
	root := t.TempDir()
	qemuRoot := filepath.Join(root, "qemu")
	if err := os.MkdirAll(filepath.Join(qemuRoot, "hw", "misc"), 0755); err != nil {
		t.Fatal(err)
	}

	store := openStore(t, filepath.Join(root, "modeling"), 10)
	artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 4096, 1<<20)
	recorder := &recordingTools{fakeTools: fakeTools{output: ""}}
	applier, project := setupApplier(t, qemuRoot, store, artifacts, recorder)

	manifest := applyManifest{
		Device: "test",
		Files: []applyEntry{
			{Artifact: "device.c", Path: "hw/misc/device.c", Action: ApplyCreate},
			{Artifact: "device.h", Path: "include/hw/misc/device.h", Action: ApplyCreate},
		},
	}
	injectEmitArtifacts(t, store, artifacts, project, manifest)

	result, err := applier.Apply(context.Background(), project.ID, scopeOf(project))
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Written) != 2 {
		t.Errorf("written = %d; want 2", len(result.Written))
	}

	calls := recorder.calls
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d; want 2", len(calls))
	}
	// The applier hands the tool a symlink-resolved path, which on macOS means
	// /private/var/... where t.TempDir() reported /var/..., so the expected prefix
	// has to be resolved the same way before comparing.
	resolvedRoot, err := filepath.EvalSymlinks(qemuRoot)
	if err != nil {
		t.Fatal(err)
	}
	for i, call := range calls {
		if call.name != "write" {
			t.Errorf("call %d: tool = %q; want write", i, call.name)
		}
		path, _ := call.args["file_path"].(string)
		if !strings.HasPrefix(path, resolvedRoot) {
			t.Errorf("call %d: path %q does not start with root %q", i, path, resolvedRoot)
		}
	}
}

// TestApplyDeniedByPolicyLeavesTreeUntouched ensures a write tool refusal does
// not leave partial output.
func TestApplyDeniedByPolicyLeavesTreeUntouched(t *testing.T) {
	root := t.TempDir()
	qemuRoot := filepath.Join(root, "qemu")
	if err := os.MkdirAll(filepath.Join(qemuRoot, "hw", "misc"), 0755); err != nil {
		t.Fatal(err)
	}

	store := openStore(t, filepath.Join(root, "modeling"), 10)
	artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 4096, 1<<20)
	denyingTools := &fakeTools{err: errors.New("policy denied write")}
	applier, project := setupApplier(t, qemuRoot, store, artifacts, denyingTools)

	manifest := applyManifest{
		Device: "test",
		Files: []applyEntry{{
			Artifact: "device.c",
			Path:     "hw/misc/device.c",
			Action:   ApplyCreate,
		}},
	}
	injectEmitArtifacts(t, store, artifacts, project, manifest)

	result, err := applier.Apply(context.Background(), project.ID, scopeOf(project))
	if err == nil {
		t.Fatal("apply succeeded; want error for denied write")
	}
	if !errors.Is(err, ErrApplyRejected) {
		t.Errorf("error %v; want ErrApplyRejected", err)
	}

	if len(result.Written) != 0 {
		t.Errorf("written = %v; want empty (nothing written before denial)", result.Written)
	}

	// The target must not exist
	target := filepath.Join(qemuRoot, "hw", "misc", "device.c")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target exists or stat failed: %v", err)
	}
}

// TestApplyPartialFailureIsReported ensures a failure after the first write
// returns an exact list of what landed.
func TestApplyPartialFailureIsReported(t *testing.T) {
	root := t.TempDir()
	qemuRoot := filepath.Join(root, "qemu")
	if err := os.MkdirAll(filepath.Join(qemuRoot, "hw", "misc"), 0755); err != nil {
		t.Fatal(err)
	}

	store := openStore(t, filepath.Join(root, "modeling"), 10)
	artifacts := openArtifacts(t, filepath.Join(root, "artifacts"), 4096, 1<<20)
	// Succeed once, then fail
	failAfterOne := &callCountTools{limit: 1, err: errors.New("disk full")}
	applier, project := setupApplier(t, qemuRoot, store, artifacts, failAfterOne)

	manifest := applyManifest{
		Device: "test",
		Files: []applyEntry{
			{Artifact: "first.c", Path: "hw/misc/first.c", Action: ApplyCreate},
			{Artifact: "second.c", Path: "hw/misc/second.c", Action: ApplyCreate},
			{Artifact: "third.c", Path: "hw/misc/third.c", Action: ApplyCreate},
		},
	}
	injectEmitArtifacts(t, store, artifacts, project, manifest)

	result, err := applier.Apply(context.Background(), project.ID, scopeOf(project))
	if err == nil {
		t.Fatal("apply succeeded; want partial failure")
	}
	if !errors.Is(err, ErrApplyPartial) {
		t.Errorf("error %v; want ErrApplyPartial", err)
	}

	if !result.Partial {
		t.Error("result.Partial = false; want true")
	}
	if len(result.Written) != 1 {
		t.Errorf("written = %v; want [hw/misc/first.c]", result.Written)
	} else if result.Written[0] != "hw/misc/first.c" {
		t.Errorf("written[0] = %q; want hw/misc/first.c", result.Written[0])
	}
	if len(result.Skipped) != 2 {
		t.Errorf("skipped = %v; want 2 files", result.Skipped)
	}

	// Only the first file should exist
	if _, err := os.Stat(filepath.Join(qemuRoot, "hw", "misc", "first.c")); err != nil {
		t.Errorf("first.c does not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(qemuRoot, "hw", "misc", "second.c")); !os.IsNotExist(err) {
		t.Errorf("second.c exists or stat failed: %v", err)
	}
}

// callCountTools succeeds for the first N calls, then fails.
type callCountTools struct {
	count int
	limit int
	err   error
}

func (c *callCountTools) Run(ctx context.Context, name string, args map[string]any) (tools.ExecutionResult, error) {
	c.count++
	if c.count > c.limit {
		return tools.ExecutionResult{}, c.err
	}
	// Simulate a successful write
	if name == "write" {
		path, _ := args["file_path"].(string)
		content, _ := args["content"].(string)
		if path != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return tools.ExecutionResult{}, err
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return tools.ExecutionResult{}, err
			}
		}
	}
	return tools.ExecutionResult{}, nil
}

// setupApplier creates an applier with a project that has emit artifacts ready.
func setupApplier(t *testing.T, qemuRoot string, store *FileProjectStore, artifacts *FileArtifactStore, toolRunner ToolRunner) (*FileApplier, Project) {
	t.Helper()

	if toolRunner == nil {
		toolRunner = &fakeTools{}
	}

	now := at(2000)
	applier, err := NewApplier(ApplierOptions{
		Projects:  store,
		Artifacts: artifacts,
		Tools:     toolRunner,
		QemuRoot:  qemuRoot,
		Now:       func() time.Time { return now },
		Logger:    testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	project, err := store.Create(context.Background(), Project{
		Title:       "test device",
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Advance to emit stage
	working := project.Clone()
	working.Current = StageEmit
	working.Status = StatusBlocked
	working.LastError = "awaiting_apply"
	working.Revision++
	project, err = store.Save(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}

	return applier, project
}

// injectEmitArtifacts commits the manifest, diff, and device files an apply needs,
// then saves the project with updated refs. Returns the saved project.
func injectEmitArtifacts(t *testing.T, store *FileProjectStore, artifacts *FileArtifactStore, project Project, manifest applyManifest) Project {
	t.Helper()

	manifestBody, err := encodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}

	drafts := []Draft{
		{Stage: StageEmit, Name: artifactApplyManifest, Kind: KindPlan, Body: manifestBody},
		{Stage: StageEmit, Name: artifactDeviceDiff, Kind: KindDiff, Body: []byte("diff placeholder")},
	}

	// Add the actual device file artifacts
	for _, entry := range manifest.Files {
		content := fmt.Sprintf("// generated content for %s\n", entry.Path)
		drafts = append(drafts, Draft{
			Stage: StageEmit,
			Name:  entry.Artifact,
			Kind:  KindCode,
			Body:  []byte(content),
		})
	}

	batch, err := artifacts.Stage(context.Background(), project.ID, drafts)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := artifacts.Commit(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}

	// Update project with these refs and save it
	working := project.Clone()
	if working.Artifacts == nil {
		working.Artifacts = make(map[Stage][]ArtifactRef)
	}
	working.Artifacts[StageEmit] = refs
	working.Revision++
	saved, err := store.Save(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}
