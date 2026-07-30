package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// overlayAssembler records what the loop asked for and injects a marker the
// tests can look for in the provider request and, crucially, fail on if it ever
// shows up in the committed session.
type overlayAssembler struct {
	queries  []prompt.ContextQuery
	builds   int
	failNext bool
}

const overlayMarker = "<memory_context>injected</memory_context>"

func (a *overlayAssembler) Prepare(_ context.Context, query prompt.ContextQuery) (prompt.Snapshot, error) {
	a.queries = append(a.queries, query)
	return prompt.Snapshot{
		SkillIndex: "demo | 1 | does things",
		Memories:   []memory.Match{{Memory: memory.Memory{ID: "mem-1", Kind: memory.KindFact, Content: "reset is 0x10"}, Score: 0.9}},
	}, nil
}

func (a *overlayAssembler) Build(_ context.Context, input prompt.Input) (prompt.Plan, error) {
	a.builds++
	if a.failNext {
		return prompt.Plan{}, prompt.ErrPromptBudget
	}
	messages := append([]llm.Message(nil), input.Persistent...)
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: overlayMarker})
	return prompt.Plan{Messages: messages, MemoryIDs: []string{"mem-1"}, Bytes: len(overlayMarker)}, nil
}

// budgetRecorder captures the budget the loop hands to the context manager.
type budgetRecorder struct{ budgets []contextmgr.ModelBudget }

func (r *budgetRecorder) EnforceBudget(_ context.Context, budget contextmgr.ModelBudget, messages []llm.Message) ([]llm.Message, int, error) {
	r.budgets = append(r.budgets, budget)
	return append([]llm.Message(nil), messages...), 0, nil
}

