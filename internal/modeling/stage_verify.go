package modeling

// stage_verify.go is the fifth and last stage: it turns "this looks right" into
// "here is a file that proves it".
//
// Verify produces no code. It runs two commands — a build and the device's qtest —
// and records what they printed, what they exited with, and when. Three rules shape
// the whole file:
//
//   - The commands are built here, from constants, with one substituted value: the
//     device name, and only after it matches a whitelist pattern. The model may
//     suggest what to run; that suggestion goes into a note, never into a command
//     line. This is the injection boundary of the modeling pipeline, because a
//     datasheet is untrusted input and the device name is derived from it.
//   - A failing build is a *result*, not a stage error: the evidence is committed
//     and the project blocks with "build_failed". Only being unable to run at all
//     — the tool denied the call, or it timed out — is a stage failure.
//   - The full output lands in an artifact; only a bounded tail reaches the project
//     record and the channel reply. That is the same rule the event text follows.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Artifact names produced by verify. build.log and qtest.log are the raw command
// output; evidence.json is the machine-readable index over them, and it is what
// `/modeling evidence` decodes.
const (
	artifactBuildLog    = "build.log"
	artifactQtestLog    = "qtest.log"
	artifactEvidence    = "evidence.json"
	evidenceKindBuild   = "build"
	evidenceKindQtest   = "qtest"
	maxEvidenceTailSize = 4 << 10 // 4 KiB: the only part of the output a human is shown
)

// ArtifactEvidenceName is the evidence index's name, exported because the command
// layer looks the artifact up by name to render it. It is the only artifact name
// that leaves this package: the others are read through the refs a project carries.
const ArtifactEvidenceName = artifactEvidence

// verifyDevicePattern is the whitelist that makes command templating safe. It is
// checked again here rather than trusted from RegIR.Validate, because this is the
// one place where a name from a datasheet becomes part of a command line: a name
// that cannot contain a quote, a space, a dollar sign or a slash cannot break out
// of the single-quoted argument it is substituted into, whatever the document said.
var verifyDevicePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// exitMarker is how the stage learns an exit code. The bash tool reports a non-zero
// exit as a Go error, which is indistinguishable from "the policy refused the call"
// without parsing a message — and parsing a message is exactly what this package
// refuses to do. So the command ends by printing its own status: the tool then
// always exits 0 for a command that *ran*, and any error from Run means the call
// itself did not happen.
const exitMarker = "qemu-agent-verify-exit:"

// Evidence is one executed command and its outcome. It is the whole point of the
// stage: a claim about a device that is not backed by one of these is a claim
// nobody can check.
type Evidence struct {
	Kind     string    `json:"kind"`      // "build" or "qtest"
	Command  string    `json:"command"`   // the exact command, from a template in this file
	ExitCode int       `json:"exit_code"` // as reported by the command itself
	OK       bool      `json:"ok"`        // ExitCode == 0
	Artifact string    `json:"artifact"`  // artifact holding the complete output
	Tail     string    `json:"tail"`      // bounded excerpt, the only part shown to a human
	At       time.Time `json:"at"`
}

// evidenceReport is the artifact body: the device it belongs to, plus one entry per
// command in the order they ran. It is a document rather than a log line because a
// reviewer wants to see the build and the test result together.
type evidenceReport struct {
	Device  string     `json:"device"`
	OK      bool       `json:"ok"` // true only when every command succeeded
	Records []Evidence `json:"records"`
}

// verifyStage implements StageRunner for StageVerify.
//
// buildDir is a build-time value, from ModelingConfig: a channel cannot ask for a
// build in a directory of its choosing, because that would put a path from a chat
// message into a command line. An empty buildDir makes the stage unavailable.
type verifyStage struct {
	buildDir string
}

var _ StageRunner = verifyStage{}

func NewVerifyStage(buildDir string) StageRunner {
	return verifyStage{buildDir: strings.TrimSpace(buildDir)}
}

func (verifyStage) Name() Stage { return StageVerify }

