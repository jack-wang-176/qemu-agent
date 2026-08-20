package runtime

// event.go — Event Port Adapter (A4).
//
// Adapts pipelineapi.EventPublisher to modeling.EventEmitter for reuse by the
// current Pipeline.
//
// The current Engine Adapter (A3) already provides the forward bridge. This
// file provides the reverse bridge for tests or future Pipeline consumers.

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// EventPublisherAdapter wraps pipelineapi.EventPublisher as modeling.EventEmitter.
//
// It is used when the current Pipeline requires modeling.EventEmitter while
// the composition root exposes only pipelineapi.EventPublisher.
type EventPublisherAdapter struct {
	publisher pipelineapi.EventPublisher
	projectID string
	stage     modeling.Stage
}

// NewEventPublisherAdapter constructs the adapter. The caller supplies
// projectID and stage before the first event.
func NewEventPublisherAdapter(
	publisher pipelineapi.EventPublisher,
	projectID string,
	stage modeling.Stage,
) *EventPublisherAdapter {
	return &EventPublisherAdapter{
		publisher: publisher,
		projectID: projectID,
		stage:     stage,
	}
}

// StageEvent implements modeling.EventEmitter.
func (a *EventPublisherAdapter) StageEvent(ctx context.Context, ev modeling.StageEvent) error {
	published := a.convert(ev)
	if published == nil {
		return nil
	}
	return a.publisher.Publish(ctx, *published)
}

func (a *EventPublisherAdapter) convert(ev modeling.StageEvent) *pipelineapi.Event {
	switch ev.Kind {
	case modeling.EventStageStarted:
		return &pipelineapi.Event{
			Kind:      pipelineapi.EventOperationStarted,
			ProjectID: pipelineapi.ProjectID(a.projectID),
			Operation: pipelineapi.OperationName(string(a.stage)),
		}
	case modeling.EventStageProgress:
		return &pipelineapi.Event{
			Kind:      pipelineapi.EventOperationProgress,
			ProjectID: pipelineapi.ProjectID(a.projectID),
			Operation: pipelineapi.OperationName(string(a.stage)),
			Progress: &pipelineapi.Progress{
				Text:    ev.Text,
				Current: 0,
				Total:   0,
			},
		}
	case modeling.EventStageCompleted:
		evt := &pipelineapi.Event{
			Kind:      pipelineapi.EventOperationCompleted,
			ProjectID: pipelineapi.ProjectID(a.projectID),
			Operation: pipelineapi.OperationName(string(a.stage)),
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

// Ensure EventPublisherAdapter satisfies modeling.EventEmitter at compile time.
var _ modeling.EventEmitter = (*EventPublisherAdapter)(nil)

// parseArgsToMap is implemented in args.go using encoding/json; the old JSON
// helper implementations intentionally remain removed to avoid duplication.

// suppress unused import warning when context is only used in signatures.
var _ = context.Background
