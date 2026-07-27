package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"sync"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type recordingRunner struct {
	mu       sync.Mutex
	sessions []*session.Session
}

func (r *recordingRunner) Run(_ context.Context, sess *session.Session, _ agent.RunInput) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, sess)
	return "ok", nil
}

type memoryStore struct {
	mu       sync.Mutex
	sessions map[string]*session.Session
}

func (s *memoryStore) Save(_ context.Context, sess *session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = copySession(sess)
	return nil
}
func (s *memoryStore) Load(_ context.Context, id string) (*session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.sessions[id]
	if stored == nil {
		return nil, fs.ErrNotExist
	}
	return copySession(stored), nil
}
func (*memoryStore) Delete(context.Context, string) error { return nil }
func (s *memoryStore) List(context.Context) ([]session.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]session.Meta, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, session.Meta{ID: sess.ID, TraceID: sess.TraceID, ModelRef: sess.ModelRef, UpdatedAt: sess.UpdatedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

type testContextManager struct {
	messages []llm.Message
	usage    int
	err      error
}

func (m testContextManager) EnforceBudget(_ context.Context, _ contextmgr.ModelBudget, messages []llm.Message) ([]llm.Message, int, error) {
	if m.err != nil {
		return nil, 0, m.err
	}
	if m.messages != nil {
		return append([]llm.Message(nil), m.messages...), m.usage, nil
	}
	return append([]llm.Message(nil), messages...), m.usage, nil
}

func newTestApplication(t *testing.T) (*Application, *recordingRunner, *session.Registry) {
	t.Helper()
	runner := &recordingRunner{}
	store := &memoryStore{sessions: make(map[string]*session.Session)}
	factory, err := session.NewDefaultFactory(session.Defaults{ModelRef: llm.ModelRef{Provider: "ollama", Model: "test-model"}, SystemPrompt: "system"}, func() string { return "generated-trace" })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := session.NewRegistry(store, factory, newTestModels(t), "ollama")
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewCommandRouter(CommandDependencies{
		Sessions: registry,
		Updater:  registry,
		Context:  testContextManager{},
		Models:   newTestModels(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewApplication(Dependencies{
		Runner:   runner,
		Sessions: registry,
		Commands: commands,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return application, runner, registry
}

func copySession(sess *session.Session) *session.Session {
	if sess == nil {
		return nil
	}
	result := *sess
	result.Messages = sess.MessageCopy()
	return &result
}

func TestRunOnceUsesRegistrySessionWithConfiguredDefaults(t *testing.T) {
	application, runner, registry := newTestApplication(t)
	answer, err := application.RunOnce(context.Background(), "trace-1", "hello")
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if answer != "ok" {
		t.Fatalf("answer = %q, want ok", answer)
	}
	runner.mu.Lock()
	if len(runner.sessions) != 1 {
		runner.mu.Unlock()
		t.Fatalf("runner sessions = %d, want 1", len(runner.sessions))
	}
	runSession := runner.sessions[0]
	runner.mu.Unlock()
	if runSession.ModelRef.Model != "test-model" || runSession.TraceID != "trace-1" {
		t.Fatalf("session = %#v", runSession)
	}
	current, err := registry.Current(context.Background(), "oneshot:trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != runSession.ID {
		t.Fatalf("current ID = %q, runner ID = %q", current.ID, runSession.ID)
	}
}

func TestHandleReusesSessionForSameKey(t *testing.T) {
	application, runner, _ := newTestApplication(t)
	for _, input := range []string{"first", "second"} {
		out, err := application.Handle(context.Background(), channel.Inbound{
			Channel: "cli", SessionKey: "cli:default", Text: input,
		})
		if err != nil {
			t.Fatalf("Handle(%q) error = %v", input, err)
		}
		if out.Action != channel.ActionReply || out.Text != "ok" {
			t.Fatalf("out = %#v", out)
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.sessions) != 2 || runner.sessions[0].ID != runner.sessions[1].ID {
		t.Fatalf("sessions = %#v", runner.sessions)
	}
}

func TestHandleRejectsInvalidInbound(t *testing.T) {
	application, _, _ := newTestApplication(t)
	tests := []channel.Inbound{
		{SessionKey: "cli:default", Text: "hello"},
		{Channel: "cli", Text: "hello"},
		{Channel: "cli", SessionKey: "cli:default", Text: "   "},
	}
	for _, input := range tests {
		if _, err := application.Handle(context.Background(), input); err == nil {
			t.Fatalf("Handle(%#v) error = nil", input)
		}
	}
}

func TestHandleRoutesCommandWithoutCallingRunner(t *testing.T) {
	application, runner, _ := newTestApplication(t)
	out, err := application.Handle(context.Background(), channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "/help",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Action != channel.ActionReply || out.Text == "" {
		t.Fatalf("out = %#v", out)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.sessions) != 0 {
		t.Fatalf("runner called %d times", len(runner.sessions))
	}
}

func TestHandleReturnsRecoverableCommandError(t *testing.T) {
	application, _, _ := newTestApplication(t)
	_, err := application.Handle(context.Background(), channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "/unknown",
	})
	if err == nil || !channel.IsRecoverable(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleExitCommand(t *testing.T) {
	application, _, _ := newTestApplication(t)
	out, err := application.Handle(context.Background(), channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "/exit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Action != channel.ActionExit {
		t.Fatalf("out = %#v", out)
	}
}

func TestNewApplicationRejectsNilCommands(t *testing.T) {
	_, runner, registry := newTestApplication(t)
	_, err := NewApplication(Dependencies{
		Runner: runner, Sessions: registry, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil {
		t.Fatal("error = nil")
	}
}

func TestRunOnceCommandDoesNotCreateSessionOrCallRunner(t *testing.T) {
	application, runner, registry := newTestApplication(t)
	answer, err := application.RunOnce(context.Background(), "trace-command", "/help")
	if err != nil {
		t.Fatal(err)
	}
	if answer == "" {
		t.Fatal("answer is empty")
	}
	if _, err := registry.Current(context.Background(), "oneshot:trace-command"); !errors.Is(err, session.ErrNoCurrentSession) {
		t.Fatalf("Current() error = %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.sessions) != 0 {
		t.Fatalf("runner called %d times", len(runner.sessions))
	}
}
