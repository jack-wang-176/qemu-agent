package modeling

// stage.go declares the contract between the Pipeline and the five stages.
//
// The split is the whole design: a stage is pure business logic that returns
// *drafts*, and the Pipeline is the only code that writes them, changes project
// state or emits events. A stage that reaches for a ProjectStore, an
// ArtifactStore, os/exec or net/http is a design error, not a shortcut — the two
// legal side-effect exits are Completer.Complete (ask the model) and
// ToolRunner.Run (run an audited tool).
//
// That is the same rule I7 applies to the Agent: the Agent never calls the
// Extractor, because the component that produces content must not also be the
// component that decides to persist it.

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

// StageInput is everything a stage may see. It is a snapshot: mutating Project
// has no effect, because the Pipeline works on its own clone.
type StageInput struct {
	Project   Project                 // read-only snapshot of the project being advanced
	Stage     Stage                   // which stage is running; a runner may assert on it
	Request   string                  // the human's request text, as typed; untrusted data
	Sources   []string                // datasheet/header paths the user named, read via the tool
	Workspace string                  // Paths.Workspace: the only tree tools may touch
	QemuRoot  string                  // may be empty; a stage decides whether it can work
	Inputs    map[Stage][]ArtifactRef // references produced by earlier stages
	Open      OpenFunc                // reads one of *this* project's artifacts, digest re-checked
	Completer Completer               // nil means this stage is not allowed to call a model
	Executor  ToolRunner              // nil means this stage is not allowed to run tools
	Events    EventEmitter            // never nil; NopEmitter when nobody listens
	Now       time.Time               // one timestamp per stage run, so output is reproducible
}

// OpenFunc is the only way a stage reads bytes. The Pipeline binds it to one
// project, so a stage cannot ask for another project's artifacts even if it
// somehow obtained a ref.
type OpenFunc func(ArtifactRef) (io.ReadCloser, error)

// StageOutput is what a stage returns. Nothing here is on disk yet.
//
// A stage may return Evidence *together with an error*. That is the one case
// where output survives a failure, and it exists for schema failures: the raw
// model reply is the only thing that makes "schema_invalid" diagnosable, so it is
// committed as evidence while the project record still holds nothing but the
// category. Artifacts of a failed stage are always discarded — a register map
// that did not validate must never become an input.
type StageOutput struct {
	Artifacts []Draft // committed by the Pipeline, in this order
	Evidence  []Draft // same, but must carry KindEvidence; also committed on failure
	Summary   string  // one paragraph for humans; never contains artifact bodies
	Blocked   bool    // true = a human has to act (Emit awaiting apply); not a failure
	Reason    string  // category explaining Blocked, e.g. "awaiting_apply"
}

// StageRunner is one step of the pipeline. It is called StageRunner rather than
// Stage because Stage is already the name of the value type that identifies a
// step, and two meanings for one name would make the registry unreadable.
type StageRunner interface {
	Name() Stage
	Run(ctx context.Context, in StageInput) (StageOutput, error)
}

// Completer is the single-shot model call, the same shape memory's extractor
// uses: no tools, no session, no history. A stage that needs two calls makes two
// calls; it never opens a conversation.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// ToolRunner is the adapter over security.Executor. The modeling package
// deliberately does not import the security package: it wants a tool call to be
// indistinguishable from any other audited side effect, and it must not be able
// to construct a policy decision itself.
type ToolRunner interface {
	Run(ctx context.Context, name string, args map[string]any) (tools.ExecutionResult, error)
}

// Stage-level failure categories. Stages wrap their errors with these so
// classify() can turn a failure into a category without ever inspecting a
// message — the message may quote a datasheet, the category never does.
var (
	ErrModelFailed      = errors.New("modeling stage could not reach the model")
	ErrSchemaInvalid    = errors.New("modeling stage output did not match its schema")
	ErrToolDenied       = errors.New("modeling tool call was denied")
	ErrBuildFailed      = errors.New("modeling build or test failed")
	ErrStageUnavailable = errors.New("modeling stage cannot run in this configuration")
)

// EventEmitter is the progress channel of a stage run. It is declared here, in
// terms of modeling's own event value, so this package stays unaware of
// runstream, channels and sessions; the app layer adapts StageEvent to whatever
// the request's event protocol is.
type EventEmitter interface {
	StageEvent(ctx context.Context, event StageEvent) error
}

// EventKind is the three-state lifecycle of one stage run. There is no "failed"
// kind: a failure is a completed stage with OK false, so a consumer that only
// tracks starts and completions can never leak a run.
type EventKind string

const (
	EventStageStarted   EventKind = "stage_started"
	EventStageProgress  EventKind = "stage_progress"
	EventStageCompleted EventKind = "stage_completed"
)

// StageEvent is a notification, not a data channel: a client that saw
// stage_completed still has to call /modeling show to read anything. That is why
// Text is capped and why losing an event cannot make the result wrong.
type StageEvent struct {
	Kind    EventKind
	Project string
	Stage   Stage
	Text    string // bounded summary; never artifact content or tool output
	OK      bool   // only meaningful for EventStageCompleted
	Blocked bool   // completed, but waiting for a human
	// Reason is the category behind Blocked or a failure, e.g. "awaiting_apply".
	// It is separate from Text because a consumer has to be able to word "waiting
	// for you" differently from "this went wrong" without parsing a summary.
	Reason string
}

// maxEventText bounds what a progress line may carry. The limit is the enforcement
// point for "events carry summaries, not payloads": a stage that tries to stream a
// register map through the event channel gets it truncated instead.
const maxEventText = 240

// NopEmitter is the disabled implementation. It exists so no stage and no
// pipeline branch contains `if events != nil` — the same reason the memory layer
// has a DisabledStore instead of a nil check.
type NopEmitter struct{}

func (NopEmitter) StageEvent(context.Context, StageEvent) error { return nil }

var _ EventEmitter = NopEmitter{}

// normalizeEmitter is called once per run, so the rest of the package can treat
// Events as always present.
func normalizeEmitter(emitter EventEmitter) EventEmitter {
	if emitter == nil {
		return NopEmitter{}
	}
	return emitter
}

// summaryLine reduces a stage summary to something safe to render: one line,
// bounded length. A stage is trusted to write a sensible sentence, but the bound
// is enforced here rather than hoped for, because this string reaches a Telegram
// message and a log line.
func summaryLine(raw string) string {
	collapsed := strings.Join(strings.Fields(strings.ReplaceAll(raw, "\n", " ")), " ")
	if len(collapsed) <= maxEventText {
		return collapsed
	}
	// Cut on a rune boundary: a truncated multi-byte character would render as a
	// replacement glyph in every channel.
	runes := []rune(collapsed)
	if len(runes) > maxEventText {
		runes = runes[:maxEventText]
	}
	return strings.TrimSpace(string(runes)) + "…"
}
