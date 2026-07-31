package modeling

// stage_emit_test.go covers the stage that stands between a generated device and a
// real QEMU tree. The properties under test are the ones that make the review step
// meaningful rather than decorative:
//
//   - emit ends Blocked and writes nothing outside the artifact store, so nothing
//     lands without `/modeling apply`;
//   - the same IR emits the same bytes, so the diff a human approves is the diff a
//     later apply writes;
//   - the manifest describes destinations inside the tree and nowhere else.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// emitInput gives the emit stage a stored register map, the same way the pipeline
// hands it infer's (or extract's) output.
func emitInput(t *testing.T, ir RegIR) StageInput {
	t.Helper()
	body, err := encodeRegIR(ir)
	if err != nil {
		t.Fatal(err)
	}
	// No Completer and no Executor: emit has no model call and no tools, and a test
	// that passed either would stop proving that.
	in := stageInput(StageEmit, nil, nil)
	return withArtifact(in, StageInfer, artifactRegIR, KindRegIR, body)
}

// TestEmitStopsAtBlockedWithoutAutoApply is the stop. The stage reports Blocked with
// the reason the CLI keys its "awaiting review" message off, and the QEMU tree it was
// told about is still byte-for-byte what it was.
func TestEmitStopsAtBlockedWithoutAutoApply(t *testing.T) {
	root := t.TempDir()
	before := treeSnapshot(t, root)

	in := emitInput(t, validIR())
	in.QemuRoot = root
	out, err := NewEmitStage(NewCRenderer(), false).Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Blocked || out.Reason != "awaiting_apply" {
		t.Fatalf("emit must block for review, got blocked=%v reason=%q", out.Blocked, out.Reason)
	}
	if got := treeSnapshot(t, root); got != before {
		t.Fatalf("emit touched the source tree:\nbefore %q\nafter  %q", before, got)
	}
	if !strings.Contains(out.Summary, "/modeling apply") {
		t.Fatalf("the summary must name the command that lands the change: %q", out.Summary)
	}
}

// TestEmitAutoApplyDoesNotBlock is the other half of the same switch: auto-apply is a
// build-time decision, and when it is on the stage completes so the applier can run
// without a human in the loop. It still writes nothing itself.
func TestEmitAutoApplyDoesNotBlock(t *testing.T) {
	root := t.TempDir()
	in := emitInput(t, validIR())
	in.QemuRoot = root

	out, err := NewEmitStage(NewCRenderer(), true).Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Blocked {
		t.Fatal("auto-apply must not leave the project waiting on a review it will not get")
	}
	if got := treeSnapshot(t, root); got != "" {
		t.Fatalf("even with auto-apply, emit itself may not write to the tree: %q", got)
	}
}

// TestEmitIsDeterministic is what makes a content address a review token: two runs
// over the same IR produce the same artifact names in the same order with the same
// bytes, so re-running emit before an apply cannot silently change what lands.
func TestEmitIsDeterministic(t *testing.T) {
	stage := NewEmitStage(NewCRenderer(), false)
	first, err := stage.Run(context.Background(), emitInput(t, validIR()))
	if err != nil {
		t.Fatal(err)
	}
	second, err := stage.Run(context.Background(), emitInput(t, validIR()))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Artifacts) != len(second.Artifacts) {
		t.Fatalf("artifact counts differ: %d vs %d", len(first.Artifacts), len(second.Artifacts))
	}
	for index := range first.Artifacts {
		if first.Artifacts[index].Name != second.Artifacts[index].Name {
			t.Fatalf("artifact %d: %q vs %q", index,
				first.Artifacts[index].Name, second.Artifacts[index].Name)
		}
		if string(first.Artifacts[index].Body) != string(second.Artifacts[index].Body) {
			t.Fatalf("artifact %s is not reproducible", first.Artifacts[index].Name)
		}
	}
}

