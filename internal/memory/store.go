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

// Store is the contract the rest of the process depends on. It is declared here
// so commands, the prompt assembler and the extractor can be tested against a
// fake without touching the filesystem.
type Store interface {
	Save(ctx context.Context, item Memory) (Memory, error)
	Get(ctx context.Context, id string, scope Scope) (Memory, error)
	List(ctx context.Context, query Query) ([]Memory, error)
	Delete(ctx context.Context, id string, scope Scope) error
	Search(ctx context.Context, query Query) ([]Match, error)
	Touch(ctx context.Context, ids []string, now time.Time) error
}

// Options is a struct rather than a parameter list because every field is
// required and a positional call site would silently swap two of them.
type Options struct {
	Root      string
	Limits    Limits
	HalfLife  time.Duration
	Sanitizer Sanitizer
	NewID     func() string
	Now       func() time.Time
}

// FileStore keeps one JSON file per item and the whole index in memory. The
// corpus is bounded by Limits.MaxItems, so a full in-memory index costs less
// than re-reading the directory on every turn — and it makes the request path
// allocation-only, never I/O-bound.
type FileStore struct {
	mu        sync.RWMutex
	itemsDir  string
	maxItems  int
	maxBytes  int
	sanitizer Sanitizer
	ranker    Ranker
	newID     func() string
	now       func() time.Time
	byID      map[string]Memory
	byHash    map[string]string
	ordered   []string
}

var _ Store = (*FileStore)(nil)

