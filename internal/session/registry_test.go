package session

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type registryTestStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	saveErr  error
}

func (s *registryTestStore) Save(_ context.Context, sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.sessions[sess.ID] = cloneSession(sess)
	return nil
}

func (s *registryTestStore) Load(_ context.Context, id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSession(s.sessions[id]), nil
}

func (*registryTestStore) Delete(context.Context, string) error { return nil }

func (s *registryTestStore) List(context.Context) ([]Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Meta, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, Meta{ID: sess.ID, TraceID: sess.TraceID, Model: sess.Model, UpdatedAt: sess.UpdatedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func newRegistryForTest(t *testing.T) (*Registry, *registryTestStore) {
	t.Helper()
	store := &registryTestStore{sessions: make(map[string]*Session)}
	factory, err := NewDefaultFactory(Defaults{Model: "model", SystemPrompt: "system"}, func() string { return "trace" })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(store, factory)
	if err != nil {
		t.Fatal(err)
	}
	return registry, store
}

func TestRegistryListOrdersNewestFirst(t *testing.T) {
	registry, store := newRegistryForTest(t)
	now := time.Now()
	store.sessions["older"] = &Session{ID: "older", TraceID: "t1", Model: "m", UpdatedAt: now.Add(-time.Hour)}
	store.sessions["newer"] = &Session{ID: "newer", TraceID: "t2", Model: "m", UpdatedAt: now}
	items, err := registry.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "newer" || items[1].ID != "older" {
		t.Fatalf("items = %#v", items)
	}
}

func TestRegistryUpdateRollsBackCallbackFailure(t *testing.T) {
	registry, _ := newRegistryForTest(t)
	ctx := context.Background()
	if _, err := registry.New(ctx, "cli:default"); err != nil {
		t.Fatal(err)
	}
	want := errors.New("callback failed")
	err := registry.Update(ctx, "cli:default", func(sess *Session) error {
		sess.AddUser("should rollback")
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	current, _ := registry.Current(ctx, "cli:default")
	if len(current.Messages) != 1 || current.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("current = %#v", current)
	}
}

func TestRegistryUpdateRollsBackSaveFailure(t *testing.T) {
	registry, store := newRegistryForTest(t)
	ctx := context.Background()
	if _, err := registry.New(ctx, "cli:default"); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk full")
	err := registry.Update(ctx, "cli:default", func(sess *Session) error {
		sess.AddUser("should rollback")
		return nil
	})
	if err == nil {
		t.Fatal("error = nil")
	}
	current, _ := registry.Current(ctx, "cli:default")
	if len(current.Messages) != 1 || current.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("current = %#v", current)
	}
}

func TestRegistryUpdatePersistsSuccess(t *testing.T) {
	registry, store := newRegistryForTest(t)
	ctx := context.Background()
	created, err := registry.New(ctx, "cli:default")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Update(ctx, "cli:default", func(sess *Session) error {
		sess.AddUser("saved")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 || loaded.Messages[1].Content != "saved" {
		t.Fatalf("loaded = %#v", loaded)
	}
}
