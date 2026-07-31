package modeling

// store.go persists projects. It deliberately copies the shape memory.FileStore
// and session.FileStore already proved: one JSON file per project, a full
// in-memory index rebuilt at startup, atomic writes, and "not authorized" folded
// into "not found" so ids cannot be enumerated.
//
// The one addition is optimistic concurrency. A stage run takes minutes, so two
// /modeling advance commands can legitimately overlap; Save requires the caller
// to have started from the revision it is replacing, and the loser gets
// ErrConflict instead of interleaving writes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scope is the authorization tuple of one project. It mirrors memory.Scope minus
// visibility: a project is either the workspace's or its owner's.
type Scope struct {
	WorkspaceID string
	UserID      string
}

// Query selects projects for listing. Stage and Status are optional filters; an
// empty value means "any".
type Query struct {
	WorkspaceID string
	UserID      string
	Stage       Stage
	Status      Status
	Limit       int
}

// ProjectStore is the contract the Pipeline and the command layer depend on. It
// is declared next to its consumers so both can be tested against a fake.
type ProjectStore interface {
	Create(ctx context.Context, project Project) (Project, error)
	Get(ctx context.Context, id string, scope Scope) (Project, error)
	List(ctx context.Context, query Query) ([]Project, error)
	Save(ctx context.Context, project Project) (Project, error)
	Delete(ctx context.Context, id string, scope Scope) error
}

var ErrCapacity = errors.New("modeling project store is full")

// StoreOptions is a struct rather than a parameter list because every field is
// required and a positional call site would silently swap two of them.
type StoreOptions struct {
	Root        string           // <ModelingDir>/projects
	MaxProjects int              // refused loudly, never silently evicted
	NewID       func() string    // injected so tests get deterministic ids
	Now         func() time.Time // injected so tests get deterministic timestamps
	Logger      *slog.Logger     // used only for skipped-file warnings
}

// FileProjectStore keeps one JSON file per project plus the whole index in
// memory. MaxProjects bounds the corpus, so a full index costs less than
// re-reading the directory on every command.
type FileProjectStore struct {
	mu     sync.RWMutex
	root   string
	max    int
	newID  func() string
	now    func() time.Time
	logger *slog.Logger
	byID   map[string]Project
}

var _ ProjectStore = (*FileProjectStore)(nil)

// OpenFileProjectStore creates the directory and rebuilds the index. A corrupt
// file is warned about and skipped rather than fatal: one hand-edited project
// must not stop the whole agent from starting.
func OpenFileProjectStore(opts StoreOptions) (*FileProjectStore, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("modeling project root is empty")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("modeling project root %q must be absolute", root)
	}
	if opts.MaxProjects <= 0 {
		return nil, errors.New("modeling max projects must be > 0")
	}
	if opts.NewID == nil {
		return nil, errors.New("modeling id generator is nil")
	}
	if opts.Now == nil {
		return nil, errors.New("modeling clock is nil")
	}
	if opts.Logger == nil {
		return nil, errors.New("modeling logger is nil")
	}
	// 0700: a project title and its artifact names describe unreleased hardware.
	if err := os.MkdirAll(filepath.Clean(root), 0o700); err != nil {
		return nil, fmt.Errorf("create modeling project dir: %w", err)
	}
	store := &FileProjectStore{
		root:   filepath.Clean(root),
		max:    opts.MaxProjects,
		newID:  opts.NewID,
		now:    opts.Now,
		logger: opts.Logger,
		byID:   make(map[string]Project),
	}
	if err := store.loadExisting(); err != nil {
		return nil, err
	}
	return store, nil
}

