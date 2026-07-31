package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
)

// KnowledgeComponents is the whole knowledge layer as one value. Every field is
// always non-nil: a disabled capability is wired as its empty implementation, so
// no request-path code ever asks "is memory on?" — that question is answered
// once, here, at startup.
type KnowledgeComponents struct {
	Store      memory.Store
	Candidates CandidateStore
	Extractor  memory.Extractor
	Assembler  prompt.Assembler
	// WorkspaceID is the scope every memory of this install is written under. It
	// is derived from the workspace path, never the path itself: scopes end up in
	// stored files and in prompts, and a path would leak the operator's directory
	// layout to anything that can read either.
	WorkspaceID string
}

// CandidateStore is the review queue as the application needs it: the write side
// for the post-run hook and the read/resolve side for the commands.
type CandidateStore interface {
	Add(ctx context.Context, item memory.Candidate) (memory.Candidate, error)
	ListPending(ctx context.Context, workspaceID, userID string) ([]memory.Candidate, error)
	Get(ctx context.Context, id string, scope memory.Scope) (memory.Candidate, error)
	Resolve(ctx context.Context, id string, scope memory.Scope, status memory.CandidateStatus, memoryID string) (memory.Candidate, error)
}

// KnowledgeInput keeps the seams a test needs to control explicitly. Completer
// is optional: auto-extraction without one is a configuration error rather than
// a silent fallback to "never propose anything".
type KnowledgeInput struct {
	Config    config.Config
	Skills    prompt.SkillIndexSource
	Completer memory.Completer
	Logger    *slog.Logger
	NewID     func() string
	Now       func() time.Time
}

func BuildKnowledge(input KnowledgeInput) (KnowledgeComponents, error) {
	if input.Skills == nil {
		return KnowledgeComponents{}, errors.New("knowledge skill index source is nil")
	}
	if input.Logger == nil {
		return KnowledgeComponents{}, errors.New("knowledge logger is nil")
	}
	if input.NewID == nil {
		input.NewID = uuid.NewString
	}
	if input.Now == nil {
		input.Now = time.Now
	}
	workspaceID, err := WorkspaceID(input.Config.Paths.Workspace)
	if err != nil {
		return KnowledgeComponents{}, err
	}
	components := KnowledgeComponents{
		Store:       memory.DisabledStore{},
		Candidates:  memory.DisabledCandidates{},
		Extractor:   memory.NopExtractor{},
		WorkspaceID: workspaceID,
	}
	cfg := input.Config.Memory

	// The searcher handed to the prompt layer is the empty one unless memory is
	// on. That keeps "memory disabled" from meaning "every turn performs one
	// search that fails validation".
	var searcher prompt.MemorySearcher = prompt.EmptyMemorySearcher{}
	if cfg.Enabled {
		sanitizer, err := memory.NewDefaultSanitizer(cfg.MaxItemBytes)
		if err != nil {
			return KnowledgeComponents{}, fmt.Errorf("build memory sanitizer: %w", err)
		}
		store, err := memory.OpenFileStore(memory.Options{
			Root:      cfg.Dir,
			Limits:    memory.Limits{MaxItems: cfg.MaxItems, MaxItemBytes: cfg.MaxItemBytes},
			HalfLife:  cfg.HalfLife,
			Sanitizer: sanitizer,
			NewID:     input.NewID,
			Now:       input.Now,
		})
		if err != nil {
			return KnowledgeComponents{}, fmt.Errorf("open memory store: %w", err)
		}
		components.Store = store
		searcher = store

		if cfg.AutoExtract {
			candidates, err := memory.OpenCandidateStore(memory.CandidateOptions{
				Root:      cfg.Dir,
				MaxItems:  cfg.MaxItems,
				TTL:       cfg.CandidateTTL,
				Sanitizer: sanitizer,
				MaxBytes:  cfg.MaxItemBytes,
				NewID:     input.NewID,
				Now:       input.Now,
			})
			if err != nil {
				return KnowledgeComponents{}, fmt.Errorf("open candidate store: %w", err)
			}
			if input.Completer == nil {
				return KnowledgeComponents{}, errors.New("memory auto-extract is enabled but no extraction model is available")
			}
			extractor, err := memory.NewLLMExtractor(input.Completer, sanitizer, cfg.TopK, cfg.MaxItemBytes)
			if err != nil {
				return KnowledgeComponents{}, fmt.Errorf("build memory extractor: %w", err)
			}
			components.Candidates = candidates
			components.Extractor = extractor
		}
	}

	assembler, err := prompt.NewDefaultAssembler(prompt.Dependencies{
		Skills:   input.Skills,
		Memories: searcher,
		Logger:   input.Logger,
	}, prompt.Config{
		MaxIndexBytes:   input.Config.Skills.MaxIndexBytes,
		MaxMemoryItems:  promptMemoryItems(input.Config),
		MaxOverlayBytes: input.Config.Prompt.MaxInjectedBytes,
		StrictMemory:    cfg.StrictSearch,
	})
	if err != nil {
		return KnowledgeComponents{}, fmt.Errorf("build prompt assembler: %w", err)
	}
	components.Assembler = assembler
	return components, nil
}

// promptMemoryItems is zero when memory is off. The overlay budget then carries
// only the skill index, and Prepare skips retrieval entirely.
func promptMemoryItems(cfg config.Config) int {
	if !cfg.Memory.Enabled {
		return 0
	}
	return cfg.Prompt.MaxMemoryItems
}

// WorkspaceID derives a stable short id from a workspace path. Two runs in the
// same directory must agree, or yesterday's memories become invisible; and the
// id must not be reversible into a path, because it is stored and rendered.
func WorkspaceID(workspace string) (string, error) {
	clean := strings.TrimSpace(workspace)
	if clean == "" {
		return "", errors.New("workspace path is empty")
	}
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("workspace path %q must be absolute", clean)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(clean)))
	return "ws-" + hex.EncodeToString(sum[:8]), nil
}
