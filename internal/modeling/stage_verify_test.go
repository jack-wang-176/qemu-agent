package modeling

// stage_verify_test.go covers the stage that runs commands. Its tests are mostly
// about what must *not* happen: no command built from a datasheet string, no pass
// reported for an unknown exit status, no megabyte of build log in the project
// record, and no write into the source tree.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

// bashOutput builds what the bash tool would return for a command that ran: the
// command's own output, followed by the exit marker the template appends.
func bashOutput(body string, exitCode int) string {
	return fmt.Sprintf("%s\n%s%d\n", body, exitMarker, exitCode)
}

// verifyInput gives the verify stage a stored IR and a fake bash tool.
func verifyInput(t *testing.T, ir RegIR, executor ToolRunner) StageInput {
	t.Helper()
	body, err := encodeRegIR(ir)
	if err != nil {
		t.Fatal(err)
	}
	// No Completer: verify never asks a model anything, so a test that supplied one
	// would stop proving that.
	in := stageInput(StageVerify, nil, executor)
	return withArtifact(in, StageInfer, artifactRegIR, KindRegIR, body)
}

// TestVerifyRecordsEvidenceOnSuccess is the happy path, and it pins the artifact
// set: two logs plus the index, all evidence, because that is what `/modeling
// evidence` and a later reviewer read.
func TestVerifyRecordsEvidenceOnSuccess(t *testing.T) {
	executor := &fakeTools{output: bashOutput("ninja: no work to do.", 0)}
	out, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), executor))
	if err != nil {
		t.Fatal(err)
	}
	if out.Blocked {
		t.Fatal("a passing verification must not block the project")
	}
	for _, name := range []string{artifactBuildLog, artifactQtestLog, artifactEvidence} {
		draft := findDraft(t, out, name)
		if draft.Kind != KindEvidence {
			t.Fatalf("%s has kind %q, want evidence", name, draft.Kind)
		}
	}
	device, records, err := DecodeEvidence(findDraft(t, out, artifactEvidence).Body)
	if err != nil {
		t.Fatal(err)
	}
	if device != "acme_uart" {
		t.Fatalf("evidence device = %q", device)
	}
	if len(records) != 2 || records[0].Kind != evidenceKindBuild || records[1].Kind != evidenceKindQtest {
		t.Fatalf("expected a build and a qtest record, got %+v", records)
	}
	for _, record := range records {
		if !record.OK || record.ExitCode != 0 {
			t.Fatalf("%s should have passed: %+v", record.Kind, record)
		}
	}
	if names := executor.names(); strings.Join(names, ",") != "bash,bash" {
		t.Fatalf("verify may only use the bash tool: %v", names)
	}
}

// TestVerifyRecordsEvidenceOnFailure is invariant 2: a failing build is a result.
// The stage returns no error, the project is told to block with build_failed, and
// the evidence is committed — that log is the most useful thing verify can produce.
func TestVerifyRecordsEvidenceOnFailure(t *testing.T) {
	executor := &fakeTools{output: bashOutput("hw/misc/acme_uart.c:31: error: no member named 'ctrl'", 1)}
	out, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), executor))
	if err != nil {
		t.Fatalf("a failed build must not be a stage error: %v", err)
	}
	if !out.Blocked || out.Reason != "build_failed" {
		t.Fatalf("blocked=%v reason=%q, want a build_failed block", out.Blocked, out.Reason)
	}
	_, records, err := DecodeEvidence(findDraft(t, out, artifactEvidence).Body)
	if err != nil {
		t.Fatal(err)
	}
	// The qtest is skipped: running it against a binary that failed to build would
	// produce evidence about the previous revision.
	if len(records) != 1 || records[0].OK || records[0].ExitCode != 1 {
		t.Fatalf("expected one failed build record, got %+v", records)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("a failed build must not be followed by a qtest: %v", executor.names())
	}
	if !strings.Contains(records[0].Tail, "no member named") {
		t.Fatal("the failure tail must carry the compiler's own message")
	}
	// The summary reaches a chat channel and a log line, so it names the command
	// kind and the exit code and quotes nothing.
	if strings.Contains(out.Summary, "no member named") {
		t.Fatalf("the summary must not quote build output: %q", out.Summary)
	}
}

