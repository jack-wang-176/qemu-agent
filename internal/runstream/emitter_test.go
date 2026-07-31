package runstream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingSink keeps what a producer emitted so a test can assert on the wire
// form rather than on a renderer's rendering of it.
type recordingSink struct {
	events []Event
	err    error
}

func (r *recordingSink) Emit(_ context.Context, event Event) error {
	r.events = append(r.events, event)
	return r.err
}

func at(seconds int64) time.Time { return time.Unix(seconds, 0).UTC() }

func newTestEmitter(t *testing.T, sink EventSink) *SequenceEmitter {
	t.Helper()
	emitter, err := NewEmitter(EmitterOptions{
		Sink: sink,
		Identity: Event{
			TraceID: "trace-1", SessionID: "sess-1", SessionKey: "cli:local", Channel: "cli",
		},
		Now: func() time.Time { return at(100) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return emitter
}

func TestEmitterStampsSequenceAndIdentity(t *testing.T) {
	sink := &recordingSink{}
	emitter := newTestEmitter(t, sink)
	ctx := context.Background()

	// A producer may not set the bookkeeping fields: whatever it puts there is
	// overwritten, so a forged sequence or session id cannot reach a consumer.
	events := []Event{
		{Type: EventRunStarted, Sequence: 99, SessionID: "somebody-else"},
		{Type: EventStageStarted, Stage: "plan"},
		{Type: EventStageCompleted, Stage: "plan", Text: "wrote plan.md"},
		{Type: EventRunCompleted},
	}
	for _, event := range events {
		if err := emitter.Emit(ctx, event); err != nil {
			t.Fatalf("emit %s: %v", event.Type, err)
		}
	}
	if len(sink.events) != len(events) {
		t.Fatalf("sink saw %d events", len(sink.events))
	}
	for index, event := range sink.events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d has sequence %d", index, event.Sequence)
		}
		if event.SessionID != "sess-1" || event.TraceID != "trace-1" || event.SessionKey != "cli:local" || event.Channel != "cli" {
			t.Fatalf("event %d identity = %#v", index, event)
		}
		if !event.At.Equal(at(100)) {
			t.Fatalf("event %d has no timestamp", index)
		}
	}
	// A stage run is still one run: it opens with run_started and closes with a
	// single terminal event.
	if sink.events[0].Type != EventRunStarted || sink.events[len(sink.events)-1].Type != EventRunCompleted {
		t.Fatalf("run is not wrapped: %#v", sink.events)
	}
}

func TestEmitterRejectsMalformedEventsBeforeTheSink(t *testing.T) {
	sink := &recordingSink{}
	emitter := newTestEmitter(t, sink)
	ctx := context.Background()

	tests := map[string]Event{
		"unknown type":            {Type: EventType("stage_polished"), Stage: "plan"},
		"stage without a name":    {Type: EventStageStarted},
		"stage start with text":   {Type: EventStageStarted, Stage: "plan", Text: "already done"},
		"empty progress":          {Type: EventStageProgress, Stage: "plan"},
		"stage with a tool":       {Type: EventStageProgress, Stage: "plan", Text: "x", ToolName: "bash"},
		"stage with a turn":       {Type: EventStageProgress, Stage: "plan", Text: "x", Turn: 1},
		"failure without reason":  {Type: EventStageCompleted, Stage: "plan", ErrorKind: "build_failed"},
		"oversize progress":       {Type: EventStageProgress, Stage: "plan", Text: strings.Repeat("a", MaxEventText+1)},
		"run event with a stage":  {Type: EventRunStarted, Stage: "plan"},
		"text delta with a stage": {Type: EventTextDelta, Turn: 1, Text: "hi", Stage: "plan"},
	}
	for name, event := range tests {
		t.Run(name, func(t *testing.T) {
			if err := emitter.Emit(ctx, event); err == nil {
				t.Fatal("Emit() error = nil")
			}
		})
	}
	// Nothing invalid reached the sink: validation happens on the producing side,
	// so a renderer never has to defend against a malformed event.
	if len(sink.events) != 0 {
		t.Fatalf("sink saw %d invalid events", len(sink.events))
	}
}

func TestEmitterReportsSinkFailureAndKeepsCounting(t *testing.T) {
	sink := &recordingSink{err: errors.New("sink is gone")}
	emitter := newTestEmitter(t, sink)
	if err := emitter.Emit(context.Background(), Event{Type: EventRunStarted}); err == nil {
		t.Fatal("a broken sink reported success")
	}
	// The sequence still advanced: it numbers what the producer emitted, not what
	// the transport managed to deliver, so a consumer's "must increase" check does
	// not break after one dropped event.
	sink.err = nil
	if err := emitter.Emit(context.Background(), Event{Type: EventRunCompleted}); err != nil {
		t.Fatal(err)
	}
	if sink.events[len(sink.events)-1].Sequence != 2 {
		t.Fatalf("sequence = %d", sink.events[len(sink.events)-1].Sequence)
	}
}

func TestNopEmitterAndSinkAreUsedInsteadOfNil(t *testing.T) {
	// A producer with no listener still gets a usable emitter, which is why no
	// call site contains `if events != nil`.
	if err := NormalizeEmitter(nil).Emit(context.Background(), Event{Type: EventType("anything")}); err != nil {
		t.Fatalf("NopEmitter.Emit = %v", err)
	}
	// ...but a canceled request is still reported, so cancellation cannot become
	// invisible just because nobody is watching.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NormalizeEmitter(nil).Emit(ctx, Event{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled NopEmitter.Emit = %v", err)
	}
	emitter := newTestEmitter(t, nil)
	if err := emitter.Emit(context.Background(), Event{Type: EventRunStarted}); err != nil {
		t.Fatalf("emitter without a sink = %v", err)
	}
}

func TestNewEmitterRequiresIdentity(t *testing.T) {
	for name, identity := range map[string]Event{
		"no session key": {Channel: "cli"},
		"no channel":     {SessionKey: "cli:local"},
		"blank":          {SessionKey: " ", Channel: " "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewEmitter(EmitterOptions{Identity: identity}); err == nil {
				t.Fatal("NewEmitter() error = nil")
			}
		})
	}
}