// Len reports how many projects the index holds.
func (s *FileProjectStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// loadExisting rebuilds the index at startup. Without it Create would hand out
// an id that already exists on disk and Save would compare against an empty
// history.
func (s *FileProjectStore) loadExisting() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("read modeling project dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	// Sorted so a startup warning names the same file on every machine.
	sort.Strings(names)
	for _, name := range names {
		project, err := s.readProject(name)
		if err != nil {
			// Only the file name and the error reach the log; the project body
			// may quote a datasheet, so it is never logged.
			s.logger.Warn("skip unreadable modeling project", "file", name, "err", err)
			continue
		}
		if _, exists := s.byID[project.ID]; exists {
			s.logger.Warn("skip duplicate modeling project", "file", name, "id", project.ID)
			continue
		}
		s.byID[project.ID] = project
	}
	return nil
}

// readProject decodes one file strictly: an unknown field, an invalid state or a
// mismatch between the file name and the stored id means the file is not what
// this store wrote, and it is skipped rather than trusted.
func (s *FileProjectStore) readProject(name string) (Project, error) {
	data, err := os.ReadFile(filepath.Join(s.root, name))
	if err != nil {
		return Project{}, fmt.Errorf("read project: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var project Project
	if err := decoder.Decode(&project); err != nil {
		return Project{}, fmt.Errorf("decode project: %w", err)
	}
	if project.ID != strings.TrimSuffix(name, ".json") {
		return Project{}, errors.New("project stores a conflicting id")
	}
	if err := project.Validate(); err != nil {
		return Project{}, err
	}
	return project, nil
}

// Create assigns an id and writes revision 1. Only Title, WorkspaceID and UserID
// are taken from the caller; the stage and status are fixed here so a new project
// can never start half-way through the pipeline.
func (s *FileProjectStore) Create(ctx context.Context, project Project) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	if strings.TrimSpace(project.Title) == "" {
		return Project{}, errors.New("modeling project title is empty")
	}
	if strings.TrimSpace(project.WorkspaceID) == "" {
		return Project{}, errors.New("modeling project workspace id is empty")
	}

	now := s.now()
	fresh := Project{
		Title:       strings.TrimSpace(project.Title),
		WorkspaceID: project.WorkspaceID,
		UserID:      project.UserID,
		Current:     FirstStage(),
		Status:      StatusPending,
		Revision:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Capacity is refused loudly: silently evicting the oldest project would
	// delete the audit trail of a device somebody already shipped.
	if len(s.byID) >= s.max {
		return Project{}, fmt.Errorf("%w (%d projects)", ErrCapacity, s.max)
	}
	id, err := s.generateID()
	if err != nil {
		return Project{}, err
	}
	fresh.ID = id
	if err := fresh.Validate(); err != nil {
		return Project{}, err
	}
	if err := s.writeProject(fresh); err != nil {
		return Project{}, err
	}
	s.byID[fresh.ID] = fresh.Clone()
	return fresh.Clone(), nil
}

// generateID refuses an unusable or colliding id: a silent collision would
// overwrite another project's state.
func (s *FileProjectStore) generateID() (string, error) {
	for range 8 {
		candidate := strings.TrimSpace(s.newID())
		if ValidateProjectID(candidate) != nil {
			return "", errors.New("modeling id generator produced an unusable id")
		}
		if _, exists := s.byID[candidate]; !exists {
			return candidate, nil
		}
	}
	return "", errors.New("modeling id generator keeps colliding")
}

// Get returns a visible project. A project of another workspace answers
// not-found, so a user cannot confirm an id by watching the error change.
func (s *FileProjectStore) Get(ctx context.Context, id string, scope Scope) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	trimmed := strings.TrimSpace(id)
	if err := ValidateProjectID(trimmed); err != nil {
		return Project{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	project, ok := s.byID[trimmed]
	if !ok || !visibleTo(project, scope) {
		return Project{}, fmt.Errorf("%w: %s", ErrNotFound, trimmed)
	}
	return project.Clone(), nil
}

// List returns copies in a fixed order — newest update first, id as tiebreak —
// so /modeling list is reproducible across processes.
func (s *FileProjectStore) List(ctx context.Context, query Query) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query.WorkspaceID) == "" {
		return nil, errors.New("modeling query workspace id is empty")
	}
	scope := Scope{WorkspaceID: query.WorkspaceID, UserID: query.UserID}

	s.mu.RLock()
	projects := make([]Project, 0, len(s.byID))
	for _, project := range s.byID {
		if !visibleTo(project, scope) {
			continue
		}
		if query.Stage != "" && project.Current != query.Stage {
			continue
		}
		if query.Status != "" && project.Status != query.Status {
			continue
		}
		projects = append(projects, project.Clone())
	}
	s.mu.RUnlock()

	sort.SliceStable(projects, func(i, j int) bool {
		if !projects[i].UpdatedAt.Equal(projects[j].UpdatedAt) {
			return projects[i].UpdatedAt.After(projects[j].UpdatedAt)
		}
		return projects[i].ID < projects[j].ID
	})
	if query.Limit > 0 && len(projects) > query.Limit {
		projects = projects[:query.Limit]
	}
	return projects, nil
}

// Save is the only state commit point. The order is fixed: validate, authorize,
// check the revision against what is stored, write a temp file, rename, then
// update the index. A rejected save has touched neither disk nor index.
func (s *FileProjectStore) Save(ctx context.Context, project Project) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	if err := project.Validate(); err != nil {
		return Project{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.byID[project.ID]
	if !ok || !visibleTo(previous, Scope{WorkspaceID: project.WorkspaceID, UserID: project.UserID}) {
		return Project{}, fmt.Errorf("%w: %s", ErrNotFound, project.ID)
	}
	// Optimistic concurrency: the caller must have started from what is stored.
	if err := project.CanReplaceFrom(previous); err != nil {
		return Project{}, err
	}
	if err := s.writeProject(project); err != nil {
		return Project{}, err
	}
	s.byID[project.ID] = project.Clone()
	return project.Clone(), nil
}

// Delete removes the file first and the index second, so a crash between the two
// cannot leave a deleted project readable after a restart. A missing file counts
// as success: delete is idempotent.
func (s *FileProjectStore) Delete(ctx context.Context, id string, scope Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(id)
	if err := ValidateProjectID(trimmed); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.byID[trimmed]
	if !ok || !visibleTo(project, scope) {
		return fmt.Errorf("%w: %s", ErrNotFound, trimmed)
	}
	if err := os.Remove(filepath.Join(s.root, trimmed+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete modeling project %q: %w", trimmed, err)
	}
	delete(s.byID, trimmed)
	return nil
}

func (s *FileProjectStore) writeProject(project Project) error {
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return fmt.Errorf("encode modeling project %q: %w", project.ID, err)
	}
	path := filepath.Join(s.root, project.ID+".json")
	if err := atomicWriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save modeling project %q: %w", project.ID, err)
	}
	return nil
}

// visibleTo is the single implementation of the read rule. An empty workspace id
// never matches: a failed workspace derivation must hide everything rather than
// expose every project.
func visibleTo(project Project, scope Scope) bool {
	if project.WorkspaceID == "" || scope.WorkspaceID == "" || project.WorkspaceID != scope.WorkspaceID {
		return false
	}
	// A project without an owner belongs to the workspace; an owned project is
	// only reachable by that owner.
	if project.UserID == "" {
		return true
	}
	return scope.UserID != "" && project.UserID == scope.UserID
}

// atomicWriteFile never leaves a half-written project behind: readers see either
// the previous file or the complete new one. Sync before rename is what makes
// that true after a power loss, not just after a process crash.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
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
	// Chmod explicitly: CreateTemp makes 0600 files, but that is not something a
	// file holding datasheet excerpts should depend on.
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