// TestEmitCommitsDiffAndManifest: the diff is an artifact rather than something the
// CLI recomputes, and the manifest is the applier's whole instruction set — so both
// have to exist, and the manifest has to agree with the files that were committed.
func TestEmitCommitsDiffAndManifest(t *testing.T) {
	out, err := NewEmitStage(NewCRenderer(), false).Run(context.Background(), emitInput(t, validIR()))
	if err != nil {
		t.Fatal(err)
	}

	diff := findDraft(t, out, artifactDeviceDiff)
	if diff.Kind != KindDiff {
		t.Fatalf("the review diff must be a diff artifact, got %q", diff.Kind)
	}
	// The header exists so a reviewer never feeds this to `git apply`: an append has
	// no line numbers until it lands.
	if !strings.Contains(string(diff.Body), "not a patch") {
		t.Fatalf("the diff must say what it is:\n%s", diff.Body)
	}

	manifest, err := decodeManifest(findDraft(t, out, artifactApplyManifest).Body)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Device != "acme_uart" {
		t.Fatalf("manifest device = %q", manifest.Device)
	}
	committed := map[string]bool{}
	for _, draft := range out.Artifacts {
		committed[draft.Name] = true
	}
	for _, entry := range manifest.Files {
		// Every destination must be inside the tree and every source must be an
		// artifact this stage actually committed: a manifest naming bytes nobody
		// stored would fail halfway through an apply.
		if !committed[entry.Artifact] {
			t.Fatalf("manifest names %q, which was not committed", entry.Artifact)
		}
		if filepath.IsAbs(entry.Path) || strings.Contains(entry.Path, "..") {
			t.Fatalf("manifest path %q escapes the tree", entry.Path)
		}
		if entry.Action != ApplyCreate && entry.Action != ApplyModify {
			t.Fatalf("manifest action %q is neither create nor modify", entry.Action)
		}
	}
	if len(manifest.Files) != 3 {
		t.Fatalf("expected the device and the two build fragments, got %d", len(manifest.Files))
	}
}

// TestEmitRefusesWithoutARenderer: a build without a code generator may not pretend
// to have emitted anything, because the next command a user runs is `apply`.
func TestEmitRefusesWithoutARenderer(t *testing.T) {
	_, err := NewEmitStage(nil, false).Run(context.Background(), emitInput(t, validIR()))
	if got := Category(err); got != "stage_unavailable" {
		t.Fatalf("category = %q, want stage_unavailable", got)
	}
}

// TestEmitRefusesWithoutARegisterMap: emit is the only stage with no model call, so
// a missing IR is a pipeline-order error, not something to ask the model about.
func TestEmitRefusesWithoutARegisterMap(t *testing.T) {
	in := stageInput(StageEmit, nil, nil)
	_, err := NewEmitStage(NewCRenderer(), false).Run(context.Background(), in)
	if got := Category(err); got != "stage_unavailable" {
		t.Fatalf("category = %q, want stage_unavailable", got)
	}
}

// TestEmitRejectsAnInvalidStoredIR: the IR is re-validated on the way in, so an
// artifact written by an older binary fails here rather than reaching a template.
func TestEmitRejectsAnInvalidStoredIR(t *testing.T) {
	in := stageInput(StageEmit, nil, nil)
	in = withArtifact(in, StageInfer, artifactRegIR, KindRegIR,
		[]byte(`{"device":"acme_uart","bus_kind":"sysbus","mmio_size":4096,"registers":[]}`))
	_, err := NewEmitStage(NewCRenderer(), false).Run(context.Background(), in)
	if got := Category(err); got != "schema_invalid" {
		t.Fatalf("category = %q, want schema_invalid", got)
	}
}

// TestManifestDecodeIsStrict: the manifest decides which paths get written, so a
// field nobody understands has to be an error rather than a silent default.
func TestManifestDecodeIsStrict(t *testing.T) {
	if _, err := decodeManifest([]byte(`{"device":"x","files":[],"extra":1}`)); Category(err) != "schema_invalid" {
		t.Fatalf("an unknown manifest field must be rejected, got %v", err)
	}
	if _, err := decodeManifest([]byte(`{"device":"x","files":[]}`)); Category(err) != "schema_invalid" {
		t.Fatalf("a manifest with no files must be rejected, got %v", err)
	}
}

// treeSnapshot renders a directory tree as one comparable string. It is how the
// "nothing was written" assertions are made: comparing a listing catches a created
// file, a deleted one and a changed size alike.
func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out.WriteString(relative)
		if !info.IsDir() {
			out.WriteString(":" + strconv.FormatInt(info.Size(), 10))
		}
		out.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}
