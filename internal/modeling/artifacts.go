package modeling

// artifacts.go is the only legal way to put modeling bytes on disk, which is why
// no stage implementation in this package calls os.WriteFile.
//
// Writing is two-phase. Stage() writes into a per-batch staging directory and
// computes each digest; Commit() renames the files into their content-addressed
// final place and appends one manifest line per artifact. A crash or a rejected
// artifact therefore never leaves half a stage's output in the committed tree —
// the invariant the Pipeline relies on when it decides whether a stage may be
// re-run.
//
// Layout:
//
//	<root>/<projectID>/staging/<batchID>/<stage>/<name>   before Commit
//	<root>/<projectID>/<stage>/<artifactID>-<name>        after Commit, never overwritten
//	<root>/<projectID>/manifest.jsonl                     append-only audit record

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Draft is one artifact a stage wants to persist. Name is a bare file name; the
// store rejects anything that could be read as a path.
type Draft struct {
	Stage Stage
	Name  string
	Kind  Kind
	Body  []byte
}

// Batch is the handle returned by Stage. Dir is the staging directory, which
// exists until Commit or Discard is called.
type Batch struct {
	ProjectID string
	ID        string
	Dir       string
	Refs      []ArtifactRef
}

// ArtifactStore is the seam the Pipeline and the stages depend on.
type ArtifactStore interface {
	Stage(ctx context.Context, projectID string, drafts []Draft) (Batch, error)
	Commit(ctx context.Context, batch Batch) ([]ArtifactRef, error)
	Discard(ctx context.Context, batch Batch) error
	Open(ctx context.Context, projectID string, ref ArtifactRef) (io.ReadCloser, error)
}

var (
	ErrUnsafeName   = errors.New("unsafe artifact name")
	ErrTooLarge     = errors.New("artifact exceeds the configured limit")
	ErrTampered     = errors.New("artifact content does not match its digest")
	ErrIDCollision  = errors.New("artifact id already stores different content")
	ErrEmptyArtifac = errors.New("artifact batch is empty")
)

// ArtifactOptions carries every dependency of the store. NewBatchID and Now are
// injected so tests are deterministic.
type ArtifactOptions struct {
	Root             string // <ModelingDir>/artifacts
	MaxArtifactBytes int64
	MaxProjectBytes  int64
	NewBatchID       func() string
	Now              func() time.Time
}

// FileArtifactStore implements ArtifactStore on the local filesystem. It holds no
// index: artifacts are addressed by content, so the directory itself is the
// index and two processes cannot disagree about it.
type FileArtifactStore struct {
	root             string
	maxArtifactBytes int64
	maxProjectBytes  int64
	newBatchID       func() string
	now              func() time.Time
}

var _ ArtifactStore = (*FileArtifactStore)(nil)

// NewBatchID is the default batch id source: random, because a batch id only has
// to be unique, and a timestamp would collide when two stages start in the same
// millisecond.
func NewBatchID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// A failing CSPRNG is not recoverable here, but it must not panic in a
		// long-running agent either; fall back to a value that is still unique
		// within the process because the nanosecond clock is monotonic enough.
		return fmt.Sprintf("b%016x", time.Now().UnixNano())
	}
	return "b" + hex.EncodeToString(raw[:])
}

// NewProjectID generates an id in the one form FileProjectStore accepts.
//
// It exists because the store validates ids against `^mp-[0-9a-f]{16}$` and a
// general-purpose UUID does not match: the id is also the state file's name, and
// constraining its alphabet is what lets the store treat a directory listing as
// an index without worrying about what a caller's generator produced. The wiring
// layer must therefore pass this, not uuid.NewString.
//
// The value is deliberately opaque. A project id ends up in command replies and
// log lines, so it carries no device name and no path — those would leak which
// unreleased hardware somebody is modelling.
func NewProjectID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Same reasoning as NewBatchID: a failing CSPRNG must not panic a
		// long-running agent, and the nanosecond clock is unique enough within one
		// process to keep two projects from colliding.
		return fmt.Sprintf("mp-%016x", time.Now().UnixNano())
	}
	return "mp-" + hex.EncodeToString(raw[:])
}

func OpenFileArtifactStore(opts ArtifactOptions) (*FileArtifactStore, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("modeling artifact root is empty")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("modeling artifact root %q must be absolute", root)
	}
	if opts.MaxArtifactBytes <= 0 || opts.MaxProjectBytes <= 0 {
		return nil, errors.New("modeling artifact limits must be > 0")
	}
	if opts.MaxArtifactBytes > opts.MaxProjectBytes {
		return nil, errors.New("modeling artifact limit must be <= project limit")
	}
	if opts.NewBatchID == nil {
		return nil, errors.New("modeling batch id generator is nil")
	}
	if opts.Now == nil {
		return nil, errors.New("modeling clock is nil")
	}
	if err := os.MkdirAll(filepath.Clean(root), 0o700); err != nil {
		return nil, fmt.Errorf("create modeling artifact dir: %w", err)
	}
	return &FileArtifactStore{
		root:             filepath.Clean(root),
		maxArtifactBytes: opts.MaxArtifactBytes,
		maxProjectBytes:  opts.MaxProjectBytes,
		newBatchID:       opts.NewBatchID,
		now:              opts.Now,
	}, nil
}