func OpenFileStore(opts Options) (*FileStore, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("memory root is empty")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("memory root %q must be absolute", root)
	}
	if opts.Limits.MaxItems <= 0 || opts.Limits.MaxItemBytes <= 0 {
		return nil, errors.New("memory limits must be > 0")
	}
	if opts.Sanitizer == nil {
		return nil, errors.New("memory sanitizer is nil")
	}
	if opts.NewID == nil {
		return nil, errors.New("memory id generator is nil")
	}
	if opts.Now == nil {
		return nil, errors.New("memory clock is nil")
	}
	ranker, err := NewRanker(opts.HalfLife)
	if err != nil {
		return nil, err
	}
	itemsDir := filepath.Join(filepath.Clean(root), "items")
	// 0700, not 0755: remembered facts are private to the operator account.
	if err := os.MkdirAll(itemsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}
	store := &FileStore{
		itemsDir:  itemsDir,
		maxItems:  opts.Limits.MaxItems,
		maxBytes:  opts.Limits.MaxItemBytes,
		sanitizer: opts.Sanitizer,
		ranker:    ranker,
		newID:     opts.NewID,
		now:       opts.Now,
		byID:      make(map[string]Memory),
		byHash:    make(map[string]string),
	}
	if err := store.loadExisting(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *FileStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// loadExisting rebuilds the index at startup. Skipping it would be the worst
// kind of bug: Save would deduplicate against an empty index and Delete would
// report not-found for items the operator can see on disk.
func (s *FileStore) loadExisting() error {
	entries, err := os.ReadDir(s.itemsDir)
	if err != nil {
		return fmt.Errorf("read memory dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	// Sorted so the surviving item of a hand-made collision is the same on every
	// machine, and so a startup failure names the same file every time.
	sort.Strings(names)
	if len(names) > s.maxItems {
		return fmt.Errorf("memory store holds %d items; limit is %d", len(names), s.maxItems)
	}
	for _, name := range names {
		item, err := s.readItem(name)
		if err != nil {
			return err
		}
		if _, exists := s.byID[item.ID]; exists {
			return fmt.Errorf("memory %q is stored twice", item.ID)
		}
		if other, exists := s.byHash[item.Fingerprint]; exists {
			return fmt.Errorf("memory %q duplicates %q; remove one file", item.ID, other)
		}
		s.byID[item.ID] = item
		s.byHash[item.Fingerprint] = item.ID
		s.ordered = append(s.ordered, item.ID)
	}
	return nil
}

func (s *FileStore) readItem(name string) (Memory, error) {
	path := filepath.Join(s.itemsDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return Memory{}, fmt.Errorf("read memory %s: %w", name, err)
	}
	var item Memory
	if err := json.Unmarshal(data, &item); err != nil {
		return Memory{}, fmt.Errorf("decode memory %s: %w", name, err)
	}
	if item.ID != strings.TrimSuffix(name, ".json") {
		return Memory{}, fmt.Errorf("memory %s stores conflicting id %q", name, item.ID)
	}
	if err := validateID(item.ID); err != nil {
		return Memory{}, fmt.Errorf("memory %s has an unusable id", name)
	}
	created := item.CreatedAt
	// Re-derive keywords and fingerprint instead of trusting the file: an item
	// edited by hand would otherwise keep an index that no longer describes its
	// content, and retrieval would quietly stop finding it.
	normalized, err := normalizeMemory(item, s.sanitizer, s.maxBytes, item.UpdatedAt)
	if err != nil {
		return Memory{}, fmt.Errorf("memory %s is not storable: %w", name, err)
	}
	normalized.CreatedAt = created
	normalized.UpdatedAt = item.UpdatedAt
	return normalized, nil
}

// Save creates or updates one item. An empty id means create; a present id
// means update in place and requires the caller to own the item.
func (s *FileStore) Save(ctx context.Context, item Memory) (Memory, error) {
	if err := ctx.Err(); err != nil {
		return Memory{}, err
	}
	requestedID := strings.TrimSpace(item.ID)
	normalized, err := normalizeMemory(item, s.sanitizer, s.maxBytes, s.now())
	if err != nil {
		return Memory{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if requestedID == "" {
		if existingID, exists := s.byHash[normalized.Fingerprint]; exists {
			// The existing item is returned with the error so a command can say
			// "already remembered as <id>" instead of writing a second copy.
			return cloneMemory(s.byID[existingID]), fmt.Errorf("%w: %s", ErrDuplicate, existingID)
		}
		if len(s.byID) >= s.maxItems {
			return Memory{}, fmt.Errorf("memory store is full (%d items)", s.maxItems)
		}
		id, err := s.generateID()
		if err != nil {
			return Memory{}, err
		}
		normalized.ID = id
	} else {
		previous, ok := s.byID[requestedID]
		// Not-found and not-yours return the same error: distinguishing them
		// would turn Save into an oracle for other users' ids.
		if !ok || !writableBy(previous.Scope, item.Scope) {
			return Memory{}, fmt.Errorf("%w: %s", ErrNotFound, requestedID)
		}
		if existingID, exists := s.byHash[normalized.Fingerprint]; exists && existingID != requestedID {
			return cloneMemory(s.byID[existingID]), fmt.Errorf("%w: %s", ErrDuplicate, existingID)
		}
		normalized.ID = requestedID
		normalized.CreatedAt = previous.CreatedAt
		normalized.UseCount = previous.UseCount
		normalized.LastUsedAt = previous.LastUsedAt
		delete(s.byHash, previous.Fingerprint)
	}

	if err := s.writeItem(normalized); err != nil {
		// The index is only updated after the rename below, so a failed write
		// leaves the store exactly as it was — except for the hash entry the
		// update path removed, which is restored here.
		if requestedID != "" {
			if previous, ok := s.byID[requestedID]; ok {
				s.byHash[previous.Fingerprint] = previous.ID
			}
		}
		return Memory{}, err
	}
	if requestedID == "" {
		s.ordered = append(s.ordered, normalized.ID)
	}
	s.byID[normalized.ID] = cloneMemory(normalized)
	s.byHash[normalized.Fingerprint] = normalized.ID
	return cloneMemory(normalized), nil
}

// generateID rejects an id the store could not turn back into a file name, and
// refuses to reuse one. A silent collision would overwrite another fact.
func (s *FileStore) generateID() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		candidate := strings.TrimSpace(s.newID())
		if validateID(candidate) != nil {
			return "", fmt.Errorf("memory id generator produced an unusable id")
		}
		if _, exists := s.byID[candidate]; !exists {
			return candidate, nil
		}
	}
	return "", errors.New("memory id generator keeps colliding")
}

func (s *FileStore) writeItem(item Memory) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return fmt.Errorf("encode memory %q: %w", item.ID, err)
	}
	path := filepath.Join(s.itemsDir, item.ID+".json")
	if err := atomicWriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save memory %q: %w", item.ID, err)
	}
	return nil
}

