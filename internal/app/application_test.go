package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
	"github.com/jack-wang-176/qemu-agent/internal/runstream"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type recordingRunner struct {
	mu       sync.Mutex
	sessions []*session.Session
	inputs   []agent.RunInput
}

func (r *recordingRunner) Run(_ context.Context, sess *session.Session, input agent.RunInput) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, sess)
	r.inputs = append(r.inputs, input)
	return "ok", nil
}

type appTestSink struct{ events []runstream.Event }

func (s *appTestSink) Emit(_ context.Context, event runstream.Event) error {
	s.events = append(s.events, event)
	return nil
}

// recordingRegistry is a SessionRegistry that never has to work: the command
// branch of Handle must not touch sessions at all, so a test that routes a
// command uses this to prove it.
type recordingRegistry struct{ calls int }

func (r *recordingRegistry) WithSession(_ context.Context, _ string, _ func(*session.Session) error) error {
	r.calls++
	return errors.New("the command branch must not open a session")
}

func (r *recordingRegistry) NewWithTrace(_ context.Context, _, _ string) (*session.Session, error) {
	r.calls++
	return nil, errors.New("the command branch must not create a session")
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
	application, runner, registry, _ := newTestApplicationWithKnowledge(t, &stubExtractor{})
	return application, runner, registry
}

func newTestApplicationWithKnowledge(t *testing.T, extractor memory.Extractor) (*Application, *recordingRunner, *session.Registry, *recordingCandidates) {
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
	commands, err := NewCommandRouter(
		testCommandDependencies(t, registry, testContextManager{}, newTestModels(t)),
		CommandConfig{MemoryTopK: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates := &recordingCandidates{}
	application, err := NewApplication(Dependencies{
		Runner:      runner,
		Sessions:    registry,
		Commands:    commands,
		Extractor:   extractor,
		Candidates:  candidates,
		WorkspaceID: testWorkspaceID,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return application, runner, registry, candidates
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
		out, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
			Channel: "cli", SessionKey: "cli:default", Text: input,
		}})
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

func TestHandlePassesRequestEventSinkToRunner(t *testing.T) {
	application, runner, _ := newTestApplication(t)
	sink := &appTestSink{}
	_, err := application.Handle(context.Background(), channel.Request{
		Inbound: channel.Inbound{Channel: "cli", SessionKey: "cli:default", Text: "hello"},
		Events:  sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.inputs) != 1 || runner.inputs[0].Events != sink {
		t.Fatalf("inputs=%#v", runner.inputs)
	}
}

// recordingCommands captures the CommandContext the application derives, which
// is the only place a command learns who is asking and what the transport can do.
type recordingCommands struct {
	contexts []CommandContext
}

func (r *recordingCommands) Execute(_ context.Context, cc CommandContext, _ Command) (CommandResult, error) {
	r.contexts = append(r.contexts, cc)
	return reply("ok"), nil
}

func TestCommandContextCarriesIdentityEventsAndInteractive(t *testing.T) {
	commands := &recordingCommands{}
	sink := &appTestSink{}
	application, err := NewApplication(Dependencies{
		Runner:      &recordingRunner{},
		Sessions:    &recordingRegistry{},
		Commands:    commands,
		Extractor:   &stubExtractor{},
		Candidates:  &recordingCandidates{},
		WorkspaceID: testWorkspaceID,
		NewTraceID:  func() string { return "trace-cmd" },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Handle(context.Background(), channel.Request{
		Inbound:      channel.Inbound{Channel: "telegram", SessionKey: "tg:42", UserID: "alice", Text: "/help"},
		Capabilities: channel.Capabilities{InteractiveApproval: true},
		Events:       sink,
	}); err != nil {
		t.Fatal(err)
	}
	if len(commands.contexts) != 1 {
		t.Fatalf("contexts = %#v", commands.contexts)
	}
	cc := commands.contexts[0]
	// Identity comes from the channel and the application, never from the typed
	// command, and the workspace is the application's own id.
	if cc.UserID != "alice" || cc.WorkspaceID != testWorkspaceID || cc.SessionKey != "tg:42" {
		t.Fatalf("identity = %#v", cc)
	}
	if cc.TraceID != "trace-cmd" {
		t.Fatalf("trace id = %q", cc.TraceID)
	}
	if !cc.Interactive {
		t.Fatal("an interactive channel was reported as non-interactive")
	}
	// Events is usable and reaches the request's sink, so a long command can
	// report progress without knowing which channel it is on.
	if cc.Events == nil {
		t.Fatal("Events is nil")
	}
	if err := cc.Events.Emit(context.Background(), runstream.Event{Type: runstream.EventRunStarted}); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 || sink.events[0].TraceID != "trace-cmd" || sink.events[0].Channel != "telegram" {
		t.Fatalf("sink events = %#v", sink.events)
	}
}

// TestCommandContextEventsAreUsableWithoutASink pins the "no nil checks in
// command code" invariant: a request with no event consumer still gets an
// emitter.
func TestCommandContextEventsAreUsableWithoutASink(t *testing.T) {
	commands := &recordingCommands{}
	application, err := NewApplication(Dependencies{
		Runner:      &recordingRunner{},
		Sessions:    &recordingRegistry{},
		Commands:    commands,
		Extractor:   &stubExtractor{},
		Candidates:  &recordingCandidates{},
		WorkspaceID: testWorkspaceID,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Handle(context.Background(), channel.Request{
		Inbound: channel.Inbound{Channel: "cli", SessionKey: "cli:default", Text: "/help"},
	}); err != nil {
		t.Fatal(err)
	}
	cc := commands.contexts[0]
	if cc.Interactive {
		t.Fatal("a request without capabilities was reported as interactive")
	}
	if err := cc.Events.Emit(context.Background(), runstream.Event{Type: runstream.EventRunStarted}); err != nil {
		t.Fatalf("emit without a sink = %v", err)
	}
	// A generated trace id is still an id: a command must always be able to
	// correlate its audit entries.
	if strings.TrimSpace(cc.TraceID) == "" {
		t.Fatal("trace id is empty")
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
		if _, err := application.Handle(context.Background(), channel.Request{Inbound: input}); err == nil {
			t.Fatalf("Handle(%#v) error = nil", input)
		}
	}
}

func TestHandleRoutesCommandWithoutCallingRunner(t *testing.T) {
	application, runner, _ := newTestApplication(t)
	out, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "/help",
	}})
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
	_, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "/unknown",
	}})
	if err == nil || !channel.IsRecoverable(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleExitCommand(t *testing.T) {
	application, _, _ := newTestApplication(t)
	out, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "/exit",
	}})
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
