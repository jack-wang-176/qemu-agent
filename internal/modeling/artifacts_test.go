package modeling

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testProjectID = "mp-0123456789abcdef"

// batchIDs hands out deterministic batch ids so a test can name the staging dir.
func batchIDs() func() string {
	counter := 0
	return func() string {
		counter++
		return "b" + strings.Repeat("0", 15) + string(rune('0'+counter))
	}
}

func openArtifacts(t *testing.T, root string, maxArtifact, maxProject int64) *FileArtifactStore {
	t.Helper()
	store, err := OpenFileArtifactStore(ArtifactOptions{
		Root: root, MaxArtifactBytes: maxArtifact, MaxProjectBytes: maxProject,
		NewBatchID: batchIDs(), Now: func() time.Time { return at(500) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func draft(stage Stage, name string, kind Kind, body string) Draft {
	return Draft{Stage: stage, Name: name, Kind: kind, Body: []byte(body)}
}

func TestStageThenCommitIsAtomic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 1024, 8192)

	batch, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageExtract, "reg-ir.json", KindRegIR, `{"registers":[]}`),
		draft(StageExtract, "notes.md", KindPlan, "extracted from table 3-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Before Commit nothing is visible in the committed tree.
	if _, err := os.Stat(filepath.Join(root, testProjectID, string(StageExtract))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging leaked into the committed tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, testProjectID, "manifest.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest written before commit: %v", err)
	}

	refs, err := store.Commit(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %#v", refs)
	}
	// Staging is gone and every ref reads back with its digest intact.
	if _, err := os.Stat(batch.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived the commit: %v", err)
	}
	for _, ref := range refs {
		body, err := store.Open(context.Background(), testProjectID, ref)
		if err != nil {
			t.Fatalf("open %q: %v", ref.Name, err)
		}
		data, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		_ = body.Close()
		if int64(len(data)) != ref.Bytes {
			t.Fatalf("artifact %q is %d bytes, ref says %d", ref.Name, len(data), ref.Bytes)
		}
	}
	// The committed name carries the content address, so two versions of the same
	// file name can coexist.
	entries, err := os.ReadDir(filepath.Join(root, testProjectID, string(StageExtract)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.Contains(entry.Name(), "-") {
			t.Fatalf("committed name %q is not content-addressed", entry.Name())
		}
	}
}

func TestDiscardLeavesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 1024, 8192)

	batch, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StagePlan, "plan.md", KindPlan, "step 1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Discard(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(batch.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discard left the staging dir behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, testProjectID, string(StagePlan))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discard committed something: %v", err)
	}
	// A second discard is harmless: the Pipeline may unwind twice on a failure.
	if err := store.Discard(context.Background(), batch); err != nil {
		t.Fatalf("second discard = %v", err)
	}
	// A forged handle pointing outside the project's staging root is refused
	// before anything is removed.
	forged := batch
	forged.Dir = filepath.Join(root, "elsewhere")
	if err := store.Discard(context.Background(), forged); !errors.Is(err, ErrUnsafeName) {
		t.Fatalf("forged batch = %v", err)
	}
}

func TestCommitIsIdempotentForSameDigest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 1024, 8192)
	body := `{"registers":[{"name":"DR"}]}`

	first, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageExtract, "reg-ir.json", KindRegIR, body),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// A re-run producing identical bytes must succeed rather than fail on the
	// existing file; that is how a retried stage stays usable.
	second, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageExtract, "reg-ir.json", KindRegIR, body),
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.Commit(context.Background(), second)
	if err != nil {
		t.Fatalf("idempotent re-commit = %v", err)
	}
	if refs[0].ID != first.Refs[0].ID {
		t.Fatalf("same content produced different ids: %q vs %q", refs[0].ID, first.Refs[0].ID)
	}

	// Different content under the same id is a collision. Two bodies whose
	// sha256 share 16 hex digits cannot be constructed on purpose, so the state
	// is simulated: the committed file holds something else than its id promises.
	// Commit must refuse instead of replacing it, because that file is the audit
	// record of what the agent once produced.
	committed := filepath.Join(root, testProjectID, string(StageExtract), first.Refs[0].ID+"-reg-ir.json")
	if err := os.WriteFile(committed, []byte("someone else's artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageExtract, "reg-ir.json", KindRegIR, body),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(context.Background(), third); !errors.Is(err, ErrIDCollision) {
		t.Fatalf("collision = %v", err)
	}
	stored, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "someone else's artifact" {
		t.Fatalf("a collision overwrote the committed artifact: %q", stored)
	}

	// A batch whose refs no longer describe the staged bytes is refused too: the
	// digest Open re-checks is fixed at commit time, not taken on trust.
	forged, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageInfer, "reg-ir.json", KindRegIR, "staged bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	forged.Refs[0].ID, forged.Refs[0].Digest = first.Refs[0].ID, first.Refs[0].Digest
	if _, err := store.Commit(context.Background(), forged); !errors.Is(err, ErrTampered) {
		t.Fatalf("forged ref = %v", err)
	}
}

