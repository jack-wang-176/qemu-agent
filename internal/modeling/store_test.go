package modeling

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sequentialIDs hands out deterministic ids so a test can assert on them.
func sequentialIDs() func() string {
	counter := 0
	return func() string {
		counter++
		return fmt.Sprintf("mp-%016x", counter)
	}
}

func openStore(t *testing.T, root string, max int) *FileProjectStore {
	t.Helper()
	store, err := OpenFileProjectStore(StoreOptions{
		Root: root, MaxProjects: max, NewID: sequentialIDs(),
		Now: func() time.Time { return at(1000) }, Logger: testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func createProject(t *testing.T, store *FileProjectStore, title, workspace string) Project {
	t.Helper()
	project, err := store.Create(context.Background(), Project{Title: title, WorkspaceID: workspace})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func TestCreateStartsAtTheFirstStage(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "projects"), 10)
	project := createProject(t, store, "pl011 clone", "ws-1111")
	if project.Current != FirstStage() || project.Status != StatusPending || project.Revision != 1 {
		t.Fatalf("project = %#v", project)
	}
	// The caller's stage and status are ignored: a project may not be created
	// half-way through the pipeline.
	forced, err := store.Create(context.Background(), Project{
		Title: "forced", WorkspaceID: "ws-1111", Current: StageVerify, Status: StatusDone, Revision: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if forced.Current != FirstStage() || forced.Status != StatusPending || forced.Revision != 1 {
		t.Fatalf("project = %#v", forced)
	}
}

func TestSaveRejectsStaleRevision(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "projects"), 10)
	project := createProject(t, store, "pl011", "ws-1111")

	first := project.Clone()
	if err := first.Begin(StagePlan, at(2000)); err != nil {
		t.Fatal(err)
	}
	first.Revision++
	if _, err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	// The second writer started from revision 1 and must lose.
	second := project.Clone()
	if err := second.Begin(StagePlan, at(3000)); err != nil {
		t.Fatal(err)
	}
	second.Revision++
	_, err := store.Save(context.Background(), second)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	stored, err := store.Get(context.Background(), project.ID, Scope{WorkspaceID: "ws-1111"})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.UpdatedAt.Equal(at(2000)) {
		t.Fatalf("the loser overwrote the winner: %#v", stored)
	}
}

func TestGetOtherWorkspaceReturnsNotFound(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "projects"), 10)
	project := createProject(t, store, "pl011", "ws-1111")

	_, err := store.Get(context.Background(), project.ID, Scope{WorkspaceID: "ws-2222"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// An unknown id gives exactly the same answer, so ids cannot be probed.
	_, unknown := store.Get(context.Background(), "mp-ffffffffffffffff", Scope{WorkspaceID: "ws-1111"})
	if !errors.Is(unknown, ErrNotFound) {
		t.Fatalf("err = %v", unknown)
	}
	if err := store.Delete(context.Background(), project.ID, Scope{WorkspaceID: "ws-2222"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete across workspaces = %v", err)
	}
}

// A private project belongs to one user; another user of the same workspace must
// not see it, and a request without a user id must not either.
func TestOwnedProjectIsPrivate(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "projects"), 10)
	owned, err := store.Create(context.Background(), Project{Title: "owned", WorkspaceID: "ws-1111", UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []Scope{
		{WorkspaceID: "ws-1111"},
		{WorkspaceID: "ws-1111", UserID: "bob"},
	} {
		if _, err := store.Get(context.Background(), owned.ID, scope); !errors.Is(err, ErrNotFound) {
			t.Fatalf("scope %#v could read another user's project: %v", scope, err)
		}
	}
	if _, err := store.Get(context.Background(), owned.ID, Scope{WorkspaceID: "ws-1111", UserID: "alice"}); err != nil {
		t.Fatal(err)
	}
}

func TestListIsFilteredSortedAndCopied(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	store, err := OpenFileProjectStore(StoreOptions{
		Root: root, MaxProjects: 10, NewID: sequentialIDs(),
		Now: func() time.Time { return at(1000) }, Logger: testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	older := createProject(t, store, "older", "ws-1111")
	newer := createProject(t, store, "newer", "ws-1111")
	createProject(t, store, "elsewhere", "ws-2222")

	advanced := newer.Clone()
	if err := advanced.Begin(StagePlan, at(5000)); err != nil {
		t.Fatal(err)
	}
	advanced.Revision++
	if _, err := store.Save(context.Background(), advanced); err != nil {
		t.Fatal(err)
	}

	projects, err := store.List(context.Background(), Query{WorkspaceID: "ws-1111"})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].ID != newer.ID || projects[1].ID != older.ID {
		t.Fatalf("projects = %#v; want newest update first", projects)
	}
	// Mutating a returned project must not reach the index.
	projects[0].Title = "hijacked"
	again, err := store.Get(context.Background(), newer.ID, Scope{WorkspaceID: "ws-1111"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Title != "newer" {
		t.Fatalf("title = %q", again.Title)
	}

	running, err := store.List(context.Background(), Query{WorkspaceID: "ws-1111", Status: StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0].ID != newer.ID {
		t.Fatalf("running = %#v", running)
	}
	limited, err := store.List(context.Background(), Query{WorkspaceID: "ws-1111", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited = %#v", limited)
	}
	if _, err := store.List(context.Background(), Query{}); err == nil {
		t.Fatal("listed without a workspace")
	}
}

func TestReopenRebuildsIndex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	first := openStore(t, root, 10)
	for index := range 3 {
		createProject(t, first, fmt.Sprintf("device-%d", index), "ws-1111")
	}

	second := openStore(t, root, 10)
	projects, err := second.List(context.Background(), Query{WorkspaceID: "ws-1111"})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 3 {
		t.Fatalf("projects = %d after reopen", len(projects))
	}
	// A rebuilt index must also know the revisions, or the next Save would be
	// rejected as a conflict against nothing.
	next := projects[0].Clone()
	if err := next.Begin(StagePlan, at(6000)); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	if _, err := second.Save(context.Background(), next); err != nil {
		t.Fatal(err)
	}
}

func TestReopenSkipsCorruptFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	first := openStore(t, root, 10)
	createProject(t, first, "good", "ws-1111")

	// Three kinds of unusable file: not JSON, valid JSON with a mismatching id,
	// and a state Validate rejects. None may stop the agent from starting.
	if err := os.WriteFile(filepath.Join(root, "mp-aaaaaaaaaaaaaaaa.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mp-bbbbbbbbbbbbbbbb.json"), []byte(`{"id":"mp-cccccccccccccccc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mp-dddddddddddddddd.json"), []byte(`{"id":"mp-dddddddddddddddd","title":"x","workspace_id":"ws-1111","current":"polish","status":"pending","revision":1,"created_at":"2020-01-01T00:00:00Z","updated_at":"2020-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	second := openStore(t, root, 10)
	projects, err := second.List(context.Background(), Query{WorkspaceID: "ws-1111"})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Title != "good" {
		t.Fatalf("projects = %#v", projects)
	}
}

func TestFilePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	store := openStore(t, root, 10)
	project := createProject(t, store, "pl011", "ws-1111")

	dirInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("dir mode = %o", mode)
	}
	fileInfo, err := os.Stat(filepath.Join(root, project.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %o", mode)
	}
}

func TestCreateRespectsCapacity(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "projects"), 2)
	createProject(t, store, "one", "ws-1111")
	createProject(t, store, "two", "ws-1111")
	_, err := store.Create(context.Background(), Project{Title: "three", WorkspaceID: "ws-1111"})
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v", err)
	}
	if store.Len() != 2 {
		t.Fatalf("len = %d; a refused create must not evict", store.Len())
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	store := openStore(t, root, 10)
	project := createProject(t, store, "pl011", "ws-1111")
	scope := Scope{WorkspaceID: "ws-1111"}

	// A file that vanished behind the store's back still deletes cleanly.
	if err := os.Remove(filepath.Join(root, project.ID+".json")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), project.ID, scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), project.ID, scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestOpenRejectsBadOptions(t *testing.T) {
	valid := StoreOptions{
		Root: filepath.Join(t.TempDir(), "projects"), MaxProjects: 1,
		NewID: sequentialIDs(), Now: func() time.Time { return at(1) }, Logger: testLogger(t),
	}
	tests := map[string]func(*StoreOptions){
		"empty root":     func(o *StoreOptions) { o.Root = " " },
		"relative root":  func(o *StoreOptions) { o.Root = "projects" },
		"zero max":       func(o *StoreOptions) { o.MaxProjects = 0 },
		"no id function": func(o *StoreOptions) { o.NewID = nil },
		"no clock":       func(o *StoreOptions) { o.Now = nil },
		"no logger":      func(o *StoreOptions) { o.Logger = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)
			if _, err := OpenFileProjectStore(opts); err == nil {
				t.Fatal("OpenFileProjectStore() error = nil")
			}
		})
	}
}

// A generator that produces ids the store cannot turn into a file name must fail
// loudly at Create rather than write "../escape.json".
func TestCreateRejectsUnusableGeneratedID(t *testing.T) {
	store, err := OpenFileProjectStore(StoreOptions{
		Root: filepath.Join(t.TempDir(), "projects"), MaxProjects: 10,
		NewID: func() string { return "../escape" },
		Now:   func() time.Time { return at(1) }, Logger: testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), Project{Title: "x", WorkspaceID: "ws-1111"}); err == nil {
		t.Fatal("accepted an unusable generated id")
	}
}
