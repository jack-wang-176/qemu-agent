package current

import (
	"context"
	"errors"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type eventRecorder struct {
	events []pipelineapi.Event
	err    error
}

func (r *eventRecorder) Publish(_ context.Context, event pipelineapi.Event) error {
	r.events = append(r.events, event)
	return r.err
}

func TestEventAdapterPreservesIdentityAndFailure(t *testing.T) {
	recorder := &eventRecorder{err: errors.New("sink unavailable")}
	adapter := newEventAdapter(recorder, "project-1", "plan")
	err := adapter.StageEvent(context.Background(), modeling.StageEvent{Kind: modeling.EventStageStarted})
	if err == nil || len(recorder.events) != 1 {
		t.Fatalf("start event err=%v events=%d", err, len(recorder.events))
	}
	if recorder.events[0].ProjectID != "project-1" || recorder.events[0].Operation != "plan" {
		t.Fatalf("identity = %#v", recorder.events[0])
	}
	completed := adapter.convert(modeling.StageEvent{Kind: modeling.EventStageCompleted, OK: false, Reason: "schema_invalid", Text: "bounded failure"})
	if completed.Error == nil || completed.Error.Category != "schema_invalid" {
		t.Fatalf("completed error = %#v", completed)
	}
}
