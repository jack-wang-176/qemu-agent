package app

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type recordingRunner struct {
	session *session.Session
}

func (r *recordingRunner) Run(_ context.Context, sess *session.Session, _ string) (string, error) {
	r.session = sess
	return "ok", nil
}

type memoryStore struct {
	saved []*session.Session
}

func (s *memoryStore) Save(_ context.Context, sess *session.Session) error {
	s.saved = append(s.saved, sess)
	return nil
}
func (*memoryStore) Load(context.Context, string) (*session.Session, error) { return nil, nil }
func (*memoryStore) Delete(context.Context, string) error                   { return nil }
func (*memoryStore) List(context.Context) ([]session.Meta, error)           { return nil, nil }

func TestRunOnceCreatesSessionWithConfiguredModel(t *testing.T) {
	runner := &recordingRunner{}
	store := &memoryStore{}
	application, err := NewApplication(Dependencies{
		Runner: runner,
		Store:  store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, Config{DefaultModel: "test-model", SystemPrompt: "system"})
	if err != nil {
		t.Fatalf("NewApplication() error = %v", err)
	}
	answer, err := application.RunOnce(context.Background(), "trace-1", "hello")
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if answer != "ok" || runner.session == nil {
		t.Fatalf("unexpected result: answer=%q session=%v", answer, runner.session)
	}
	if runner.session.Model != "test-model" || runner.session.TraceID != "trace-1" {
		t.Fatalf("session = %#v", runner.session)
	}
	if len(store.saved) != 1 {
		t.Fatalf("initial saves = %d, want 1", len(store.saved))
	}
}