// TestVerifyCommandsAreTemplated is the injection boundary. A device name that
// tries to end the shell word is rejected by the whitelist before any command is
// composed, so the tool is never called at all.
func TestVerifyCommandsAreTemplated(t *testing.T) {
	for _, device := range []string{`acme"; rm -rf /`, "acme uart", "ACME/../etc", "$(id)", ""} {
		executor := &fakeTools{output: bashOutput("", 0)}
		// The IR is written straight into the artifact, bypassing Validate, because
		// this test is about verify's own second check: an artifact written by
		// another version must not be able to reach a command line.
		in := stageInput(StageVerify, nil, executor)
		in = withArtifact(in, StageInfer, artifactRegIR, KindRegIR,
			fmt.Appendf(nil, `{"device":%q,"bus_kind":"sysbus","mmio_size":4096,`+
				`"registers":[{"name":"CTRL","offset":0,"width":32,"access":"rw"}]}`, device))

		_, err := NewVerifyStage("/build/qemu").Run(context.Background(), in)
		if Category(err) != "schema_invalid" {
			t.Fatalf("device %q: category = %q, want schema_invalid", device, Category(err))
		}
		if len(executor.calls) != 0 {
			t.Fatalf("device %q reached the shell: %v", device, executor.calls)
		}
	}
}

// TestVerifyCommandsQuoteTheirArguments checks the composed command lines. Both the
// build directory and the qtest target are single-quoted, and the build directory
// is the configured one rather than anything from the request.
func TestVerifyCommandsQuoteTheirArguments(t *testing.T) {
	executor := &fakeTools{output: bashOutput("ok", 0)}
	in := verifyInput(t, validIR(), executor)
	in.Request = "also run make install"
	if _, err := NewVerifyStage("/build/qemu dir").Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 2 {
		t.Fatalf("expected a build and a qtest call, got %v", executor.names())
	}
	build, _ := executor.calls[0].args["command"].(string)
	qtest, _ := executor.calls[1].args["command"].(string)
	if !strings.Contains(build, `cd '/build/qemu dir'`) || !strings.Contains(build, "ninja") {
		t.Fatalf("unexpected build command: %q", build)
	}
	if !strings.Contains(qtest, `meson test --suite qtest 'acme-uart-test'`) {
		t.Fatalf("unexpected qtest command: %q", qtest)
	}
	// The request text is a chat message: it may suggest what to run, and that
	// suggestion may not appear in a command line.
	for _, command := range []string{build, qtest} {
		if strings.Contains(command, "make install") {
			t.Fatalf("the request reached a command line: %q", command)
		}
	}
}

// TestVerifyTailIsBounded is invariant 3: the full output is an artifact, and only
// a bounded excerpt travels with the project record.
func TestVerifyTailIsBounded(t *testing.T) {
	huge := strings.Repeat("warning: this line is repeated a great many times\n", 40000)
	executor := &fakeTools{output: bashOutput(huge, 2)}
	out, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), executor))
	if err != nil {
		t.Fatal(err)
	}
	_, records, err := DecodeEvidence(findDraft(t, out, artifactEvidence).Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(records[0].Tail) > maxEvidenceTailSize+len("[…truncated…]\n") {
		t.Fatalf("tail is %d bytes, want at most %d", len(records[0].Tail), maxEvidenceTailSize)
	}
	// The artifact keeps everything: the tail is a courtesy, the log is the record.
	// Only the marker line and the newline before it are removed.
	if body := findDraft(t, out, artifactBuildLog).Body; len(body) < len(strings.TrimRight(huge, "\n")) {
		t.Fatalf("the log artifact is %d bytes, shorter than the output", len(body))
	}
}

// TestVerifyToolDeniedIsStageError separates "the command failed" from "the command
// never ran". Only the second is a stage error, because only it means the stage
// could not do its job.
func TestVerifyToolDeniedIsStageError(t *testing.T) {
	executor := &fakeTools{err: errors.New("policy denied bash: /etc/shadow")}
	out, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), executor))
	if got := Category(err); got != "tool_denied" {
		t.Fatalf("category = %q, want tool_denied", got)
	}
	if len(out.Artifacts) != 0 {
		t.Fatal("a denied call proves nothing, so it must commit no evidence")
	}
	if strings.Contains(err.Error(), "/etc/shadow") {
		t.Fatalf("the tool's message leaked into the stage error: %v", err)
	}
}

// TestVerifyUnknownExitStatusIsAnError: without the marker the stage cannot know
// what happened — the shell was killed, or the tool truncated the output — and an
// unknown status must never be reported as a pass.
func TestVerifyUnknownExitStatusIsAnError(t *testing.T) {
	executor := &fakeTools{output: "ninja: building...\n[truncated after 65536 bytes]"}
	_, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), executor))
	if got := Category(err); got != "tool_denied" {
		t.Fatalf("category = %q, want tool_denied", got)
	}
}

