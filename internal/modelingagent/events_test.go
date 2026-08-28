package modelingagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

type eventBridgeEmitter struct {
	events []runstream.Event
	err    error
}

func (e *eventBridgeEmitter) Emit(_ context.Context, event runstream.Event) error {
	e.events = append(e.events, event)
	return e.err
}

func TestEventBridgeDropsInternalErrorMessage(t *testing.T) {
	emitter := &eventBridgeEmitter{}
	bridge := NewEventBridge(emitter, nil)
	raw := "raw-model-response /secret/path provider-token"
	err := bridge.Publish(context.Background(), pipelineapi.Event{
		Kind: pipelineapi.EventOperationCompleted, ProjectID: "project", Operation: "verify",
		Error: &pipelineapi.EngineError{Category: "schema_invalid", Message: raw},
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if len(emitter.events) != 1 || emitter.events[0].Type != runstream.EventStageCompleted {
		t.Fatalf("events = %#v", emitter.events)
	}
	event := emitter.events[0]
	if event.ErrorKind != "schema_invalid" || strings.Contains(event.Summary, raw) {
		t.Fatalf("event exposed unsafe data: %#v", event)
	}
}

func TestEventBridgeTreatsBlockedAsCompletedNotification(t *testing.T) {
	emitter := &eventBridgeEmitter{}
	bridge := NewEventBridge(emitter, nil)
	err := bridge.Publish(context.Background(), pipelineapi.Event{
		Kind: pipelineapi.EventOperationCompleted, ProjectID: "project", Operation: "emit",
		Result: &pipelineapi.ResultSummary{Status: pipelineapi.OpBlocked, Summary: "ignored"},
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if got := emitter.events[0]; got.Type != runstream.EventStageCompleted || got.ErrorKind != "" {
		t.Fatalf("blocked event = %#v", got)
	}
}

func TestEventBridgePreservesDeliveryCause(t *testing.T) {
	cause := errors.New("sink failed")
	bridge := NewEventBridge(&eventBridgeEmitter{err: cause}, nil)
	err := bridge.Publish(context.Background(), pipelineapi.Event{
		Kind: pipelineapi.EventOperationStarted, ProjectID: "project", Operation: "plan",
	})
	if !errors.Is(err, ErrEventDelivery) || !errors.Is(err, cause) {
		t.Fatalf("Publish() error = %v", err)
	}
}