func (s *FileStore) Get(ctx context.Context, id string, scope Scope) (Memory, error) {
	if err := ctx.Err(); err != nil {
		return Memory{}, err
	}
	if err := validateID(strings.TrimSpace(id)); err != nil {
		return Memory{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.byID[strings.TrimSpace(id)]
	if !ok || !visibleTo(item.Scope, scope.WorkspaceID, scope.UserID) {
		return Memory{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return cloneMemory(item), nil
}

// List returns every visible item, newest first. It ignores Query.Text: listing
// is for humans reviewing what the agent knows, and filtering that by relevance
// would hide exactly the stale item they are looking for.
func (s *FileStore) List(ctx context.Context, query Query) ([]Memory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query.WorkspaceID) == "" {
		return nil, errors.New("memory query workspace id is empty")
	}
	s.mu.RLock()
	items := make([]Memory, 0, len(s.ordered))
	for _, id := range s.ordered {
		item := s.byID[id]
		if visibleTo(item.Scope, query.WorkspaceID, query.UserID) && kindAllowed(item.Kind, query.Kinds) {
			items = append(items, cloneMemory(item))
		}
	}
	s.mu.RUnlock()
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ID < items[j].ID
	})
	if query.TopK > 0 && len(items) > query.TopK {
		items = items[:query.TopK]
	}
	return items, nil
}

// Delete removes the file first and the index second. The reverse order would
// leave a forgotten fact readable after a crash, which is the one failure a
// forget command may not have.
func (s *FileStore) Delete(ctx context.Context, id string, scope Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(id)
	if err := validateID(trimmed); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.byID[trimmed]
	// An unauthorized delete answers not-found, so a user cannot enumerate other
	// users' ids by watching the error change.
	if !ok || !writableBy(item.Scope, scope) {
		return fmt.Errorf("%w: %s", ErrNotFound, trimmed)
	}
	if err := os.Remove(filepath.Join(s.itemsDir, trimmed+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete memory %q: %w", trimmed, err)
	}
	delete(s.byID, trimmed)
	delete(s.byHash, item.Fingerprint)
	s.ordered = removeID(s.ordered, trimmed)
	return nil
}

// Touch records that items were actually injected into a prompt. It is a
// separate call, made after the run, precisely so Search stays read-only: a
// write on the request path would serialize concurrent sessions on one mutex
// and make retrieval order depend on who queried first.
func (s *FileStore) Touch(ctx context.Context, ids []string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if now.IsZero() {
		now = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, id := range ids {
		item, ok := s.byID[strings.TrimSpace(id)]
		if !ok {
			continue
		}
		item.UseCount++
		item.LastUsedAt = now
		if err := s.writeItem(item); err != nil {
			errs = append(errs, err)
			continue
		}
		s.byID[item.ID] = cloneMemory(item)
	}
	return errors.Join(errs...)
}

func removeID(ids []string, id string) []string {
	for index, candidate := range ids {
		if candidate == id {
			return append(ids[:index:index], ids[index+1:]...)
		}
	}
	return ids
}

// atomicWriteFile never leaves a half-written item behind: readers either see
// the previous file or the complete new one. Sync before rename is what makes
// that true after a power loss, not just after a process crash.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	name := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(name)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	// Chmod explicitly: CreateTemp makes 0600 files, but an inherited umask on
	// some systems is not something a secret-bearing file should depend on.
	if err := temp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
