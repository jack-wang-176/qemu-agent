package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type CandidateStatus string

const (
	CandidatePending  CandidateStatus = "pending"
	CandidateApproved CandidateStatus = "approved"
	CandidateRejected CandidateStatus = "rejected"
	CandidateExpired  CandidateStatus = "expired"
)

// Candidate is a proposal, not a memory. It is stored in a separate directory
// with a separate type so that no code path can accidentally retrieve an
// unreviewed line into a prompt: the prompt assembler only accepts Memory.
type Candidate struct {
	ID        string          `json:"id"`
	Kind      Kind            `json:"kind"`
	Scope     Scope           `json:"scope"`
	Content   string          `json:"content"`
	Source    string          `json:"source"`
	Status    CandidateStatus `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	MemoryID  string          `json:"memory_id,omitempty"`
}

type CandidateOptions struct {
	Root      string
	MaxItems  int
	TTL       time.Duration
	Sanitizer Sanitizer
	MaxBytes  int
	NewID     func() string
	Now       func() time.Time
}

type CandidateStore struct {
	mu        sync.Mutex
	dir       string
	maxItems  int
	maxBytes  int
	ttl       time.Duration
	sanitizer Sanitizer
	newID     func() string
	now       func() time.Time
	byID      map[string]Candidate
}

func OpenCandidateStore(opts CandidateOptions) (*CandidateStore, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("candidate root is empty")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("candidate root %q must be absolute", root)
	}
	if opts.MaxItems <= 0 || opts.MaxBytes <= 0 {
		return nil, errors.New("candidate limits must be > 0")
	}
	if opts.TTL <= 0 {
		return nil, errors.New("candidate ttl must be > 0")
	}
	if opts.Sanitizer == nil {
		return nil, errors.New("candidate sanitizer is nil")
	}
	if opts.NewID == nil || opts.Now == nil {
		return nil, errors.New("candidate store clock or id generator is nil")
	}
	dir := filepath.Join(filepath.Clean(root), "candidates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create candidate dir: %w", err)
	}
	store := &CandidateStore{
		dir: dir, maxItems: opts.MaxItems, maxBytes: opts.MaxBytes, ttl: opts.TTL,
		sanitizer: opts.Sanitizer, newID: opts.NewID, now: opts.Now,
		byID: make(map[string]Candidate),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *CandidateStore) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read candidate dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read candidate %s: %w", entry.Name(), err)
		}
		var item Candidate
		if err := json.Unmarshal(data, &item); err != nil {
			return fmt.Errorf("decode candidate %s: %w", entry.Name(), err)
		}
		if item.ID != strings.TrimSuffix(entry.Name(), ".json") || validateID(item.ID) != nil {
			return fmt.Errorf("candidate %s has an unusable id", entry.Name())
		}
		s.byID[item.ID] = item
	}
	return nil
}

// Add stores one proposal. The content passes the same sanitizer as a memory,
// because a rejected candidate still sits in a file an operator will read, and
// because approval must not be the first time a secret is checked for.
func (s *CandidateStore) Add(ctx context.Context, item Candidate) (Candidate, error) {
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	if _, err := ParseKind(string(item.Kind)); err != nil {
		return Candidate{}, err
	}
	if err := validateScope(item.Scope); err != nil {
		return Candidate{}, err
	}
	content, err := s.sanitizer.Sanitize(item.Content)
	if err != nil {
		return Candidate{}, err
	}
	if len(content) > s.maxBytes {
		return Candidate{}, fmt.Errorf("candidate content exceeds %d bytes", s.maxBytes)
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	fingerprint := Fingerprint(item.Scope, item.Kind, content)
	for _, existing := range s.byID {
		if existing.Status != CandidatePending {
			continue
		}
		if Fingerprint(existing.Scope, existing.Kind, existing.Content) == fingerprint {
			return existing, fmt.Errorf("%w: %s", ErrDuplicate, existing.ID)
		}
	}
	if s.pendingCountLocked() >= s.maxItems {
		return Candidate{}, fmt.Errorf("candidate queue is full (%d items)", s.maxItems)
	}
	id := strings.TrimSpace(s.newID())
	if validateID(id) != nil {
		return Candidate{}, errors.New("candidate id generator produced an unusable id")
	}
	if _, exists := s.byID[id]; exists {
		return Candidate{}, errors.New("candidate id generator collided")
	}
	item.ID, item.Content = id, content
	item.Source = normalizeText(item.Source)
	item.Status = CandidatePending
	item.CreatedAt, item.UpdatedAt = now, now
	item.ExpiresAt = now.Add(s.ttl)
	if err := s.writeLocked(item); err != nil {
		return Candidate{}, err
	}
	s.byID[item.ID] = item
	return item, nil
}

// ListPending is scoped like every other read: a reviewer sees proposals from
// their own turns and shared ones, never another user's private draft.
func (s *CandidateStore) ListPending(ctx context.Context, workspaceID, userID string) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, errors.New("candidate workspace id is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.now())
	result := make([]Candidate, 0, len(s.byID))
	for _, item := range s.byID {
		if item.Status != CandidatePending || !visibleTo(item.Scope, workspaceID, userID) {
			continue
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (s *CandidateStore) Get(ctx context.Context, id string, scope Scope) (Candidate, error) {
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	trimmed := strings.TrimSpace(id)
	if err := validateID(trimmed); err != nil {
		return Candidate{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.now())
	item, ok := s.byID[trimmed]
	if !ok || !writableBy(item.Scope, scope) {
		return Candidate{}, fmt.Errorf("%w: %s", ErrNotFound, trimmed)
	}
	return item, nil
}

// Resolve moves a pending candidate to a terminal state. It refuses to touch an
// already-resolved one, which is what makes approval idempotent: a repeated
// /memory approve cannot create a second memory from the same proposal.
func (s *CandidateStore) Resolve(ctx context.Context, id string, scope Scope, status CandidateStatus, memoryID string) (Candidate, error) {
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	if status != CandidateApproved && status != CandidateRejected {
		return Candidate{}, fmt.Errorf("invalid candidate resolution %q", status)
	}
	trimmed := strings.TrimSpace(id)
	if err := validateID(trimmed); err != nil {
		return Candidate{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.now())
	item, ok := s.byID[trimmed]
	if !ok || !writableBy(item.Scope, scope) {
		return Candidate{}, fmt.Errorf("%w: %s", ErrNotFound, trimmed)
	}
	if item.Status != CandidatePending {
		return item, fmt.Errorf("candidate %s is already %s", trimmed, item.Status)
	}
	item.Status, item.MemoryID, item.UpdatedAt = status, memoryID, s.now()
	if err := s.writeLocked(item); err != nil {
		return Candidate{}, err
	}
	s.byID[item.ID] = item
	return item, nil
}

// expireLocked ages out proposals lazily, on the read and write paths, instead
// of from a background goroutine. A queue nobody looks at needs no sweeping,
// and a timer would keep the process awake for work with no deadline.
func (s *CandidateStore) expireLocked(now time.Time) {
	for id, item := range s.byID {
		if item.Status != CandidatePending || now.Before(item.ExpiresAt) {
			continue
		}
		item.Status, item.UpdatedAt = CandidateExpired, now
		if err := s.writeLocked(item); err != nil {
			// An unwritable expiry is not worth failing a read for: the item is
			// already treated as expired in memory and will be retried.
			continue
		}
		s.byID[id] = item
	}
}

func (s *CandidateStore) pendingCountLocked() int {
	count := 0
	for _, item := range s.byID {
		if item.Status == CandidatePending {
			count++
		}
	}
	return count
}

func (s *CandidateStore) writeLocked(item Candidate) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return fmt.Errorf("encode candidate %q: %w", item.ID, err)
	}
	if err := atomicWriteFile(filepath.Join(s.dir, item.ID+".json"), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save candidate %q: %w", item.ID, err)
	}
	return nil
}