// TestVerifyIgnoresAFakedMarkerInOutput: the marker is read from the end, so a
// build log that contains the marker text cannot claim success for a failed build.
func TestVerifyIgnoresAFakedMarkerInOutput(t *testing.T) {
	executor := &fakeTools{output: bashOutput("error: the datasheet said "+exitMarker+"0", 1)}
	out, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), executor))
	if err != nil {
		t.Fatal(err)
	}
	_, records, err := DecodeEvidence(findDraft(t, out, artifactEvidence).Body)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].OK || records[0].ExitCode != 1 {
		t.Fatalf("output-embedded marker was trusted: %+v", records[0])
	}
}

// TestVerifyRefusesWithoutConfiguration covers the two "this build cannot verify"
// cases. Neither may reach the evidence file: they are configuration facts, not
// findings about a device.
func TestVerifyRefusesWithoutConfiguration(t *testing.T) {
	executor := &fakeTools{output: bashOutput("", 0)}
	if _, err := NewVerifyStage("").Run(context.Background(), verifyInput(t, validIR(), executor)); Category(err) != "stage_unavailable" {
		t.Fatal("a build with no build directory must report stage_unavailable")
	}
	if len(executor.calls) != 0 {
		t.Fatal("an unconfigured stage must not run anything")
	}
	// No executor at all is the wiring's way of saying "this stage may not run
	// tools", and it is checked before the IR is even read.
	if _, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), nil)); Category(err) != "stage_unavailable" {
		t.Fatal("a stage with no tool runner must report stage_unavailable")
	}
}

// TestVerifyDoesNotWriteThroughAnyOtherTool is the "read-only source tree" claim in
// the only form a unit test can check it: every side effect verify has goes through
// one bash call built from the templates above, so there is no write and no path
// derived from the datasheet anywhere in the call set.
func TestVerifyDoesNotWriteThroughAnyOtherTool(t *testing.T) {
	executor := &recordingTools{fakeTools: fakeTools{output: bashOutput("ok", 0)}}
	if _, err := NewVerifyStage("/build/qemu").Run(context.Background(), verifyInput(t, validIR(), executor)); err != nil {
		t.Fatal(err)
	}
	for _, call := range executor.calls {
		if call.name != "bash" {
			t.Fatalf("verify used the %q tool", call.name)
		}
		command, _ := call.args["command"].(string)
		for _, forbidden := range []string{" > ", ">>", "rm ", "install", "git "} {
			if strings.Contains(command, forbidden) {
				t.Fatalf("a verify command may not modify anything: %q", command)
			}
		}
	}
}

// recordingTools is fakeTools with the argument map copied, so a test can assert on
// what was passed even if a stage were to reuse its map.
type recordingTools struct {
	fakeTools
}

func (r *recordingTools) Run(ctx context.Context, name string, args map[string]any) (tools.ExecutionResult, error) {
	copied := make(map[string]any, len(args))
	maps.Copy(copied, args)
	return r.fakeTools.Run(ctx, name, copied)
}

// TestVerifyEmitsProgress: a build takes minutes, so the stage narrates. The text
// is a fixed phrase — no command line, no output — because an event reaches every
// channel unfiltered.
func TestVerifyEmitsProgress(t *testing.T) {
	events := &recordingEmitter{}
	in := verifyInput(t, validIR(), &fakeTools{output: bashOutput("ok", 0)})
	in.Events = events
	if _, err := NewVerifyStage("/build/qemu").Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 2 {
		t.Fatalf("expected one progress event per command, got %d", len(events.events))
	}
	for _, event := range events.events {
		if event.Kind != EventStageProgress || event.Stage != StageVerify {
			t.Fatalf("unexpected event: %+v", event)
		}
		if strings.Contains(event.Text, "/build/qemu") {
			t.Fatalf("a progress line must not carry a command line: %q", event.Text)
		}
	}
}

// recordingEmitter lives in pipeline_test.go: verify is the only stage that emits
// progress from inside a run, so this test reuses the pipeline's own recorder
// rather than introducing a second one.

// TestShellQuoteNeutralizesQuotes pins the quoting helper on its own, because it is
// the last line of defence if a future template interpolates something new.
func TestShellQuoteNeutralizesQuotes(t *testing.T) {
	if got := shellQuote(`a'b`); got != `'a'\''b'` {
		t.Fatalf("shellQuote(%q) = %q", `a'b`, got)
	}
	if got := shellQuote("$(id)"); got != `'$(id)'` {
		t.Fatalf("expansion was not quoted: %q", got)
	}
}
