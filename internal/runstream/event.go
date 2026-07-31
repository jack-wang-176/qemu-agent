package runstream

// event.go is the wire format of one request. It is deliberately a single flat
// struct with a closed set of types: every consumer (CLI renderer, Telegram
// sink) switches on Type and hard-fails on an unknown one, so a new event kind
// cannot be added without teaching every renderer to display it.
//
// The protocol has one shape rule: a request opens with run_started and closes
// with exactly one of run_completed / run_failed. Everything else — turns, text,
// tool calls, modeling stages — happens between those two.

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

	// The three stage events belong to a long-running pipeline run (I8's
	// /modeling advance). They are their own types rather than free-text notices
	// because a stage is a first-class concept: a client has to be able to tell
	// which stage moved without parsing a sentence.
	EventStageStarted   EventType = "stage_started"
	EventStageProgress  EventType = "stage_progress"
	EventStageCompleted EventType = "stage_completed"
)

// Event carries one notification. It is a notification and not a data channel:
// a client that saw stage_completed still has to ask for the result, which is
// what makes a lossy stream (Telegram edit throttling merges messages) harmless.
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
	// Stage names the pipeline step a stage_* event is about. It is a separate
	// field rather than a reuse of ToolName because a stage is not a tool call:
	// borrowing the slot would make every renderer lie about what it is showing.
	Stage string
}

type EventSink interface {
	Emit(context.Context, Event) error
}
