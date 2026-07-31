package modeling

// stage_emit.go is the fourth stage: it turns the register IR into files and stops.
//
// Stopping is the feature. Emit is the last stage before the one action in this
// project that cannot be undone by deleting a project directory — writing into a
// QEMU source tree — so it writes its output into the artifact store, renders a diff
// for a human, and ends Blocked. `/modeling apply` is the only path from here to the
// tree, and it needs an interactive channel because somebody has to read that diff.
//
// The stage itself has no tools and no model call: everything it produces comes out
// of the Renderer, which is a pure function of the IR. That is what makes the bytes
// a reviewer approves the same bytes an apply writes.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Artifact names produced by emit. The applier looks the manifest up by name, so
// these constants are the contract between the two stages of a landing.
const (
	artifactDeviceDiff    = "device.diff"
	artifactApplyManifest = "apply-manifest.json"
)

// applyEntry maps one committed artifact to its destination in the source tree.
// It exists because the emit stage may not read QemuRoot: it can say "these bytes
// belong at hw/misc/x.c", but only the applier — which does read the tree — can turn
// that into a FileChange with a base digest.
type applyEntry struct {
	Artifact string      `json:"artifact"` // artifact name committed by this stage
	Path     string      `json:"path"`     // target path relative to QemuRoot
	Action   ApplyAction `json:"action"`   // create, or modify meaning "append"
}

// applyManifest is the whole landing description, as an artifact. Persisting it
// rather than recomputing paths later means a plan can be checked against what emit
// actually decided, instead of against whatever the current code would decide.
type applyManifest struct {
	Device string       `json:"device"`
	Files  []applyEntry `json:"files"`
}

// emitStage implements StageRunner for StageEmit.
//
// autoApply is a build-time decision, not a per-request one: a channel cannot ask
// for an unattended apply by setting a flag on a command, because the field is set
// once from configuration at wiring time.
type emitStage struct {
	renderer  Renderer
	autoApply bool
}

var _ StageRunner = emitStage{}

func NewEmitStage(renderer Renderer, autoApply bool) StageRunner {
	return emitStage{renderer: renderer, autoApply: autoApply}
}

func (emitStage) Name() Stage { return StageEmit }

// Run renders the device and commits the file set, the review diff and the manifest.
func (s emitStage) Run(_ context.Context, in StageInput) (StageOutput, error) {
	// 1: a renderer is a hard dependency; a build without one may not pretend to
	// have emitted anything.
	if s.renderer == nil {
		return StageOutput{}, fmt.Errorf("%w: this build has no code renderer", ErrStageUnavailable)
	}

	// 2: the IR is the only input, and loadRegIR re-validates it. Emit reads the
	// *latest* IR, which is infer's output when infer ran and extract's otherwise.
	ir, _, err := loadRegIR(in)
	if err != nil {
		return StageOutput{}, err
	}

	// 3: render. Pure function, so this is the whole generation step; a failure here
	// is a template that cannot express this IR, not a transient error.
	files, err := s.renderer.Render(ir)
	if err != nil {
		return StageOutput{}, err
	}
	if len(files) == 0 {
		return StageOutput{}, fmt.Errorf("%w: the renderer produced no files", ErrStageUnavailable)
	}

	// 4: one draft per rendered file, plus the manifest and the diff. The diff is a
	// first-class artifact rather than something /modeling diff computes, so the
	// object under review and the object that lands are the same bytes.
	drafts := make([]Draft, 0, len(files)+2)
	entries := make([]applyEntry, 0, len(files))
	for _, file := range files {
		drafts = append(drafts, Draft{Stage: StageEmit, Name: file.Name, Kind: file.Kind, Body: file.Body})
		entries = append(entries, applyEntry{Artifact: file.Name, Path: file.Path, Action: file.Action})
	}
	manifest, err := encodeManifest(applyManifest{Device: ir.Device, Files: entries})
	if err != nil {
		return StageOutput{}, err
	}
	drafts = append(drafts,
		Draft{Stage: StageEmit, Name: artifactApplyManifest, Kind: KindPlan, Body: manifest},
		Draft{Stage: StageEmit, Name: artifactDeviceDiff, Kind: KindDiff, Body: []byte(renderReviewDiff(ir, files))},
	)

	// 5: report. Blocked is not a failure — the artifacts are recorded and the
	// project waits on this same stage until a human applies or resets it.
	summary := fmt.Sprintf("generated %d file(s) for %s; run /modeling diff <id> to review and /modeling apply <id> to land",
		len(files), ir.Device)
	if s.autoApply {
		summary = fmt.Sprintf("generated %d file(s) for %s; auto-apply is on, so /modeling apply will not wait for review",
			len(files), ir.Device)
		return StageOutput{Artifacts: drafts, Summary: summary}, nil
	}
	return StageOutput{
		Artifacts: drafts, Summary: summary,
		Blocked: true, Reason: "awaiting_apply",
	}, nil
}

// encodeManifest renders the manifest deterministically: indented for a human
// reading it in the artifact directory, and newline-terminated so two renders of the
// same file set produce the same digest.
func encodeManifest(manifest applyManifest) ([]byte, error) {
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode apply manifest: %w", err)
	}
	return append(body, '\n'), nil
}

// decodeManifest is the applier's half. It is strict for the same reason every other
// decode in this package is: the manifest decides which paths get written, so a field
// nobody understands must be an error rather than a silent default.
func decodeManifest(body []byte) (applyManifest, error) {
	var manifest applyManifest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return applyManifest{}, fmt.Errorf("%w: the apply manifest does not decode", ErrSchemaInvalid)
	}
	if len(manifest.Files) == 0 {
		return applyManifest{}, fmt.Errorf("%w: the apply manifest lists no files", ErrSchemaInvalid)
	}
	return manifest, nil
}

// renderReviewDiff renders the file set the way a reviewer wants to read it.
//
// It is deliberately *not* a patch that `git apply` could consume: an append to
// meson.build has no line numbers until the moment it lands, and inventing them
// would produce a patch that applies to a file nobody verified. The header says so,
// so a reviewer never mistakes this for a machine-applicable diff.
func renderReviewDiff(ir RegIR, files []RenderedFile) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# qemu-agent review diff for device %s (bus %s)\n", ir.Device, ir.BusKind)
	out.WriteString("# This is a rendering for review, not a patch: `/modeling apply` writes the\n")
	out.WriteString("# artifacts themselves, and appends are placed at the end of the target file.\n")
	for _, file := range files {
		out.WriteString("\n")
		switch file.Action {
		case ApplyCreate:
			fmt.Fprintf(&out, "--- /dev/null\n+++ b/%s\n@@ new file, %d line(s) @@\n", file.Path, countLines(file.Body))
		default:
			fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n@@ appended at end of file, %d line(s) @@\n",
				file.Path, file.Path, countLines(file.Body))
		}
		for _, line := range strings.Split(strings.TrimRight(string(file.Body), "\n"), "\n") {
			out.WriteString("+" + line + "\n")
		}
	}
	return out.String()
}

// countLines counts the lines of a rendered body for the diff hunk header. A body
// always ends with a newline, so the trailing empty element is dropped.
func countLines(body []byte) int {
	trimmed := strings.TrimRight(string(body), "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}
