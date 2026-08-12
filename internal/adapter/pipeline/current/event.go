package current

// event.go — StageEvent → pipelineapi.Event adapter (A3).
//
// 现有 modeling.Pipeline 通过 EventEmitter 发布 StageEvent。
// current Engine Adapter 把 pipelineapi.EventPublisher 包装为
// modeling.EventEmitter，并把 StageEvent 转换为 pipelineapi.Event。

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// eventAdapter 把 pipelineapi.EventPublisher 适配为 modeling.EventEmitter。
type eventAdapter struct {
	publisher pipelineapi.EventPublisher
	projectID pipelineapi.ProjectID
	operation pipelineapi.OperationName
}

// newEventAdapter 用 ExecuteRequest 的信息构造 eventAdapter。
func newEventAdapter(publisher pipelineapi.EventPublisher, projectID pipelineapi.ProjectID, operation pipelineapi.OperationName) *eventAdapter {
	return &eventAdapter{publisher: publisher, projectID: projectID, operation: operation}
}

// StageEvent 实现 modeling.EventEmitter。
//
// 把 modeling.StageEvent 转换为 pipelineapi.Event 并通过 publisher 发布。
// 转换规则：
//
//	stage_started   → operation_started
//	stage_progress  → operation_progress
//	stage_completed → operation_completed（OK=true → Result，false → Error）
func (a *eventAdapter) StageEvent(ctx context.Context, ev modeling.StageEvent) error {
	published := a.convert(ev)
	if published == nil {
		return nil
	}
	return a.publisher.Publish(ctx, *published)
}

func (a *eventAdapter) convert(ev modeling.StageEvent) *pipelineapi.Event {
	// 在第一个事件到来时锁定 project/operation（Engine 调用时已知）。
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
