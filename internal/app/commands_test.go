package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

func newTestRouter(t *testing.T, contextManager ContextCommands) (*CommandRouter, *session.Registry) {
	t.Helper()
	store := &memoryStore{sessions: make(map[string]*session.Session)}
	factory, err := session.NewDefaultFactory(session.Defaults{ModelRef: llm.ModelRef{Provider: "ollama", Model: "test-model"}, SystemPrompt: "system"}, func() string { return "trace" })
	if err != nil {
		t.Fatal(err)
	}
	models := newTestModels(t)
	registry, err := session.NewRegistry(store, factory, models, "ollama")
	if err != nil {
		t.Fatal(err)
	}
	if contextManager == nil {
		contextManager = testContextManager{}
	}
	router, err := NewCommandRouter(CommandDependencies{Sessions: registry, Updater: registry, Context: contextManager, Models: models})
	if err != nil {
		t.Fatal(err)
	}
	return router, registry
}

func parseForTest(t *testing.T, text string) Command {
	t.Helper()
	command, ok, err := ParseCommand(text)
	if err != nil || !ok {
		t.Fatalf("ParseCommand(%q) = %#v, %v, %v", text, command, ok, err)
	}
	return command
}

func TestParseCommand(t *testing.T) {
	if _, ok, err := ParseCommand("hello"); err != nil || ok {
		t.Fatalf("ordinary text = %v, %v", ok, err)
	}
	command := parseForTest(t, " /QUIT ")
	if command.Name != "exit" || command.Raw != "/QUIT" {
		t.Fatalf("command = %#v", command)
	}
	if _, _, err := ParseCommand("/"); err == nil || !channel.IsRecoverable(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewCommandRouterRejectsNilDependencies(t *testing.T) {
	router, registry := newTestRouter(t, nil)
	_ = router
	ctx := testContextManager{}
	tests := []CommandDependencies{
		{Updater: registry, Context: ctx, Models: newTestModels(t)},
		{Sessions: registry, Context: ctx, Models: newTestModels(t)},
		{Sessions: registry, Updater: registry, Models: newTestModels(t)},
		{Sessions: registry, Updater: registry, Context: ctx},
	}
	for _, deps := range tests {
		if _, err := NewCommandRouter(deps); err == nil {
			t.Fatal("error = nil")
		}
	}
}

func TestCommandRouterUsageAndUnknownErrorsAreRecoverable(t *testing.T) {
	router, _ := newTestRouter(t, nil)
	for _, input := range []string{"/new extra", "/resume", "/compact extra", "/unknown"} {
		_, err := router.Execute(context.Background(), "cli:default", parseForTest(t, input))
		if err == nil || !channel.IsRecoverable(err) {
			t.Fatalf("Execute(%q) error = %v", input, err)
		}
	}
}

func TestCommandRouterSessionLifecycle(t *testing.T) {
	router, registry := newTestRouter(t, nil)
	ctx := context.Background()
	key := "cli:default"

	created, err := router.Execute(ctx, key, parseForTest(t, "/new"))
	if err != nil || !strings.Contains(created.Text, "new session:") {
		t.Fatalf("new = %#v, %v", created, err)
	}
	first, err := registry.Current(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.WithSession(ctx, key, func(sess *session.Session) error {
		sess.AddUser("hello")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	history, err := router.Execute(ctx, key, parseForTest(t, "/history"))
	if err != nil || !strings.Contains(history.Text, "user: hello") {
		t.Fatalf("history = %#v, %v", history, err)
	}

	reset, err := router.Execute(ctx, key, parseForTest(t, "/reset"))
	if err != nil || !strings.Contains(reset.Text, first.ID) {
		t.Fatalf("reset = %#v, %v", reset, err)
	}
	current, _ := registry.Current(ctx, key)
	if current.ID != first.ID || len(current.Messages) != 1 || current.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("current = %#v", current)
	}

	_, err = router.Execute(ctx, key, parseForTest(t, "/new"))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := registry.Current(ctx, key)
	if second.ID == first.ID {
		t.Fatal("new session reused ID")
	}

	resumed, err := router.Execute(ctx, key, parseForTest(t, "/resume "+first.ID))
	if err != nil || !strings.Contains(resumed.Text, first.ID) {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	current, _ = registry.Current(ctx, key)
	if current.ID != first.ID {
		t.Fatalf("current ID = %q", current.ID)
	}

	listed, err := router.Execute(ctx, key, parseForTest(t, "/sessions"))
	if err != nil || !strings.Contains(listed.Text, first.ID) || !strings.Contains(listed.Text, second.ID) {
		t.Fatalf("sessions = %#v, %v", listed, err)
	}
}

func TestCommandRouterCompact(t *testing.T) {
	compacted := []llm.Message{{Role: llm.RoleSystem, Content: "system"}, {Role: llm.RoleAssistant, Content: "summary"}}
	router, registry := newTestRouter(t, testContextManager{messages: compacted, usage: 12})
	ctx := context.Background()
	key := "cli:default"
	if _, err := registry.New(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithSession(ctx, key, func(sess *session.Session) error {
		sess.AddUser("one")
		sess.AddAssistant(llm.Message{Content: "two"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := router.Execute(ctx, key, parseForTest(t, "/compact"))
	if err != nil || result.Action != channel.ActionReply {
		t.Fatalf("compact = %#v, %v", result, err)
	}
	current, _ := registry.Current(ctx, key)
	if len(current.Messages) != len(compacted) || current.TokenUsage != 12 {
		t.Fatalf("current = %#v", current)
	}
}

func TestCommandRouterCompactFailurePreservesSession(t *testing.T) {
	want := errors.New("summary failed")
	router, registry := newTestRouter(t, testContextManager{err: want})
	ctx := context.Background()
	key := "cli:default"
	if _, err := registry.New(ctx, key); err != nil {
		t.Fatal(err)
	}
	before, _ := registry.Current(ctx, key)
	_, err := router.Execute(ctx, key, parseForTest(t, "/compact"))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	after, _ := registry.Current(ctx, key)
	if after.ID != before.ID || len(after.Messages) != len(before.Messages) {
		t.Fatalf("before = %#v, after = %#v", before, after)
	}
}

func TestCommandRouterModelCurrentListAndSelect(t *testing.T) {
	router, registry := newTestRouter(t, nil)
	ctx := context.Background()
	if _, err := registry.New(ctx, "cli:default"); err != nil {
		t.Fatal(err)
	}
	current, err := router.Execute(ctx, "cli:default", parseForTest(t, "/model"))
	if err != nil || !strings.Contains(current.Text, "ollama:test-model") {
		t.Fatalf("current = %#v, %v", current, err)
	}
	list, err := router.Execute(ctx, "cli:default", parseForTest(t, "/model list"))
	if err != nil || !strings.Contains(list.Text, "test-model") {
		t.Fatalf("list = %#v, %v", list, err)
	}
	selected, err := router.Execute(ctx, "cli:default", parseForTest(t, "/model model"))
	if err != nil || !strings.Contains(selected.Text, "ollama:model") {
		t.Fatalf("selected = %#v, %v", selected, err)
	}
	after, _ := registry.Current(ctx, "cli:default")
	if after.ModelRef.String() != "ollama:model" {
		t.Fatalf("ModelRef = %s", after.ModelRef.String())
	}
}

func TestCommandRouterUnknownModelIsRecoverable(t *testing.T) {
	router, registry := newTestRouter(t, nil)
	if _, err := registry.New(context.Background(), "cli:default"); err != nil {
		t.Fatal(err)
	}
	_, err := router.Execute(context.Background(), "cli:default", parseForTest(t, "/model missing"))
	if err == nil || !channel.IsRecoverable(err) {
		t.Fatalf("error = %v", err)
	}
}