func newKnowledgeAgent(t *testing.T, provider llm.Provider, store session.Store, deps func(*Dependencies), cfg func(*Config)) *Agent {
	t.Helper()
	ref := llm.ModelRef{Provider: provider.Name(), Model: "model"}
	dependencies := Dependencies{
		Models:  agentTestModels{resolved: llm.ResolvedModel{Definition: llm.ModelDefinition{Ref: ref, MaxContext: 4096, MaxOutput: 128, Tools: true}, Provider: provider}},
		Catalog: agentTestCatalog{schemas: []llm.ToolSchema{{Name: "read"}}},
		Executor: &agentTestExecutor{
			results: []security.Result{{Output: "<loaded_skill>full body</loaded_skill>", PersistentOutput: "receipt"}},
			errs:    []error{nil},
		},
		Store: store, Context: agentTestContext{}, Prompts: prompt.NopAssembler{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewID:  func() string { return "invocation" }, Now: func() time.Time { return time.Unix(1, 0) },
	}
	config := Config{MaxTurns: 4, MemoryTopK: 3, PromptReservedTokens: 256}
	if deps != nil {
		deps(&dependencies)
	}
	if cfg != nil {
		cfg(&config)
	}
	value, err := New(dependencies, config)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestNewRequiresAPromptAssembler(t *testing.T) {
	provider := &agentTestProvider{name: "test"}
	ref := llm.ModelRef{Provider: "test", Model: "model"}
	base := Dependencies{
		Models:  agentTestModels{resolved: llm.ResolvedModel{Definition: llm.ModelDefinition{Ref: ref, MaxContext: 4096, MaxOutput: 128, Tools: true}, Provider: provider}},
		Catalog: agentTestCatalog{}, Executor: &agentTestExecutor{}, Store: &agentTestStore{},
		Context: agentTestContext{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewID: func() string { return "invocation" },
	}
	if _, err := New(base, Config{MaxTurns: 1}); err == nil {
		t.Fatal("New() error = nil without a prompt assembler")
	}
	base.Prompts = prompt.NopAssembler{}
	if _, err := New(base, Config{MaxTurns: 1, PromptReservedTokens: -1}); err == nil {
		t.Fatal("New() error = nil for a negative reservation")
	}
	if _, err := New(base, Config{MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRetrievesOncePerRequestAndRendersPerTurn(t *testing.T) {
	provider := &agentTestProvider{name: "test", responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "use_skill", Args: `{"name":"demo"}`}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	recorder := &requestRecorder{agentTestProvider: provider}
	assembler := &overlayAssembler{}
	budgets := &budgetRecorder{}
	store := &agentTestStore{}
	agent := newKnowledgeAgent(t, recorder, store, func(d *Dependencies) {
		d.Prompts = assembler
		d.Context = budgets
	}, nil)
	live := session.NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})

	answer, err := agent.Run(context.Background(), live, RunInput{
		Text: "how do I reset it", SessionKey: "cli:default", Channel: "cli",
		UserID: "alice", WorkspaceID: "ws-abc123", Events: &memoryEventSink{},
	})
	if err != nil || answer != "done" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if len(assembler.queries) != 1 {
		t.Fatalf("Prepare ran %d times; retrieval must not change between turns of one run", len(assembler.queries))
	}
	query := assembler.queries[0]
	if query.UserID != "alice" || query.WorkspaceID != "ws-abc123" || query.TopK != 3 || query.Text != "how do I reset it" {
		t.Fatalf("query = %#v", query)
	}
	if assembler.builds != 2 {
		t.Fatalf("Build ran %d times; want one render per turn", assembler.builds)
	}
	// 4096 context - 128 output - 256 reserved.
	for _, budget := range budgets.budgets {
		if budget.MaxContext != 3712 {
			t.Fatalf("history budget = %d; the overlay and the answer were not reserved", budget.MaxContext)
		}
	}
	if len(recorder.requests) != 2 {
		t.Fatalf("provider requests = %d", len(recorder.requests))
	}
	for index, request := range recorder.requests {
		if request.Messages[len(request.Messages)-1].Content != overlayMarker {
			t.Fatalf("request %d had no overlay: %#v", index, request.Messages)
		}
	}
	// The whole point of the split: the model saw the overlay, the transcript did
	// not. A persisted overlay would replay this request's recall forever.
	for _, source := range []struct {
		name     string
		messages []llm.Message
	}{{"live", live.Messages}, {"saved", store.saved.Messages}} {
		for _, message := range source.messages {
			if strings.Contains(message.Content, overlayMarker) || strings.Contains(message.Content, "mem-1") {
				t.Fatalf("%s session kept the overlay: %#v", source.name, message)
			}
			if strings.Contains(message.Content, "full body") {
				t.Fatalf("%s session kept the loaded skill body: %#v", source.name, message)
			}
		}
	}
}

func TestRunFailsWhenTheReservationLeavesNoHistoryBudget(t *testing.T) {
	provider := &agentTestProvider{name: "test", responses: []*llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Content: "unused"}}}}
	store := &agentTestStore{}
	agent := newKnowledgeAgent(t, provider, store, nil, func(c *Config) { c.PromptReservedTokens = 4096 })
	live := session.NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})
	before := live.Clone()

	_, err := agent.Run(context.Background(), live, RunInput{Text: "hi", SessionKey: "cli:default", Channel: "cli", Events: &memoryEventSink{}})
	if err == nil || !strings.Contains(err.Error(), "history budget") {
		t.Fatalf("err = %v; a reservation larger than the context must fail before the provider call", err)
	}
	if len(live.Messages) != len(before.Messages) || store.saved != nil {
		t.Fatal("a rejected budget still touched the session")
	}
}

func TestRunFailsAndCommitsNothingWhenTheOverlayDoesNotFit(t *testing.T) {
	provider := &agentTestProvider{name: "test", responses: []*llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Content: "unused"}}}}
	store := &agentTestStore{}
	agent := newKnowledgeAgent(t, provider, store, func(d *Dependencies) {
		d.Prompts = &overlayAssembler{failNext: true}
	}, nil)
	live := session.NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})
	before := live.Clone()

	_, err := agent.Run(context.Background(), live, RunInput{Text: "hi", SessionKey: "cli:default", Channel: "cli", Events: &memoryEventSink{}})
	if !errors.Is(err, prompt.ErrPromptBudget) {
		t.Fatalf("err = %v", err)
	}
	if len(live.Messages) != len(before.Messages) || store.saved != nil {
		t.Fatal("a failed prompt build still committed the run")
	}
}
