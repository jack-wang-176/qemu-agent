package modelingworkflow

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestFileStoreMissingAndCompareAndSave(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "bindings"))
	if err != nil {
		t.Fatal(err)
	}
	key := BindingKey{WorkspaceID: "workspace-1", UserID: "user-1", ConversationID: "session-1"}
	if _, found, err := store.Load(context.Background(), key); err != nil || found {
		t.Fatalf("missing Load() = found %v, error %v", found, err)
	}

	saved, err := store.CompareAndSave(context.Background(), Binding{Key: key, Title: "UART"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 {
		t.Fatalf("saved version = %d, want 1", saved.Version)
	}
	loaded, found, err := store.Load(context.Background(), key)
	if err != nil || !found || loaded.Version != 1 || loaded.Title != "UART" {
		t.Fatalf("Load() = %#v, %v, %v", loaded, found, err)
	}
	if _, err := store.CompareAndSave(context.Background(), saved, 0); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("stale save error = %v, want conflict", err)
	}

	saved.Title = "UART v2"
	saved, err = store.CompareAndSave(context.Background(), saved, 1)
	if err != nil || saved.Version != 2 {
		t.Fatalf("second save = %#v, %v", saved, err)
	}
	if err := store.Delete(context.Background(), key, 1); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("stale delete error = %v, want conflict", err)
	}
	if err := store.Delete(context.Background(), key, 2); err != nil {
		t.Fatal(err)
	}
}
