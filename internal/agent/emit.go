package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type emitter struct {
	sink runstream.EventSink
	base runstream.Event
	seq  uint64
	now  func() time.Time
}

var ErrEventDelivery = errors.New("run event delivery failed")

func newEmitter(input RunInput, s *session.Session, now func() time.Time) (*emitter, error) {
	if s == nil {
		return nil, errors.New("event session is nil")
	}
	if strings.TrimSpace(input.SessionKey) == "" || strings.TrimSpace(input.Channel) == "" {
		return nil, errors.New("event request identity is incomplete")
	}
	if now == nil {
		now = time.Now
	}
	return &emitter{
		sink: runstream.NormalizeSink(input.Events),
		base: runstream.Event{TraceID: s.TraceID, SessionID: s.ID, SessionKey: input.SessionKey, Channel: input.Channel},
		now:  now,
	}, nil
}

func (e *emitter) Emit(ctx context.Context, event runstream.Event) error {
	e.seq++
	event.Sequence = e.seq
	event.At = e.now()
	event.SessionID = e.base.SessionID
	event.TraceID = e.base.TraceID
	event.SessionKey = e.base.SessionKey
	event.Channel = e.base.Channel
	if err := validateEvent(event); err != nil {
		return fmt.Errorf("validate run event: %w", err)
	}
	if err := e.sink.Emit(ctx, event); err != nil {
		return fmt.Errorf("emit %s event: %w", event.Type, err)
	}
	return nil
}

func validateEvent(event runstream.Event) error {
	switch event.Type {
	case runstream.EventRunStarted, runstream.EventRunCompleted:
		if event.Turn != 0 || hasEventPayload(event) {
			return errors.New("run event contains unexpected payload")
		}
	case runstream.EventTurnStarted:
		if event.Turn <= 0 || hasEventPayload(event) {
			return errors.New("turn_started contains invalid payload")
		}
	case runstream.EventTextDelta:
		if event.Turn <= 0 || event.Text == "" || event.ToolCallID != "" || event.ToolName != "" || event.ToolOK != nil || event.ErrorKind != "" || event.Summary != "" {
			return errors.New("text_delta contains invalid payload")
		}
	case runstream.EventToolStarted:
		if event.Turn <= 0 || event.ToolCallID == "" || event.ToolName == "" || event.Text != "" || event.ToolOK != nil || event.ErrorKind != "" || event.Summary != "" {
			return errors.New("tool_started contains invalid payload")
		}
	case runstream.EventToolCompleted:
		if event.Turn <= 0 || event.ToolCallID == "" || event.ToolName == "" || event.Text != "" || event.ToolOK == nil {
			return errors.New("tool_completed contains invalid payload")
		}
		if *event.ToolOK && (event.ErrorKind != "" || event.Summary != "") {
			return errors.New("successful tool event contains error summary")
		}
		if !*event.ToolOK && (event.ErrorKind == "" || event.Summary == "") {
			return errors.New("failed tool event lacks public error summary")
		}
	case runstream.EventRunFailed:
		if event.Turn != 0 || event.Text != "" || event.ToolCallID != "" || event.ToolName != "" || event.ToolOK != nil || event.ErrorKind == "" || event.Summary == "" {
			return errors.New("run_failed contains invalid payload")
		}
	default:
		return fmt.Errorf("unknown run event type %q", event.Type)
	}
	return nil
}

func hasEventPayload(event runstream.Event) bool {
	return event.Text != "" || event.ToolCallID != "" || event.ToolName != "" || event.ToolOK != nil || event.ErrorKind != "" || event.Summary != ""
}
