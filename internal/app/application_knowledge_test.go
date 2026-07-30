package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/channel"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
)

// stubExtractor records what the hook was allowed to see. The assertion that
// matters is negative: it must never receive tool output, the system prompt or
// the rest of the history.
type stubExtractor struct {
	mu         sync.Mutex
	turns      []memory.Turn
	scopes     []memory.Scope
	candidates []memory.Candidate
	err        error
}

func (e *stubExtractor) Extract(_ context.Context, turn memory.Turn, scope memory.Scope) ([]memory.Candidate, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.turns = append(e.turns, turn)
	e.scopes = append(e.scopes, scope)
	if e.err != nil {
		return nil, e.err
	}
	out := make([]memory.Candidate, 0, len(e.candidates))
	for _, candidate := range e.candidates {
		candidate.Scope = scope
		out = append(out, candidate)
	}
	return out, nil
}

// The zero value proposes nothing, which is what every test that does not care
// about extraction wants.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recordingCandidates struct {
	mu    sync.Mutex
	added []memory.Candidate
	err   error
}

func (c *recordingCandidates) Add(_ context.Context, item memory.Candidate) (memory.Candidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return memory.Candidate{}, c.err
	}
	item.ID = "cand-" + item.Content
	c.added = append(c.added, item)
	return item, nil
}

func (c *recordingCandidates) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.added)
}

func TestHandleProposesCandidatesFromTheFinalExchangeOnly(t *testing.T) {
	extractor := &stubExtractor{candidates: []memory.Candidate{{Kind: memory.KindFact, Content: "boots from NOR"}}}
	application, _, _, candidates := newTestApplicationWithKnowledge(t, extractor)

	out, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "telegram", SessionKey: "tg:1", UserID: "alice", Text: "how does it boot",
	}})
	if err != nil || out.Text != "ok" {
		t.Fatalf("out = %#v, err = %v", out, err)
	}
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if len(extractor.turns) != 1 {
		t.Fatalf("extractor ran %d times", len(extractor.turns))
	}
	turn := extractor.turns[0]
	if turn.User != "how does it boot" || turn.Assistant != "ok" {
		t.Fatalf("turn = %#v", turn)
	}
	scope := extractor.scopes[0]
	// A named user gets a private proposal: an unreviewed line about one person
	// must not default to being shared with the whole workspace.
	if scope.WorkspaceID != testWorkspaceID || scope.UserID != "alice" || scope.Visibility != memory.VisibilityPrivate {
		t.Fatalf("scope = %#v", scope)
	}
	if candidates.count() != 1 {
		t.Fatalf("stored %d candidates", candidates.count())
	}
}

func TestHandleProposesWorkspaceScopeWithoutAUser(t *testing.T) {
	extractor := &stubExtractor{candidates: []memory.Candidate{{Kind: memory.KindFact, Content: "uses make"}}}
	application, _, _, _ := newTestApplicationWithKnowledge(t, extractor)

	if _, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "how do I build",
	}}); err != nil {
		t.Fatal(err)
	}
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if scope := extractor.scopes[0]; scope.UserID != "" || scope.Visibility != memory.VisibilityWorkspace {
		t.Fatalf("scope = %#v", scope)
	}
}

func TestHandleSucceedsWhenTheExtractorOrQueueFails(t *testing.T) {
	extractor := &stubExtractor{err: errors.New("model unavailable")}
	application, _, _, _ := newTestApplicationWithKnowledge(t, extractor)
	// The answer is already correct and already produced. A knowledge write is
	// not allowed to turn it into a failed request.
	out, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "hello",
	}})
	if err != nil || out.Text != "ok" {
		t.Fatalf("out = %#v, err = %v", out, err)
	}

	failing := &stubExtractor{candidates: []memory.Candidate{{Kind: memory.KindFact, Content: "a fact"}}}
	application, _, _, candidates := newTestApplicationWithKnowledge(t, failing)
	candidates.err = errors.New("queue full")
	if _, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "hello",
	}}); err != nil {
		t.Fatalf("a failed queue write broke the request: %v", err)
	}
}

func TestHandleDoesNotProposeForCommands(t *testing.T) {
	extractor := &stubExtractor{}
	application, _, _, _ := newTestApplicationWithKnowledge(t, extractor)
	if _, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "cli", SessionKey: "cli:default", Text: "/help",
	}}); err != nil {
		t.Fatal(err)
	}
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if len(extractor.turns) != 0 {
		t.Fatalf("a command was extracted from: %#v", extractor.turns)
	}
}

func TestHandlePassesIdentityToTheRunner(t *testing.T) {
	application, runner, _, _ := newTestApplicationWithKnowledge(t, &stubExtractor{})
	if _, err := application.Handle(context.Background(), channel.Request{Inbound: channel.Inbound{
		Channel: "telegram", SessionKey: "tg:7", UserID: "bob", Text: "hello",
	}}); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	input := runner.inputs[0]
	// Recall is authorized by these two fields, so the loop must receive them
	// from the channel instead of parsing the session key.
	if input.UserID != "bob" || input.WorkspaceID != testWorkspaceID {
		t.Fatalf("input = %#v", input)
	}
}

func TestNewApplicationRejectsMissingKnowledgeDependencies(t *testing.T) {
	_, runner, registry, _ := newTestApplicationWithKnowledge(t, &stubExtractor{})
	commands, err := NewCommandRouter(
		testCommandDependencies(t, registry, testContextManager{}, newTestModels(t)),
		CommandConfig{MemoryTopK: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	base := Dependencies{
		Runner: runner, Sessions: registry, Commands: commands,
		Extractor: &stubExtractor{}, Candidates: &recordingCandidates{},
		WorkspaceID: testWorkspaceID, Logger: testLogger(),
	}
	for name, mutate := range map[string]func(*Dependencies){
		"extractor":  func(d *Dependencies) { d.Extractor = nil },
		"candidates": func(d *Dependencies) { d.Candidates = nil },
		"workspace":  func(d *Dependencies) { d.WorkspaceID = "  " },
	} {
		deps := base
		mutate(&deps)
		if _, err := NewApplication(deps); err == nil {
			t.Fatalf("error = nil for a missing %s", name)
		}
	}
	if _, err := NewApplication(base); err != nil {
		t.Fatal(err)
	}
}

func TestErrorKindNeverLeaksContent(t *testing.T) {
	secret := errors.New("memory content looks like a secret: ghp_abcdefghijklmnop")
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{context.Canceled, "canceled"},
		{memory.ErrSensitiveContent, "sensitive-content"},
		{memory.ErrPromptControl, "prompt-control"},
		{memory.ErrDuplicate, "duplicate"},
		{memory.ErrDisabled, "disabled"},
		{secret, "other"},
	} {
		if got := errorKind(test.err); got != test.want {
			t.Fatalf("errorKind(%v) = %q, want %q", test.err, got, test.want)
		}
	}
	// The whole point: what is logged is a category, not the message.
	if strings.Contains(errorKind(secret), "ghp_") {
		t.Fatal("errorKind echoed the error text")
	}
}
