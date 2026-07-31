package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

type agentTestModels struct{ resolved llm.ResolvedModel }

func (m agentTestModels) Resolve(llm.ModelRef) (llm.ResolvedModel, error) { return m.resolved, nil }

type agentTestCatalog struct{ schemas []llm.ToolSchema }

func (c agentTestCatalog) Schemas() []llm.ToolSchema { return c.schemas }

type agentTestContext struct{}

func (agentTestContext) EnforceBudget(_ context.Context, _ contextmgr.ModelBudget, messages []llm.Message) ([]llm.Message, int, error) {
	return append([]llm.Message(nil), messages...), 0, nil
}

type agentTestStore struct {
	saved *session.Session
	err   error
}

func (s *agentTestStore) Save(_ context.Context, value *session.Session) error {
	if s.err != nil {
		return s.err
	}
	s.saved = value.Clone()
	return nil
}
func (*agentTestStore) Load(context.Context, string) (*session.Session, error) {
	return nil, errors.New("unused")
}
func (*agentTestStore) Delete(context.Context, string) error { return errors.New("unused") }
func (*agentTestStore) List(context.Context) ([]session.Meta, error) {
	return nil, errors.New("unused")
}

type agentTestExecutor struct {
	results []security.Result
	errs    []error
	calls   int
}

func (e *agentTestExecutor) Execute(context.Context, security.Invocation) (security.Result, error) {
	index := e.calls
	e.calls++
	return e.results[index], e.errs[index]
}

type agentTestProvider struct {
	name      string
	responses []*llm.Response
	complete  int
}

func (p *agentTestProvider) Name() string { return p.name }
func (*agentTestProvider) Capability() llm.Capabilities {
	return llm.Capabilities{Tools: true, Streaming: false}
}
func (p *agentTestProvider) Complete(context.Context, llm.Request) (*llm.Response, error) {
	response := p.responses[p.complete]
	p.complete++
	return response, nil
}
func (*agentTestProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unexpected stream")
}

type memoryEventSink struct{ events []runstream.Event }

func (s *memoryEventSink) Emit(_ context.Context, event runstream.Event) error {
	s.events = append(s.events, event)
	return nil
}

func newAgentForTest(t *testing.T, provider llm.Provider, executor SecureToolExecutor, store session.Store) *Agent {
	t.Helper()
	ref := llm.ModelRef{Provider: provider.Name(), Model: "model"}
	value, err := New(Dependencies{
		Models:  agentTestModels{resolved: llm.ResolvedModel{Definition: llm.ModelDefinition{Ref: ref, MaxContext: 4096, MaxOutput: 128, Tools: true}, Provider: provider}},
		Catalog: agentTestCatalog{schemas: []llm.ToolSchema{{Name: "read"}}}, Executor: executor,
		Store: store, Context: agentTestContext{}, Prompts: prompt.NopAssembler{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewID:  func() string { return "invocation" }, Now: func() time.Time { return time.Unix(1, 0) },
	}, Config{MaxTurns: 4, MemoryTopK: 4, PromptReservedTokens: 256})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRunCommitsOnceAndEmitsLifecycleAndToolEvents(t *testing.T) {
	provider := &agentTestProvider{name: "test", responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read", Args: `{}`}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	store := &agentTestStore{}
	executor := &agentTestExecutor{results: []security.Result{{Output: "tool output"}}, errs: []error{nil}}
	agent := newAgentForTest(t, provider, executor, store)
	live := session.NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})
	before := live.Clone()
	sink := &memoryEventSink{}

	answer, err := agent.Run(context.Background(), live, RunInput{Text: "hello", SessionKey: "cli:default", Channel: "cli", Events: sink})
	if err != nil || answer != "done" {
		t.Fatalf("Run() answer=%q err=%v", answer, err)
	}
	if store.saved == nil || !reflect.DeepEqual(store.saved.Messages, live.Messages) {
		t.Fatalf("saved=%#v live=%#v", store.saved, live)
	}
	if reflect.DeepEqual(before.Messages, live.Messages) {
		t.Fatal("live session was not committed")
	}
	types := make([]runstream.EventType, len(sink.events))
	for index, event := range sink.events {
		types[index] = event.Type
		if event.Sequence != uint64(index+1) || event.SessionID != live.ID || event.TraceID != live.TraceID {
			t.Fatalf("event %d = %#v", index, event)
		}
	}
	want := []runstream.EventType{runstream.EventRunStarted, runstream.EventTurnStarted, runstream.EventToolStarted, runstream.EventToolCompleted, runstream.EventTurnStarted, runstream.EventRunCompleted}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("event types=%v want=%v", types, want)
	}
}

func TestRunSaveFailureLeavesLiveSessionUnchangedAndEmitsFailed(t *testing.T) {
	provider := &agentTestProvider{name: "test", responses: []*llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Content: "answer"}}}}
	store := &agentTestStore{err: errors.New("disk full")}
	agent := newAgentForTest(t, provider, &agentTestExecutor{}, store)
	live := session.NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})
	before := live.Clone()
	sink := &memoryEventSink{}

	_, err := agent.Run(context.Background(), live, RunInput{Text: "hello", SessionKey: "cli:default", Channel: "cli", Events: sink})
	if err == nil {
		t.Fatal("Run() error=nil")
	}
	if !reflect.DeepEqual(live, before) {
		t.Fatalf("live changed: before=%#v after=%#v", before, live)
	}
	if got := sink.events[len(sink.events)-1].Type; got != runstream.EventRunFailed {
		t.Fatalf("last event=%s", got)
	}
}
