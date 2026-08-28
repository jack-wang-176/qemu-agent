package modelingagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/modelingworkflow"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
)

type dialogueResolver struct {
	resolved llm.ResolvedModel
}

func (r dialogueResolver) Resolve(ref llm.ModelRef) (llm.ResolvedModel, error) {
	if ref != r.resolved.Definition.Ref {
		return llm.ResolvedModel{}, errors.New("unexpected model reference")
	}
	return r.resolved, nil
}

type dialogueProvider struct {
	request  llm.Request
	response *llm.Response
}

func (*dialogueProvider) Name() string { return "test" }

func (*dialogueProvider) Capability() llm.Capabilities {
	return llm.Capabilities{MaxContext: 4096}
}

func (p *dialogueProvider) Complete(_ context.Context, request llm.Request) (*llm.Response, error) {
	p.request = request
	return p.response, nil
}

func (*dialogueProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unexpected stream call")
}

type dialogueContext struct {
	budget contextmgr.ModelBudget
}

func (c *dialogueContext) EnforceBudget(
	_ context.Context,
	budget contextmgr.ModelBudget,
	messages []llm.Message,
) ([]llm.Message, int, error) {
	c.budget = budget
	return append([]llm.Message(nil), messages...), 1, nil
}

func TestDialogueInterpretBuildsOneBoundedToolFreeRequest(t *testing.T) {
	ref := llm.ModelRef{Provider: "test", Model: "intent-model"}
	provider := &dialogueProvider{response: &llm.Response{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: `{"kind":"start","title":"UART model","instruction":"Model a UART device","sources":[],"artifact_id":""}`,
	}}}
	contextManager := &dialogueContext{}
	dialogue := NewDialogue(DialogueConfig{
		Resolver: dialogueResolver{resolved: llm.ResolvedModel{
			Definition: llm.ModelDefinition{Ref: ref, MaxContext: 4096, MaxOutput: 256},
			Provider:   provider,
		}},
		Context:  contextManager,
		Prompts:  prompt.NopAssembler{},
		Model:    ref,
		MaxBytes: 4096,
		Now:      func() time.Time { return time.Unix(10, 0) },
	})

	intent, err := dialogue.Interpret(context.Background(), modelingworkflow.InterpretInput{
		Text:        "Start a UART model",
		History:     []modelingworkflow.ConversationMsg{{Role: "user", Text: "I need a device"}, {Role: "assistant", Text: "Which device?"}},
		HasHistory:  true,
		Awaiting:    modelingworkflow.AwaitingNone,
		WorkspaceID: "workspace-1",
		UserID:      "user-1",
	})
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if intent.Kind != modelingworkflow.IntentStart || intent.Title != "UART model" {
		t.Fatalf("intent = %#v", intent)
	}
	if provider.request.Model != ref.Model || provider.request.MaxTokens != 256 {
		t.Fatalf("provider request model/output = %q/%d", provider.request.Model, provider.request.MaxTokens)
	}
	if len(provider.request.Tools) != 0 {
		t.Fatalf("provider tools = %#v, want none", provider.request.Tools)
	}
	if len(provider.request.Messages) != 5 {
		t.Fatalf("provider message count = %d, want 5", len(provider.request.Messages))
	}
	if provider.request.Messages[0].Role != llm.RoleSystem || provider.request.Messages[0].Content != interpreterSystemPrompt {
		t.Fatal("first message is not the fixed interpreter system prompt")
	}
	if provider.request.Messages[4].Role != llm.RoleUser || provider.request.Messages[4].Content != "Start a UART model" {
		t.Fatalf("last message = %#v", provider.request.Messages[4])
	}
	if contextManager.budget.MaxContext != 4096-256 {
		t.Fatalf("context budget = %d", contextManager.budget.MaxContext)
	}
}

func TestDecodeIntentRejectsMalformedContracts(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		awaiting modelingworkflow.AwaitingKind
	}{
		{
			name:     "unknown field",
			content:  `{"kind":"inspect","title":"","instruction":"","sources":[],"artifact_id":"","project_id":"mp-0123456789abcdef"}`,
			awaiting: modelingworkflow.AwaitingNone,
		},
		{
			name:     "trailing object",
			content:  `{"kind":"inspect","title":"","instruction":"","sources":[],"artifact_id":""} {}`,
			awaiting: modelingworkflow.AwaitingNone,
		},
		{
			name:     "input without awaiting state",
			content:  `{"kind":"provide_input","title":"","instruction":"more detail","sources":[],"artifact_id":""}`,
			awaiting: modelingworkflow.AwaitingNone,
		},
		{
			name:     "invalid artifact ID",
			content:  `{"kind":"read_artifact","title":"","instruction":"","sources":[],"artifact_id":"not-an-id"}`,
			awaiting: modelingworkflow.AwaitingNone,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeIntent(test.content, test.awaiting); err == nil {
				t.Fatal("decodeIntent() error = nil")
			}
		})
	}
}

func TestDecodeIntentAcceptsAwaitedSource(t *testing.T) {
	intent, err := decodeIntent(
		`{"kind":"provide_input","title":"","instruction":"","sources":[{"kind":"workspace_path","value":"docs/uart.md","digest":""}],"artifact_id":""}`,
		modelingworkflow.AwaitingSource,
	)
	if err != nil {
		t.Fatalf("decodeIntent() error = %v", err)
	}
	if intent.Kind != modelingworkflow.IntentProvideInput || len(intent.Sources) != 1 {
		t.Fatalf("intent = %#v", intent)
	}
}
