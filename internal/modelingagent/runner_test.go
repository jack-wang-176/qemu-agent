package modelingagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/modelingworkflow"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

type runnerWorkflow struct {
	result  modelingworkflow.Result
	err     error
	calls   int
	call    modelingworkflow.CallContext
	request modelingworkflow.Request
	caller  security.Caller
	secure  bool
}

func (w *runnerWorkflow) Handle(ctx context.Context, call modelingworkflow.CallContext, request modelingworkflow.Request) (modelingworkflow.Result, error) {
	w.calls++
	w.call = call
	w.request = request
	w.caller, w.secure = security.CallerFrom(ctx)
	return w.result, w.err
}

type runnerContext struct{}

func (runnerContext) EnforceBudget(_ context.Context, _ contextmgr.ModelBudget, messages []llm.Message) ([]llm.Message, int, error) {
	return append([]llm.Message(nil), messages...), len(messages), nil
}

type runnerStore struct {
	saved    *session.Session
	err      error
	live     *session.Session
	liveSize int
}

func (s *runnerStore) Save(_ context.Context, value *session.Session) error {
	if s.live != nil {
		s.liveSize = len(s.live.Messages)
	}
	if s.err != nil {
		return s.err
	}
	s.saved = value.Clone()
	return nil
}

func (*runnerStore) Load(context.Context, string) (*session.Session, error) {
	return nil, errors.New("unused")
}
func (*runnerStore) Delete(context.Context, string) error { return errors.New("unused") }
func (*runnerStore) List(context.Context) ([]session.Meta, error) {
	return nil, errors.New("unused")
}

type runnerEvents struct{ events []runstream.Event }

func (s *runnerEvents) Emit(_ context.Context, event runstream.Event) error {
	s.events = append(s.events, event)
	return nil
}

func TestRunnerCommitsBoundedWorkflowReply(t *testing.T) {
	live := runnerSession()
	live.AddUser("earlier request")
	live.AddAssistant(llm.Message{Content: "earlier reply"})
	initialSize := len(live.Messages)
	workflow := &runnerWorkflow{result: modelingworkflow.Result{Reply: "Please provide the datasheet.", State: modelingworkflow.StateNeedsInput}}
	store := &runnerStore{live: live}
	events := &runnerEvents{}
	runner := runnerForTest(workflow, store)

	answer, err := runner.Run(context.Background(), live, agent.RunInput{
		Text: "Model this UART", SessionKey: "cli:default", Channel: "cli", WorkspaceID: "ws-1", UserID: "alice", Interactive: true, Events: events,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != workflow.result.Reply || workflow.calls != 1 {
		t.Fatalf("answer/calls = %q/%d", answer, workflow.calls)
	}
	if workflow.request.Text != "Model this UART" || !workflow.request.Hashistory || len(workflow.request.History) != 2 {
		t.Fatalf("workflow request = %#v", workflow.request)
	}
	if workflow.call.TraceID != live.TraceID || workflow.call.IdempotencyKey == "" {
		t.Fatalf("workflow call = %#v", workflow.call)
	}
	if !workflow.secure {
		t.Fatal("workflow context is missing its security caller")
	}
	wantCaller := security.Caller{
		TraceID: live.TraceID, SessionID: live.ID, SessionKey: "cli:default",
		Channel: "cli", Interactive: true,
	}
	if workflow.caller != wantCaller {
		t.Fatalf("security caller = %#v, want %#v", workflow.caller, wantCaller)
	}
	if store.liveSize != initialSize {
		t.Fatalf("live session changed before Save: size = %d, want %d", store.liveSize, initialSize)
	}
	if store.saved == nil || len(store.saved.Messages) != initialSize+2 || len(live.Messages) != initialSize+2 {
		t.Fatalf("saved/live messages = %#v/%#v", store.saved, live.Messages)
	}
	if len(events.events) != 2 || events.events[0].Type != runstream.EventRunStarted || events.events[1].Type != runstream.EventRunCompleted {
		t.Fatalf("events = %#v", events.events)
	}
}

func TestRunnerWorkflowFailureDoesNotCommitSession(t *testing.T) {
	live := runnerSession()
	cause := errors.New("internal provider detail")
	workflow := &runnerWorkflow{err: &modelingworkflow.Error{
		Kind: modelingworkflow.ErrorUnavailable, Public: "Modeling is unavailable.", Retryable: true, Cause: cause,
	}}
	store := &runnerStore{live: live}
	events := &runnerEvents{}
	runner := runnerForTest(workflow, store)

	_, err := runner.Run(context.Background(), live, agent.RunInput{
		Text: "Model this UART", SessionKey: "cli:default", Channel: "cli", WorkspaceID: "ws-1", Events: events,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("Run() error = %v", err)
	}
	if store.saved != nil || len(live.Messages) != 0 {
		t.Fatalf("failed run committed session: saved=%#v live=%#v", store.saved, live.Messages)
	}
	if len(events.events) != 2 || events.events[1].Type != runstream.EventRunFailed || events.events[1].ErrorKind != "unavailable" {
		t.Fatalf("events = %#v", events.events)
	}
}

func TestRunnerSaveFailureLeavesLiveSessionUnchanged(t *testing.T) {
	live := runnerSession()
	saveErr := errors.New("disk unavailable")
	workflow := &runnerWorkflow{result: modelingworkflow.Result{Reply: "Additional input is needed.", State: modelingworkflow.StateNeedsInput}}
	store := &runnerStore{live: live, err: saveErr}
	events := &runnerEvents{}
	runner := runnerForTest(workflow, store)

	_, err := runner.Run(context.Background(), live, agent.RunInput{
		Text: "Model this UART", SessionKey: "cli:default", Channel: "cli", WorkspaceID: "ws-1", Events: events,
	})
	if !errors.Is(err, saveErr) {
		t.Fatalf("Run() error = %v", err)
	}
	if len(live.Messages) != 0 {
		t.Fatalf("live session changed after Save failure: %#v", live.Messages)
	}
	if len(events.events) != 2 || events.events[1].Type != runstream.EventRunFailed {
		t.Fatalf("events = %#v", events.events)
	}
}

func TestTurnIdempotencyKeyIsStablePerUncommittedTurn(t *testing.T) {
	first := turnIdempotencyKey("session-1", 2, "model uart")
	if second := turnIdempotencyKey("session-1", 2, "model uart"); first != second {
		t.Fatal("same uncommitted turn produced a different idempotency key")
	}
	if next := turnIdempotencyKey("session-1", 4, "model uart"); first == next {
		t.Fatal("next committed turn reused an idempotency key")
	}
}

func runnerForTest(workflow modelingworkflow.Service, store session.Store) Runner {
	return NewRunner(Dependencies{
		Workflow: workflow, Store: store, Context: runnerContext{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewID:  func() string { return "request-1" },
		Model:  llm.ModelRef{Provider: "test", Model: "model"}, MaxContext: 4096,
	})
}

func runnerSession() *session.Session {
	s := session.NewSession("trace-1", "", llm.ModelRef{Provider: "test", Model: "model"})
	s.ID = "session-1"
	return s
}
