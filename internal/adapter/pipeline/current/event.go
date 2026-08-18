package current

// event.go — StageEvent → pipelineapi.Event adapter .
//
// The current modeling.Pipeline publishes StageEvent through EventEmitter.
// The current Engine Adapter wraps pipelineapi.EventPublisher as a
// modeling.EventEmitter and converts StageEvent to pipelineapi.Event.

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// eventAdapter adapts pipelineapi.EventPublisher to modeling.EventEmitter.
type eventAdapter struct {
	publisher pipelineapi.EventPublisher
	projectID pipelineapi.ProjectID
	operation pipelineapi.OperationName
}

// newEventAdapter constructs an eventAdapter from ExecuteRequest identity.
func newEventAdapter(publisher pipelineapi.EventPublisher, projectID pipelineapi.ProjectID, operation pipelineapi.OperationName) *eventAdapter {
	return &eventAdapter{publisher: publisher, projectID: projectID, operation: operation}
}

// StageEvent implements modeling.EventEmitter.
//
// It converts modeling.StageEvent to pipelineapi.Event and publishes it.
// Mapping rules:
//
//	stage_started   → operation_started
//	stage_progress  → operation_progress
//	stage_completed -> operation_completed (OK=true -> Result, false -> Error)
func (a *eventAdapter) StageEvent(ctx context.Context, ev modeling.StageEvent) error {
	published := a.convert(ev)
	if published == nil {
		return nil
	}
	return a.publisher.Publish(ctx, *published)
}

func (a *eventAdapter) convert(ev modeling.StageEvent) *pipelineapi.Event {
	// Project and operation identity are fixed when the adapter is constructed.
	pid := a.projectID
	op := a.operation

	switch ev.Kind {
	case modeling.EventStageStarted:
		return &pipelineapi.Event{
			Kind:      pipelineapi.EventOperationStarted,
			ProjectID: pid,
			Operation: op,
		}
	case modeling.EventStageProgress:
		return &pipelineapi.Event{
			Kind:      pipelineapi.EventOperationProgress,
			ProjectID: pid,
			Operation: op,
			Progress: &pipelineapi.Progress{
				Text:    ev.Text,
				Current: 0,
				Total:   0,
			},
		}
	case modeling.EventStageCompleted:
		evt := &pipelineapi.Event{
			Kind:      pipelineapi.EventOperationCompleted,
			ProjectID: pid,
			Operation: op,
		}
		if ev.OK {
			evt.Result = &pipelineapi.ResultSummary{
				Status:  pipelineapi.OpSucceeded,
				Summary: ev.Text,
			}
			if ev.Blocked {
				evt.Result.Status = pipelineapi.OpBlocked
			}
		} else {
			evt.Error = &pipelineapi.EngineError{
				Category: ev.Reason,
				Message:  ev.Text,
			}
		}
		return evt
	default:
		return nil
	}
}

// Ensure eventAdapter satisfies modeling.EventEmitter at compile time.
var _ modeling.EventEmitter = (*eventAdapter)(nil)

// suppress unused import warning when context is only used in signatures.
var _ = context.Background