// Run builds, tests, and records. The numbered steps are the order the invariants
// depend on: validate the name before composing any command, run everything before
// judging anything, and commit the evidence whatever the verdict was.
func (s verifyStage) Run(ctx context.Context, in StageInput) (StageOutput, error) {
	// 1: configuration. Both of these are "this build cannot verify", not "this
	// device failed to verify", so they must not reach the evidence file.
	if s.buildDir == "" {
		return StageOutput{}, fmt.Errorf("%w: no QEMU build directory is configured", ErrStageUnavailable)
	}
	executor, err := requireExecutor(in)
	if err != nil {
		return StageOutput{}, err
	}

	// 2: the device name. It comes from the IR, which is model output about an
	// untrusted document, so it is validated *before* it is put anywhere near a
	// shell. A name that fails here produces no tool call at all.
	ir, _, err := loadRegIR(in)
	if err != nil {
		return StageOutput{}, err
	}
	device := strings.ToLower(strings.TrimSpace(ir.Device))
	if !verifyDevicePattern.MatchString(device) {
		return StageOutput{}, fmt.Errorf("%w: the device name is not a safe command argument", ErrSchemaInvalid)
	}

	// 3: run the two commands. Each returns evidence plus, only when the call could
	// not be made, an error — a non-zero exit is carried inside the evidence.
	records := make([]Evidence, 0, 2)
	drafts := make([]Draft, 0, 3)

	buildRecord, buildLog, err := s.run(ctx, in, executor, evidenceKindBuild, buildCommand(s.buildDir))
	if err != nil {
		return StageOutput{}, err
	}
	records = append(records, buildRecord)
	drafts = append(drafts, Draft{Stage: StageVerify, Name: artifactBuildLog, Kind: KindEvidence, Body: []byte(buildLog)})

	// The qtest only runs when the build succeeded: testing a binary that was not
	// rebuilt would produce evidence about the previous revision, which is worse
	// than no evidence.
	if buildRecord.OK {
		qtestRecord, qtestLog, qtestErr := s.run(ctx, in, executor, evidenceKindQtest, qtestCommand(s.buildDir, device))
		if qtestErr != nil {
			return StageOutput{}, qtestErr
		}
		records = append(records, qtestRecord)
		drafts = append(drafts, Draft{Stage: StageVerify, Name: artifactQtestLog, Kind: KindEvidence, Body: []byte(qtestLog)})
	}

	// 4: the report. It is committed on success and on failure alike, because a
	// failed build is the most useful thing this stage can record.
	report := evidenceReport{Device: device, OK: allOK(records), Records: records}
	body, err := encodeEvidence(report)
	if err != nil {
		return StageOutput{}, err
	}
	drafts = append(drafts, Draft{Stage: StageVerify, Name: artifactEvidence, Kind: KindEvidence, Body: body})

	// 5: the verdict. A failure blocks the project rather than erroring it: the
	// stage did its job, and the next action belongs to a human.
	if !report.OK {
		return StageOutput{
			Artifacts: drafts,
			Summary: fmt.Sprintf("verification of %s failed; %s. Run /modeling evidence to read the output",
				device, describe(records)),
			Blocked: true, Reason: classify(ErrBuildFailed),
		}, nil
	}
	return StageOutput{
		Artifacts: drafts,
		Summary:   fmt.Sprintf("verified %s: %s", device, describe(records)),
	}, nil
}

// run executes one command through the audited tool and turns the result into
// evidence. It returns an error only when the command did not run: a denied policy
// decision or a timeout is a stage failure, while a command that ran and failed is
// evidence with OK false.
func (s verifyStage) run(ctx context.Context, in StageInput, executor ToolRunner, kind, command string) (Evidence, string, error) {
	// A build can take minutes, so the stage says what it is doing. The text is a
	// fixed phrase plus the kind: no command line, no output, nothing derived from
	// the datasheet, because an event reaches every channel unfiltered.
	_ = in.Events.StageEvent(ctx, StageEvent{
		Kind: EventStageProgress, Project: in.Project.ID, Stage: StageVerify,
		Text: "running the " + kind + " step",
	})
	result, err := executor.Run(ctx, "bash", map[string]any{"command": command})
	if err != nil {
		// The message is dropped on purpose: it may quote the command output, and
		// the category is all the project record is allowed to carry.
		return Evidence{}, "", fmt.Errorf("%w: the %s command could not be run", ErrToolDenied, kind)
	}
	output, exitCode, found := splitExitMarker(result.ModelOutput)
	if !found {
		// No marker means the shell never reached the last line — killed, or the
		// tool truncated the output. Either way the exit status is unknown, and an
		// unknown status must not be reported as a pass.
		return Evidence{}, "", fmt.Errorf("%w: the %s command did not report an exit status", ErrToolDenied, kind)
	}
	return Evidence{
		Kind: kind, Command: command, ExitCode: exitCode, OK: exitCode == 0,
		Artifact: logArtifactName(kind), Tail: tailOf(output, maxEvidenceTailSize), At: in.Now,
	}, output, nil
}

// buildCommand is the build template. `cd || exit` rather than `cd &&` so a missing
// build directory is a normal non-zero exit with a readable message, and ninja is
// called without a target so the configured build's own default applies.
func buildCommand(buildDir string) string {
	return withExitMarker(fmt.Sprintf("cd %s || exit 66\nninja", shellQuote(buildDir)))
}

