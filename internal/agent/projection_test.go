package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// TestRunPersistsProjectionButKeepsModelViewForLaterTurns is the core I7-D
// contract: the second provider request must still contain the full tool output,
// while the committed session only keeps the receipt.
func TestRunPersistsProjectionButKeepsModelViewForLaterTurns(t *testing.T) {
	provider := &agentTestProvider{name: "test", responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "use_skill", Args: `{"name":"demo"}`}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	recorder := &requestRecorder{agentTestProvider: provider}
	store := &agentTestStore{}
	executor := &agentTestExecutor{
		results: []security.Result{{Output: "<loaded_skill>full body</loaded_skill>", PersistentOutput: "receipt"}},
		errs:    []error{nil},
	}
	agent := newAgentForTest(t, recorder, executor, store)
	live := session.NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})

	if _, err := agent.Run(context.Background(), live, RunInput{Text: "hi", SessionKey: "cli:default", Channel: "cli", Events: &memoryEventSink{}}); err != nil {
		t.Fatal(err)
	}
	transient := findToolMessage(t, recorder.requests[1].Messages, "call-1")
	if !strings.Contains(transient, "full body") {
		t.Fatalf("model lost the skill body inside the run: %q", transient)
	}
	if got := findToolMessage(t, live.Messages, "call-1"); got != "receipt" {
		t.Fatalf("committed tool message = %q, want receipt", got)
	}
	if got := findToolMessage(t, store.saved.Messages, "call-1"); got != "receipt" {
		t.Fatalf("saved tool message = %q, want receipt", got)
	}
}

// TestRunKeepsRunWhenProjectionTargetIsGone documents the deliberate deviation
// from the plan: compaction may delete the tool message inside the same run, and
// losing a receipt must not discard a completed run.
func TestRunKeepsRunWhenProjectionTargetIsGone(t *testing.T) {
	provider := &agentTestProvider{name: "test", responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "use_skill", Args: `{"name":"demo"}`}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	store := &agentTestStore{}
	executor := &agentTestExecutor{
		results: []security.Result{{Output: "model view", PersistentOutput: "receipt"}},
		errs:    []error{nil},
	}
	agent := newAgentForTest(t, provider, executor, store)
	agent.ctxmgr = droppingContext{}
	live := session.NewSession("trace", "system", llm.ModelRef{Provider: "test", Model: "model"})

	answer, err := agent.Run(context.Background(), live, RunInput{Text: "hi", SessionKey: "cli:default", Channel: "cli", Events: &memoryEventSink{}})
	if err != nil || answer != "done" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	for _, message := range live.Messages {
		if message.Role == llm.RoleTool {
			t.Fatalf("tool message survived compaction: %#v", message)
		}
	}
}

// droppingContext emulates a compactor that removes tool messages.
type droppingContext struct{}

func (droppingContext) EnforceBudget(_ context.Context, _ contextmgr.ModelBudget, messages []llm.Message) ([]llm.Message, int, error) {
	kept := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == llm.RoleTool {
			continue
		}
		kept = append(kept, message)
	}
	return kept, 0, nil
}

type requestRecorder struct {
	*agentTestProvider
	requests []llm.Request
}

func (p *requestRecorder) Complete(ctx context.Context, request llm.Request) (*llm.Response, error) {
	p.requests = append(p.requests, request)
	return p.agentTestProvider.Complete(ctx, request)
}

func findToolMessage(t *testing.T, messages []llm.Message, callID string) string {
	t.Helper()
	for _, message := range messages {
		if message.Role == llm.RoleTool && message.ToolCallID == callID {
			return message.Content
		}
	}
	t.Fatalf("tool message %q not found in %#v", callID, messages)
	return ""
}
