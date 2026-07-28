package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

var (
	ErrEmptySessionKey  = errors.New("session key is empty")
	ErrNoCurrentSession = errors.New("session key has no current session")
)

// Factory creates sessions without exposing construction details.
type Factory interface {
	New(traceID string) *Session
}

// Registry maps external session keys to process-local sessions. The map lock
// protects entries only; each entry serializes one session key independently.
type Registry struct {
	mu             sync.RWMutex
	entries        map[string]*entry
	store          Store
	factory        Factory
	models         llm.ModelResolver
	legacyProvider string
}

type entry struct {
	mu        sync.Mutex
	sessionID string
	session   *Session
}

func NewRegistry(store Store, factory Factory, models llm.ModelResolver, legacyProvider string) (*Registry, error) {
	if store == nil {
		return nil, errors.New("session registry store is nil")
	}
	if factory == nil {
		return nil, errors.New("session registry factory is nil")
	}
	if models == nil {
		return nil, errors.New("session registry model resolver is nil")
	}
	if strings.TrimSpace(legacyProvider) == "" {
		return nil, errors.New("session registry legacy provider is empty")
	}
	return &Registry{entries: make(map[string]*entry), store: store, factory: factory, models: models, legacyProvider: strings.ToLower(strings.TrimSpace(legacyProvider))}, nil
}

func validateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrEmptySessionKey
	}
	return nil
}

func validateSession(sess *Session) error {
	if sess == nil {
		return errors.New("session is nil")
	}
	if strings.TrimSpace(sess.ID) == "" {
		return errors.New("session id is empty")
	}
	if strings.TrimSpace(sess.TraceID) == "" {
		return errors.New("session trace id is empty")
	}
	if strings.TrimSpace(sess.ModelRef.Model) == "" {
		return errors.New("session model is empty")
	}
	return nil
}

func (r *Registry) lookupEntry(key string) (*entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.entries[key]
	return item, ok
}

func (r *Registry) getOrCreateEntry(key string) *entry {
	if item, ok := r.lookupEntry(key); ok {
		return item
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if item := r.entries[key]; item != nil {
		return item
	}
	item := &entry{}
	r.entries[key] = item
	return item
}

func (r *Registry) createAndSave(ctx context.Context, traceID string) (*Session, error) {
	created := r.factory.New(traceID)
	if err := validateSession(created); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	if err := r.store.Save(ctx, created); err != nil {
		return nil, fmt.Errorf("save initial session: %w", err)
	}
	return created, nil
}

func cloneSession(sess *Session) *Session {
	return sess.Clone()
}

func systemMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == llm.RoleSystem {
			result = append(result, message)
		}
	}
	return result
}

// WithSession serializes access to one session key and lazily creates its
// first session before invoking fn.
func (r *Registry) WithSession(ctx context.Context, key string, fn func(*Session) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("session callback is nil")
	}
	item := r.getOrCreateEntry(key)
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.session == nil {
		created, err := r.createAndSave(ctx, "")
		if err != nil {
			return err
		}
		item.session = created
		item.sessionID = created.ID
	}
	return fn(item.session)
}

func (r *Registry) New(ctx context.Context, key string) (*Session, error) {
	return r.newWithTrace(ctx, key, "")
}

// NewWithTrace creates a fresh binding using a caller-owned request trace.
func (r *Registry) NewWithTrace(ctx context.Context, key, traceID string) (*Session, error) {
	if strings.TrimSpace(traceID) == "" {
		return nil, errors.New("trace id is empty")
	}
	return r.newWithTrace(ctx, key, traceID)
}

func (r *Registry) newWithTrace(ctx context.Context, key, traceID string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	item := r.getOrCreateEntry(key)
	item.mu.Lock()
	defer item.mu.Unlock()
	created, err := r.createAndSave(ctx, traceID)
	if err != nil {
		return nil, err
	}
	item.session = created
	item.sessionID = created.ID
	return cloneSession(created), nil
}

// Current returns a defensive copy without creating a missing entry.
func (r *Registry) Current(ctx context.Context, key string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	item, ok := r.lookupEntry(key)
	if !ok {
		return nil, ErrNoCurrentSession
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.session == nil {
		return nil, ErrNoCurrentSession
	}
	return cloneSession(item.session), nil
}

// Resume replaces a key binding only after loading and validating the target.
func (r *Registry) Resume(ctx context.Context, key, id string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("session id is empty")
	}
	item := r.getOrCreateEntry(key)
	item.mu.Lock()
	defer item.mu.Unlock()
	loaded, err := r.store.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load session %q: %w", id, err)
	}
	if err := validateSession(loaded); err != nil {
		return nil, fmt.Errorf("validate session %q: %w", id, err)
	}
	if loaded.ModelRef.Provider == "" {
		loaded.ModelRef.Provider = r.legacyProvider
	}
	ref, err := llm.NormalizeModelRef(loaded.ModelRef)
	if err != nil {
		return nil, fmt.Errorf("normalize session %q model: %w", id, err)
	}
	loaded.ModelRef = ref
	if _, err := r.models.Resolve(ref); err != nil {
		return nil, fmt.Errorf("resolve session %q model %q: %w", id, ref.String(), err)
	}
	item.session = loaded
	item.sessionID = loaded.ID
	return cloneSession(loaded), nil
}

// Reset preserves identity, trace, model, creation time, and system messages.
func (r *Registry) Reset(ctx context.Context, key string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	item, ok := r.lookupEntry(key)
	if !ok {
		return nil, ErrNoCurrentSession
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.session == nil {
		return nil, ErrNoCurrentSession
	}
	reset := &Session{
		ID:         item.session.ID,
		TraceID:    item.session.TraceID,
		ModelRef:   item.session.ModelRef,
		Messages:   systemMessages(item.session.Messages),
		TokenUsage: 0,
		CreatedAt:  item.session.CreatedAt,
		UpdatedAt:  time.Now(),
	}
	if err := r.store.Save(ctx, reset); err != nil {
		return nil, fmt.Errorf("save reset session: %w", err)
	}
	item.session = reset
	item.sessionID = reset.ID
	return cloneSession(reset), nil
}

// List returns persisted session metadata ordered by most recent update.
func (r *Registry) List(ctx context.Context) ([]Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items, err := r.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	result := append([]Meta(nil), items...)
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

// Update mutates the current session while holding its entry lock and rolls
// back the in-memory value if the callback or persistence fails.
func (r *Registry) Update(ctx context.Context, key string, fn func(*Session) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("session update callback is nil")
	}
	item, ok := r.lookupEntry(key)
	if !ok {
		return ErrNoCurrentSession
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.session == nil {
		return ErrNoCurrentSession
	}

	before := cloneSession(item.session)
	if err := fn(item.session); err != nil {
		item.session = before
		item.sessionID = before.ID
		return err
	}
	if err := r.store.Save(ctx, item.session); err != nil {
		item.session = before
		item.sessionID = before.ID
		return fmt.Errorf("save updated session: %w", err)
	}
	return nil
}