// Stage validates every draft, then writes them into a fresh staging directory.
//
// The order matters: all names, stages, kinds and limits are checked *before any
// byte is written*, so a rejected batch leaves nothing behind — the same
// sanitize-before-persist rule the memory store follows.
func (s *FileArtifactStore) Stage(ctx context.Context, projectID string, drafts []Draft) (Batch, error) {
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	if err := ValidateProjectID(projectID); err != nil {
		return Batch{}, err
	}
	if len(drafts) == 0 {
		return Batch{}, ErrEmptyArtifac
	}

	// Phase 1: validate the whole batch and compute the digests.
	seen := make(map[string]struct{}, len(drafts))
	var batchBytes int64
	refs := make([]ArtifactRef, 0, len(drafts))
	now := s.now()
	for _, draft := range drafts {
		if err := validateName(draft.Name); err != nil {
			return Batch{}, fmt.Errorf("%w: %s", ErrUnsafeName, draft.Name)
		}
		// filepath.Base must be a no-op, or the name carried a path the regexp
		// did not catch on this platform.
		if filepath.Base(draft.Name) != draft.Name {
			return Batch{}, fmt.Errorf("%w: %s", ErrUnsafeName, draft.Name)
		}
		if stageIndex(draft.Stage) < 0 {
			return Batch{}, fmt.Errorf("artifact %q has unknown stage %q", draft.Name, draft.Stage)
		}
		if _, err := ParseKind(string(draft.Kind)); err != nil {
			return Batch{}, err
		}
		key := string(draft.Stage) + "/" + draft.Name
		if _, exists := seen[key]; exists {
			return Batch{}, fmt.Errorf("artifact %q is staged twice", key)
		}
		seen[key] = struct{}{}
		size := int64(len(draft.Body))
		if size == 0 {
			return Batch{}, fmt.Errorf("artifact %q is empty", draft.Name)
		}
		if size > s.maxArtifactBytes {
			return Batch{}, fmt.Errorf("%w: %s is %d bytes, limit is %d", ErrTooLarge, draft.Name, size, s.maxArtifactBytes)
		}
		batchBytes += size
		sum := sha256.Sum256(draft.Body)
		digest := hex.EncodeToString(sum[:])
		refs = append(refs, ArtifactRef{
			ID: digest[:16], Stage: draft.Stage, Name: draft.Name, Kind: draft.Kind,
			Bytes: size, Digest: digest, Created: now,
		})
	}
	// The project budget counts what is already on disk plus this batch, so the
	// limit cannot be exceeded by many small stages.
	used, err := s.projectBytes(projectID)
	if err != nil {
		return Batch{}, err
	}
	if used+batchBytes > s.maxProjectBytes {
		return Batch{}, fmt.Errorf("%w: project would hold %d bytes, limit is %d", ErrTooLarge, used+batchBytes, s.maxProjectBytes)
	}

	// Phase 2: write. Nothing here can reject the batch any more.
	batchID := strings.TrimSpace(s.newBatchID())
	if err := validateName(batchID); err != nil {
		return Batch{}, errors.New("modeling batch id generator produced an unusable id")
	}
	dir := filepath.Join(s.root, projectID, "staging", batchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Batch{}, fmt.Errorf("create staging dir: %w", err)
	}
	for index, draft := range drafts {
		stageDir := filepath.Join(dir, string(draft.Stage))
		if err := os.MkdirAll(stageDir, 0o700); err != nil {
			return Batch{}, fmt.Errorf("create staging stage dir: %w", err)
		}
		if err := atomicWriteFile(filepath.Join(stageDir, draft.Name), draft.Body, 0o600); err != nil {
			return Batch{}, fmt.Errorf("stage artifact %q: %w", refs[index].Name, err)
		}
	}
	return Batch{ProjectID: projectID, ID: batchID, Dir: dir, Refs: refs}, nil
}

