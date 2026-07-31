package runstream

import (
	"context"
	"time"
)

type EventType string

const (
	EventRunStarted    EventType = "run_started"
	EventTurnStarted   EventType = "turn_started"
	EventTextDelta     EventType = "text_delta"
	EventToolStarted   EventType = "tool_started"
	EventToolCompleted EventType = "tool_completed"
	EventRunCompleted  EventType = "run_completed"
	EventRunFailed     EventType = "run_failed"
)

type Event struct {
	Type       EventType
	Sequence   uint64
	At         time.Time
	TraceID    string
	SessionID  string
	SessionKey string
	Channel    string
	Turn       int
	Text       string
	ToolCallID string
	ToolName   string
	ToolOK     *bool
	ErrorKind  string
	Summary    string
}

type EventSink interface {
	Emit(context.Context, Event) error
}
