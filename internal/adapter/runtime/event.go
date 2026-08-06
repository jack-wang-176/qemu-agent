package runtime

// event.go — Event Port Adapter (A4).
//
// 把 pipelineapi.EventPublisher 的发布能力适配为
// modeling.EventEmitter，并在 current Pipeline 复用同一 publisher。
//
// 实际上 current Engine Adapter (A3) 已经在 event.go 中实现了
// modeling.EventEmitter → pipelineapi.EventPublisher 的适配。
// 本文件提供反向适配（pipelineapi.Event → modeling.StageEvent），
// 供 testing 或未来 Pipeline 复用现有 EventEmitter 消费者。

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

// EventPublisherAdapter 把 pipelineapi.EventPublisher 包装为 modeling.EventEmitter。
//
// 使用场景：current Pipeline 期望一个 modeling.EventEmitter；
// 组合根只有 pipelineapi.EventPublisher 可用；
// 本 Adapter 完成两者桥接。
type EventPublisherAdapter struct {
	publisher pipelineapi.EventPublisher
	projectID string
	stage     modeling.Stage
}

// NewEventPublisherAdapter 构造 adapter。
// projectID 和 stage 在第一个事件到来前由调用方注入。
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

// StageEvent 实现 modeling.EventEmitter。
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

// 注意：parseArgsToMap 的实现见 args.go（直接使用 encoding/json）。
// 本文件不再保留 jsonUnmarshal/jsonUnmarshalImpl 旧 helper，避免与 args.go 重复定义。

// suppress unused import warning when context is only used in signatures.
var _ = context.Background