// qtestCommand runs only this device's test. The name is the dashed spelling, which
// is what QEMU's test suite uses, and it has already passed devicePattern; quoting
// it as well is belt and braces, because one template edit should not be able to
// reintroduce an injection.
func qtestCommand(buildDir, device string) string {
	target := strings.ReplaceAll(device, "_", "-") + "-test"
	return withExitMarker(fmt.Sprintf("cd %s || exit 66\nmeson test --suite qtest %s",
		shellQuote(buildDir), shellQuote(target)))
}

// withExitMarker wraps a command so it always exits 0 and prints its real status.
// See exitMarker: this is what keeps "the command failed" distinguishable from "the
// command was refused" without inspecting an error message.
func withExitMarker(command string) string {
	return fmt.Sprintf("{\n%s\n} 2>&1\nprintf '\\n%s%%d\\n' \"$?\"\nexit 0", command, exitMarker)
}

// shellQuote makes any string a single shell word. Single quotes disable every
// expansion bash has, and an embedded quote is closed, escaped and reopened — the
// standard construction, spelled out because getting it wrong here would undo the
// whitelist above.
func shellQuote(raw string) string {
	return "'" + strings.ReplaceAll(raw, "'", `'\''`) + "'"
}

// splitExitMarker separates the command's output from the status line the wrapper
// appended. It reads the *last* marker, because command output that happens to
// contain the marker text must not be able to fake a success.
func splitExitMarker(raw string) (string, int, bool) {
	index := strings.LastIndex(raw, exitMarker)
	if index < 0 {
		return raw, 0, false
	}
	digits := strings.TrimSpace(raw[index+len(exitMarker):])
	if newline := strings.IndexByte(digits, '\n'); newline >= 0 {
		digits = digits[:newline]
	}
	code, err := strconv.Atoi(strings.TrimSpace(digits))
	if err != nil {
		return raw, 0, false
	}
	return strings.TrimRight(raw[:index], "\n"), code, true
}

// tailOf keeps the end of a command's output, which is where a compiler or a test
// runner puts the reason it failed. The cut is on a rune boundary and on a line
// boundary, so the excerpt a channel renders is readable text.
func tailOf(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	cut := output[len(output)-limit:]
	if newline := strings.IndexByte(cut, '\n'); newline >= 0 && newline+1 < len(cut) {
		cut = cut[newline+1:]
	}
	// Drop a partial leading rune left by the byte-wise cut.
	for len(cut) > 0 && !utf8ValidStart(cut[0]) {
		cut = cut[1:]
	}
	return "[…truncated…]\n" + cut
}

// utf8ValidStart reports whether a byte can begin a UTF-8 sequence. It is spelled
// out rather than imported so the intent — "do not emit half a character" — is
// visible at the call site.
func utf8ValidStart(b byte) bool { return b&0xC0 != 0x80 }

// logArtifactName maps an evidence kind to the artifact holding its full output, so
// Evidence.Artifact and the committed draft cannot drift apart.
func logArtifactName(kind string) string {
	if kind == evidenceKindQtest {
		return artifactQtestLog
	}
	return artifactBuildLog
}

// allOK is the report verdict. An empty record set is not a pass: it would mean
// nothing ran.
func allOK(records []Evidence) bool {
	if len(records) == 0 {
		return false
	}
	for _, record := range records {
		if !record.OK {
			return false
		}
	}
	return true
}

// describe summarises the outcome for a human without quoting any output. Only the
// kind and the exit code appear, because a summary reaches a log line and a chat
// message, and command output may contain anything.
func describe(records []Evidence) string {
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, fmt.Sprintf("%s exited %d", record.Kind, record.ExitCode))
	}
	if len(parts) == 0 {
		return "nothing ran"
	}
	return strings.Join(parts, ", ")
}

// encodeEvidence writes the report deterministically, for the same reason the apply
// manifest is written that way: two identical runs must produce the same digest.
func encodeEvidence(report evidenceReport) ([]byte, error) {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode evidence: %w", err)
	}
	return append(body, '\n'), nil
}

// DecodeEvidence reads an evidence.json back. It is exported because the CLI's
// `/modeling evidence` renders it, and it is strict for the usual reason: a field
// this build does not understand means the artifact was written by another version,
// which a reviewer has to be told rather than shown a partial record of.
func DecodeEvidence(body []byte) (string, []Evidence, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var report evidenceReport
	if err := decoder.Decode(&report); err != nil {
		return "", nil, fmt.Errorf("%w: the evidence file does not decode", ErrSchemaInvalid)
	}
	return report.Device, report.Records, nil
}