func TestRejectsUnsafeName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 1024, 8192)

	for _, name := range []string{"../escape.md", "a/b.md", "..", "", ".hidden", "with space.md", `back\slash.md`} {
		_, err := store.Stage(context.Background(), testProjectID, []Draft{draft(StagePlan, name, KindPlan, "x")})
		if !errors.Is(err, ErrUnsafeName) {
			t.Fatalf("Stage(%q) = %v", name, err)
		}
		// The rejection must not echo the body, and nothing may be on disk.
		if strings.Contains(err.Error(), "x") && name == "" {
			t.Fatalf("error echoed the artifact body: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, testProjectID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected batch created a project dir: %v", err)
	}
	// An unknown stage or kind is refused for the same reason: both become path
	// elements or are rendered later.
	if _, err := store.Stage(context.Background(), testProjectID, []Draft{draft("polish", "plan.md", KindPlan, "x")}); err == nil {
		t.Fatal("accepted an unknown stage")
	}
	if _, err := store.Stage(context.Background(), testProjectID, []Draft{draft(StagePlan, "plan.md", "sketch", "x")}); err == nil {
		t.Fatal("accepted an unknown kind")
	}
	// A bad project id is not distinguishable from an unknown one.
	if _, err := store.Stage(context.Background(), "../etc", []Draft{draft(StagePlan, "plan.md", KindPlan, "x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad project id = %v", err)
	}
}

func TestRejectsOversizeArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 16, 8192)

	_, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageEmit, "device.c", KindCode, strings.Repeat("a", 17)),
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// The whole batch is refused, including the artifact that would have fit.
	_, err = store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageEmit, "small.c", KindCode, "ok"),
		draft(StageEmit, "device.c", KindCode, strings.Repeat("a", 17)),
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, testProjectID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an oversize batch wrote something: %v", err)
	}
}

func TestRejectsProjectBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 64, 100)

	first, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StagePlan, "plan.md", KindPlan, strings.Repeat("a", 60)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// Each artifact is under the per-artifact limit, but the project budget counts
	// what is already committed, so many small stages cannot fill the disk.
	_, err = store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageExtract, "reg-ir.json", KindRegIR, strings.Repeat("b", 60)),
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// Staging counts too: a stage that never commits still hits the budget.
	pending, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageExtract, "reg-ir.json", KindRegIR, strings.Repeat("c", 30)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageInfer, "reg-ir.json", KindRegIR, strings.Repeat("d", 30)),
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("uncommitted staging did not count: %v", err)
	}
	if err := store.Discard(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDetectsTamperedBody(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 1024, 8192)

	batch, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StageVerify, "build.log", KindEvidence, "make: ok\nqtest: 12 passed\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.Commit(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	ref := refs[0]
	path := filepath.Join(root, testProjectID, string(StageVerify), ref.ID+"-"+ref.Name)
	if err := os.WriteFile(path, []byte("make: ok\nqtest: 99 passed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A hand-edited build log must not be readable as evidence that the device
	// passed its tests.
	if _, err := store.Open(context.Background(), testProjectID, ref); !errors.Is(err, ErrTampered) {
		t.Fatalf("err = %v", err)
	}
	// A ref of another project resolves to nothing rather than to that project's
	// bytes.
	if _, err := store.Open(context.Background(), "mp-ffffffffffffffff", ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project open = %v", err)
	}
}

func TestManifestAppendedOncePerCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 1024, 8192)

	for _, body := range []string{"first", "second"} {
		batch, err := store.Stage(context.Background(), testProjectID, []Draft{
			draft(StagePlan, "plan.md", KindPlan, body),
			draft(StagePlan, "notes.md", KindPlan, body+"-notes"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Commit(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, testProjectID, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("manifest has %d lines:\n%s", len(lines), data)
	}
	// The manifest is append-only: the first commit's lines are still the first
	// lines, so it stays an audit record instead of a snapshot.
	if !strings.Contains(lines[0], `"batch":"b0000000000000001"`) {
		t.Fatalf("first line = %s", lines[0])
	}
	if !strings.Contains(lines[3], `"batch":"b0000000000000002"`) {
		t.Fatalf("last line = %s", lines[3])
	}
	// A manifest line quotes no artifact body, only its metadata.
	if strings.Contains(string(data), "first-notes") || strings.Contains(string(data), "second") {
		t.Fatalf("manifest quoted artifact content:\n%s", data)
	}
}

func TestArtifactPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := openArtifacts(t, root, 1024, 8192)

	batch, err := store.Stage(context.Background(), testProjectID, []Draft{
		draft(StagePlan, "plan.md", KindPlan, "step 1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.Commit(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		root: 0o700,
		filepath.Join(root, testProjectID, string(StagePlan)):                        0o700,
		filepath.Join(root, testProjectID, string(StagePlan), refs[0].ID+"-plan.md"): 0o600,
		filepath.Join(root, testProjectID, "manifest.jsonl"):                         0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != want {
			t.Fatalf("%s mode = %o, want %o", path, mode, want)
		}
	}
}

func TestOpenArtifactStoreRejectsBadOptions(t *testing.T) {
	valid := ArtifactOptions{
		Root: filepath.Join(t.TempDir(), "artifacts"), MaxArtifactBytes: 16, MaxProjectBytes: 32,
		NewBatchID: NewBatchID, Now: func() time.Time { return at(1) },
	}
	tests := map[string]func(*ArtifactOptions){
		"empty root":          func(o *ArtifactOptions) { o.Root = " " },
		"relative root":       func(o *ArtifactOptions) { o.Root = "artifacts" },
		"zero artifact limit": func(o *ArtifactOptions) { o.MaxArtifactBytes = 0 },
		"zero project limit":  func(o *ArtifactOptions) { o.MaxProjectBytes = 0 },
		"artifact over project": func(o *ArtifactOptions) {
			o.MaxArtifactBytes, o.MaxProjectBytes = 64, 32
		},
		"no batch ids": func(o *ArtifactOptions) { o.NewBatchID = nil },
		"no clock":     func(o *ArtifactOptions) { o.Now = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)
			if _, err := OpenFileArtifactStore(opts); err == nil {
				t.Fatal("OpenFileArtifactStore() error = nil")
			}
		})
	}
}
