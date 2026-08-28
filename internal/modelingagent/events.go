package modelingagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
)

var ErrEventDelivery = errors.New("modeling agent event delivery failed")

// EventBridge maps engine lifecycle notifications to the request run stream.
// It deliberately drops artifact descriptors and internal diagnostic messages.
type EventBridge struct {
	emitter runstream.Emitter
	logger  *slog.Logger
}

func NewEventBridge(emitter runstream.Emitter, logger *slog.Logger) *EventBridge {
	return &EventBridge{emitter: runstream.NormalizeEmitter(emitter), logger: logger}
}

var _ pipelineapi.EventPublisher = (*EventBridge)(nil)

func (b *EventBridge) Publish(ctx context.Context, evt pipelineapi.Event) error {
	if b == nil {
		return errors.New("modelingagent: event bridge is nil")
	}
	if strings.TrimSpace(string(evt.ProjectID)) == "" || strings.TrimSpace(string(evt.Operation)) == "" {
		return errors.New("modelingagent: pipeline event identity is incomplete")
	}

	out, err := projectRunEvent(evt)
	if err != nil {
		return err
	}
	if err := runstream.ValidateEvent(out); err != nil {
		return fmt.Errorf("modelingagent: invalid projected event: %w", err)
	}
	if err := b.emitter.Emit(ctx, out); err != nil {
		if b.logger != nil {
			b.logger.Warn("modeling event dropped", "kind", "event_delivery", "operation", evt.Operation)
		}
		return fmt.Errorf("%w: %w", ErrEventDelivery, err)
	}
	return nil
}

func projectRunEvent(evt pipelineapi.Event) (runstream.Event, error) {
	stage := string(evt.Operation)
	switch evt.Kind {
	case pipelineapi.EventOperationStarted:
		if evt.Progress != nil || evt.Result != nil || evt.Error != nil {
			return runstream.Event{}, errors.New("modelingagent: started event contains a result payload")
		}
		return runstream.Event{Type: runstream.EventStageStarted, Stage: stage}, nil

	case pipelineapi.EventOperationProgress:
		if evt.Progress == nil || evt.Result != nil || evt.Error != nil {
			return runstream.Event{}, errors.New("modelingagent: progress event payload is invalid")
		}
		if strings.TrimSpace(evt.Progress.Text) == "" || len(evt.Progress.Text) > runstream.MaxEventText {
			return runstream.Event{}, errors.New("modelingagent: progress text is invalid")
		}
		if evt.Progress.Current < 0 || evt.Progress.Total < 0 || (evt.Progress.Total > 0 && evt.Progress.Current > evt.Progress.Total) {
			return runstream.Event{}, errors.New("modelingagent: progress counters are invalid")
		}
		return runstream.Event{Type: runstream.EventStageProgress, Stage: stage, Text: evt.Progress.Text}, nil

	case pipelineapi.EventOperationCompleted:
		if (evt.Result == nil) == (evt.Error == nil) || evt.Progress != nil {
			return runstream.Event{}, errors.New("modelingagent: completed event must contain exactly one outcome")
		}
		if evt.Error != nil {
			kind, summary := publicEngineFailure(evt.Error.Category)
			return runstream.Event{Type: runstream.EventStageCompleted, Stage: stage, ErrorKind: kind, Summary: summary}, nil
		}
		switch evt.Result.Status {
		case pipelineapi.OpSucceeded:
			return runstream.Event{Type: runstream.EventStageCompleted, Stage: stage, Summary: "Modeling operation completed."}, nil
		case pipelineapi.OpBlocked:
			return runstream.Event{Type: runstream.EventStageCompleted, Stage: stage, Summary: "Modeling operation is awaiting input or approval."}, nil
		case pipelineapi.OpFailed:
			return runstream.Event{}, errors.New("modelingagent: failed completed event requires EngineError")
		default:
			return runstream.Event{}, errors.New("modelingagent: completed event has an unknown status")
		}
	default:
		return runstream.Event{}, fmt.Errorf("modelingagent: unknown pipeline event kind %q", evt.Kind)
	}
}

func publicEngineFailure(category string) (kind, summary string) {
	switch category {
	case "conflict":
		return "conflict", "The modeling project changed before the operation completed."
	case "model_failed":
		return "model_failed", "The modeling provider could not complete the operation."
	case "schema_invalid":
		return "schema_invalid", "The modeling output did not satisfy its schema."
	case "tool_denied":
		return "denied", "A required modeling action was denied."
	case "build_failed":
		return "build_failed", "The generated model did not pass verification."
	case "unavailable", "disabled", "stage_unavailable":
		return "unavailable", "The modeling capability is unavailable."
	default:
		return "internal", "The modeling operation could not be completed."
	}
}
