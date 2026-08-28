package runstream

// emitter.go owns the *producing* side of the protocol. It exists because there
// is now more than one producer: request runners emit run events and modeling
// pipeline adapters emit stage events. Both must agree on three things — a strictly increasing
// sequence, one identity per request, and a payload that matches the type — so
// those three rules live here instead of once per producer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Emitter is the write side of a request's event stream. A producer takes this
// interface, never an EventSink, so it cannot invent its own sequence numbers.
type Emitter interface {
	Emit(context.Context, Event) error
}

// NopEmitter is the disabled implementation. It exists so no producer contains
// `if events != nil`: a request with nobody listening gets this instead of nil.
// It still reports a canceled context, because "nobody is listening" must not
// turn into "cancellation is invisible".
type NopEmitter struct{}

func (NopEmitter) Emit(ctx context.Context, _ Event) error { return ctx.Err() }

var _ Emitter = NopEmitter{}

// NormalizeEmitter is called once, at the edge, so the rest of a call path can
// treat an emitter as always present.
func NormalizeEmitter(e Emitter) Emitter {
	if e == nil {
		return NopEmitter{}
	}
	return e
}

// MaxEventText bounds the free text of text_delta and stage_progress. It is the
// enforcement point for "events carry summaries, not payloads": a producer that
// tries to stream a register map or a build log through the event channel gets
// an error instead of a very large Telegram message.
const MaxEventText = 512

// EmitterOptions carries the per-request identity. Identity is a whole Event so
// that adding a correlation field later does not change this signature; only
// TraceID, SessionID, SessionKey and Channel are read from it.
type EmitterOptions struct {
	Sink     EventSink
	Identity Event
	Now      func() time.Time
}

// SequenceEmitter stamps and validates every event of one request. It is not
// safe for concurrent use on purpose: a request is one logical thread of
// notifications, and two goroutines emitting interleaved sequences would make
// the renderers' "sequence must increase" check meaningless.
type SequenceEmitter struct {
	sink EventSink
	base Event
	seq  uint64
	now  func() time.Time
}

var _ Emitter = (*SequenceEmitter)(nil)

func NewEmitter(opts EmitterOptions) (*SequenceEmitter, error) {
	if strings.TrimSpace(opts.Identity.SessionKey) == "" || strings.TrimSpace(opts.Identity.Channel) == "" {
		return nil, errors.New("run event identity is incomplete")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SequenceEmitter{
		sink: NormalizeSink(opts.Sink),
		base: Event{
			TraceID:   opts.Identity.TraceID,
			SessionID: opts.Identity.SessionID,
			// Identity is copied field by field rather than wholesale, so a caller
			// cannot smuggle a default Text or ToolName into every event.
			SessionKey: opts.Identity.SessionKey,
			Channel:    opts.Identity.Channel,
		},
		now: now,
	}, nil
}

// Emit overwrites the bookkeeping fields, validates the result, and only then
// hands the event to the sink. The order matters: a producer must not be able to
// forge a sequence number or a session id, and a malformed event must never
// reach a renderer, because renderers are allowed to hard-fail.
func (e *SequenceEmitter) Emit(ctx context.Context, event Event) error {
	e.seq++
	event.Sequence = e.seq
	event.At = e.now()
	event.TraceID = e.base.TraceID
	event.SessionID = e.base.SessionID
	event.SessionKey = e.base.SessionKey
	event.Channel = e.base.Channel
	if err := ValidateEvent(event); err != nil {
		return fmt.Errorf("validate run event: %w", err)
	}
	if err := e.sink.Emit(ctx, event); err != nil {
		return fmt.Errorf("emit %s event: %w", event.Type, err)
	}
	return nil
}

// ValidateEvent rejects an event whose payload does not match its type. This is
// a producer-side contract check, not input validation: every field that a
// renderer reads for a given type is required here, and every field it does not
// read must be empty, so a consumer never has to guess whether a value is
// meaningful.
func ValidateEvent(event Event) error {
	switch event.Type {
	case EventRunStarted, EventRunCompleted:
		if event.Turn != 0 || hasEventPayload(event) {
			return errors.New("run event contains unexpected payload")
		}
	case EventTurnStarted:
		if event.Turn <= 0 || hasEventPayload(event) {
			return errors.New("turn_started contains invalid payload")
		}
	case EventTextDelta:
		if event.Turn <= 0 || event.Text == "" || event.ToolCallID != "" || event.ToolName != "" || event.ToolOK != nil || event.ErrorKind != "" || event.Summary != "" || event.Stage != "" {
			return errors.New("text_delta contains invalid payload")
		}
	case EventToolStarted:
		if event.Turn <= 0 || event.ToolCallID == "" || event.ToolName == "" || event.Text != "" || event.ToolOK != nil || event.ErrorKind != "" || event.Summary != "" || event.Stage != "" {
			return errors.New("tool_started contains invalid payload")
		}
	case EventToolCompleted:
		if event.Turn <= 0 || event.ToolCallID == "" || event.ToolName == "" || event.Text != "" || event.ToolOK == nil || event.Stage != "" {
			return errors.New("tool_completed contains invalid payload")
		}
		if *event.ToolOK && (event.ErrorKind != "" || event.Summary != "") {
			return errors.New("successful tool event contains error summary")
		}
		if !*event.ToolOK && (event.ErrorKind == "" || event.Summary == "") {
			return errors.New("failed tool event lacks public error summary")
		}
	case EventRunFailed:
		if event.Turn != 0 || event.Text != "" || event.ToolCallID != "" || event.ToolName != "" || event.ToolOK != nil || event.ErrorKind == "" || event.Summary == "" || event.Stage != "" {
			return errors.New("run_failed contains invalid payload")
		}
	case EventStageStarted, EventStageProgress, EventStageCompleted:
		return validateStageEvent(event)
	default:
		return fmt.Errorf("unknown run event type %q", event.Type)
	}
	return nil
}

// validateStageEvent is split out because the three stage types share most of
// their rules: no turn, no tool fields, a stage name, and bounded text.
func validateStageEvent(event Event) error {
	if event.Stage == "" {
		return fmt.Errorf("%s has no stage", event.Type)
	}
	if event.Turn != 0 || event.ToolCallID != "" || event.ToolName != "" || event.ToolOK != nil {
		return fmt.Errorf("%s contains turn or tool payload", event.Type)
	}
	if len(event.Text) > MaxEventText || len(event.Summary) > MaxEventText {
		return fmt.Errorf("%s text exceeds the event budget", event.Type)
	}
	switch event.Type {
	case EventStageStarted:
		// A start says only that work began; anything else would be a claim about
		// a result that does not exist yet.
		if event.Text != "" || event.Summary != "" || event.ErrorKind != "" {
			return errors.New("stage_started contains unexpected payload")
		}
	case EventStageProgress:
		if event.Text == "" || event.ErrorKind != "" || event.Summary != "" {
			return errors.New("stage_progress contains invalid payload")
		}
	case EventStageCompleted:
		// A failed stage must carry a public category, the same rule tool_completed
		// and run_failed follow. Success may carry a summary line and nothing else;
		// Summary alone (without ErrorKind) means "finished but blocked".
		if event.ErrorKind != "" && event.Summary == "" {
			return errors.New("failed stage event lacks a public category")
		}
	}
	return nil
}

func hasEventPayload(event Event) bool {
	return event.Text != "" || event.ToolCallID != "" || event.ToolName != "" || event.ToolOK != nil || event.ErrorKind != "" || event.Summary != "" || event.Stage != ""
}
