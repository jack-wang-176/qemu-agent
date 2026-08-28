package modelingworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrBindingConflict = errors.New("modelingworkflow: binding version conflict")

type FileStore struct {
	dir string
	mu  sync.Mutex
}

func NewFileStore(dir string) (*FileStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("modelingworkflow: binding directory is empty")
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("modelingworkflow: binding directory must be absolute")
	}
	return &FileStore{dir: filepath.Clean(dir)}, nil
}

func (s *FileStore) Load(ctx context.Context, key BindingKey) (Binding, bool, error) {
	if err := ctx.Err(); err != nil {
		return Binding{}, false, err
	}
	if err := validateBindingKey(key); err != nil {
		return Binding{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(key)
}

func (s *FileStore) load(key BindingKey) (Binding, bool, error) {
	data, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return Binding{}, false, nil
	}
	if err != nil {
		return Binding{}, false, fmt.Errorf("modelingworkflow: read binding: %w", err)
	}
	var binding Binding
	if err := json.Unmarshal(data, &binding); err != nil {
		return Binding{}, false, fmt.Errorf("modelingworkflow: decode binding: %w", err)
	}
	if binding.Key != key {
		return Binding{}, false, errors.New("modelingworkflow: stored binding key mismatch")
	}
	return cloneBinding(binding), true, nil
}

func (s *FileStore) CompareAndSave(ctx context.Context, binding Binding, expected int) (Binding, error) {
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}
	if err := validateBindingKey(binding.Key); err != nil {
		return Binding{}, err
	}
	if expected < 0 {
		return Binding{}, errors.New("modelingworkflow: expected binding version must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, found, err := s.load(binding.Key)
	if err != nil {
		return Binding{}, err
	}
	if (!found && expected != 0) || (found && current.Version != expected) {
		return Binding{}, ErrBindingConflict
	}
	next := cloneBinding(binding)
	next.Version = expected + 1
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Binding{}, fmt.Errorf("modelingworkflow: create binding directory: %w", err)
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return Binding{}, fmt.Errorf("modelingworkflow: encode binding: %w", err)
	}
	if err := writeAtomic(s.dir, s.path(next.Key), data); err != nil {
		return Binding{}, err
	}
	return cloneBinding(next), nil
}

func (s *FileStore) Delete(ctx context.Context, key BindingKey, expected int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateBindingKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found, err := s.load(key)
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	if current.Version != expected {
		return ErrBindingConflict
	}
	if err := os.Remove(s.path(key)); err != nil {
		return fmt.Errorf("modelingworkflow: delete binding: %w", err)
	}
	return nil
}

func (s *FileStore) path(key BindingKey) string {
	payload, _ := json.Marshal(key)
	digest := sha256.Sum256(payload)
	return filepath.Join(s.dir, fmt.Sprintf("%x.json", digest))
}

func validateBindingKey(key BindingKey) error {
	if strings.TrimSpace(key.WorkspaceID) == "" || strings.TrimSpace(key.ConversationID) == "" {
		return errors.New("modelingworkflow: binding workspace and conversation are required")
	}
	if strings.TrimSpace(key.WorkspaceID) != key.WorkspaceID ||
		strings.TrimSpace(key.UserID) != key.UserID ||
		strings.TrimSpace(key.ConversationID) != key.ConversationID {
		return errors.New("modelingworkflow: binding key must be canonical")
	}
	return nil
}

func writeAtomic(dir, destination string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".binding-*.tmp")
	if err != nil {
		return fmt.Errorf("modelingworkflow: create binding temp file: %w", err)
	}
	name := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("modelingworkflow: set binding permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("modelingworkflow: write binding: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("modelingworkflow: sync binding: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelingworkflow: close binding: %w", err)
	}
	if err := os.Rename(name, destination); err != nil {
		return fmt.Errorf("modelingworkflow: commit binding: %w", err)
	}
	committed = true
	return nil
}