// Commit moves a staged batch into its final, content-addressed place and
// appends the manifest. It is idempotent for identical content: a re-run that
// produces the same bytes finds the file already there and reports success.
// Different content under the same id is an id collision and an error — the
// store never overwrites a committed artifact.
func (s *FileArtifactStore) Commit(ctx context.Context, batch Batch) ([]ArtifactRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkBatch(batch); err != nil {
		return nil, err
	}

	committed := make([]ArtifactRef, 0, len(batch.Refs))
	for _, ref := range batch.Refs {
		finalDir := filepath.Join(s.root, batch.ProjectID, string(ref.Stage))
		if err := os.MkdirAll(finalDir, 0o700); err != nil {
			return nil, fmt.Errorf("create artifact dir: %w", err)
		}
		target := filepath.Join(finalDir, ref.ID+"-"+ref.Name)
		source := filepath.Join(batch.Dir, string(ref.Stage), ref.Name)

		switch existing, err := os.Stat(target); {
		case err == nil && existing.Mode().IsRegular():
			// Already committed. Verify rather than trust: equal content means an
			// idempotent re-run, different content means this id already addresses
			// something else and committing would destroy it.
			same, err := s.sameContent(target, ref.Digest)
			if err != nil {
				return nil, err
			}
			if !same {
				return nil, fmt.Errorf("%w: %s", ErrIDCollision, ref.ID)
			}
		case err == nil:
			return nil, fmt.Errorf("artifact path %q is not a regular file", target)
		case errors.Is(err, os.ErrNotExist):
			// Re-check the staged bytes against the ref before they become
			// permanent: Open promises that a committed artifact matches its
			// digest, and that promise has to start here rather than depend on the
			// caller having handed back the Batch it was given.
			same, err := s.sameContent(source, ref.Digest)
			if err != nil {
				return nil, err
			}
			if !same {
				return nil, fmt.Errorf("%w: %s", ErrTampered, ref.ID)
			}
			if err := os.Rename(source, target); err != nil {
				return nil, fmt.Errorf("commit artifact %q: %w", ref.Name, err)
			}
		default:
			return nil, fmt.Errorf("stat artifact %q: %w", ref.Name, err)
		}
		committed = append(committed, ref)
	}

	// The manifest is written only after every rename succeeded, so a manifest
	// line always describes a file that exists.
	if err := s.appendManifest(batch, committed); err != nil {
		return nil, err
	}
	// Staging is cleaned last; a leftover directory is harmless, a missing
	// committed file is not.
	if err := os.RemoveAll(batch.Dir); err != nil {
		return nil, fmt.Errorf("clean staging dir: %w", err)
	}
	return committed, nil
}

// Discard throws a staged batch away. It is what the Pipeline calls when a stage
// fails after staging, so the next attempt starts from an empty staging area.
func (s *FileArtifactStore) Discard(ctx context.Context, batch Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkBatch(batch); err != nil {
		return err
	}
	if err := os.RemoveAll(batch.Dir); err != nil {
		return fmt.Errorf("discard staging dir: %w", err)
	}
	return nil
}

// Open returns the bytes of a committed artifact after re-checking the digest.
// The check is the point: a hand-edited build log must not be readable as
// evidence that the device passed its tests. Artifacts are bounded by
// MaxArtifactBytes, so reading fully before handing anything out is affordable
// and lets the caller never see unverified bytes.
func (s *FileArtifactStore) Open(ctx context.Context, projectID string, ref ArtifactRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, projectID, string(ref.Stage), ref.ID+"-"+ref.Name)
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, ref.ID)
		}
		return nil, fmt.Errorf("read artifact %q: %w", ref.Name, err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != ref.Digest {
		return nil, fmt.Errorf("%w: %s", ErrTampered, ref.ID)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// checkBatch rejects a handle that was not produced by this store: the staging
// directory must sit under the project's own staging root, so a forged Batch
// cannot make Commit rename files from anywhere on disk.
func (s *FileArtifactStore) checkBatch(batch Batch) error {
	if err := ValidateProjectID(batch.ProjectID); err != nil {
		return err
	}
	if len(batch.Refs) == 0 {
		return ErrEmptyArtifac
	}
	expected := filepath.Join(s.root, batch.ProjectID, "staging", batch.ID)
	if filepath.Clean(batch.Dir) != expected {
		return fmt.Errorf("%w: staging dir %q", ErrUnsafeName, batch.Dir)
	}
	for _, ref := range batch.Refs {
		if err := validateRef(ref); err != nil {
			return err
		}
	}
	return nil
}

// sameContent reports whether an already committed file has the expected digest.
func (s *FileArtifactStore) sameContent(path, digest string) (bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read committed artifact: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) == digest, nil
}

// manifestEntry is one append-only audit line. It records the batch so a
// reviewer can tell which artifacts were produced by the same stage run.
type manifestEntry struct {
	Batch    string      `json:"batch"`
	At       time.Time   `json:"at"`
	Artifact ArtifactRef `json:"artifact"`
}

func (s *FileArtifactStore) appendManifest(batch Batch, refs []ArtifactRef) error {
	path := filepath.Join(s.root, batch.ProjectID, "manifest.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open modeling manifest: %w", err)
	}
	defer file.Close()
	now := s.now()
	var buffer bytes.Buffer
	for _, ref := range refs {
		line, err := json.Marshal(manifestEntry{Batch: batch.ID, At: now, Artifact: ref})
		if err != nil {
			return fmt.Errorf("encode manifest line: %w", err)
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	if _, err := file.Write(buffer.Bytes()); err != nil {
		return fmt.Errorf("append modeling manifest: %w", err)
	}
	return file.Sync()
}

// projectBytes sums the artifact bytes the project already occupies, staging
// included. Counting staging is deliberate: a stage that repeatedly stages and
// never commits must still hit the budget instead of filling the disk. The
// manifest is excluded — it is the audit record, and a full project must not be
// able to stop its own history from being written.
func (s *FileArtifactStore) projectBytes(projectID string) (int64, error) {
	root := filepath.Join(s.root, projectID)
	manifest := filepath.Join(root, "manifest.jsonl")
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() || path == manifest {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("measure modeling project usage: %w", err)
	}
	return total, nil
}
