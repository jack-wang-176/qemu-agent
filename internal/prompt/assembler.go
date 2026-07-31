package prompt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
)

type Dependencies struct {
	Skills   SkillIndexSource
	Memories MemorySearcher
	Logger   *slog.Logger
}

type Config struct {
	MaxIndexBytes   int
	MaxMemoryItems  int
	MaxOverlayBytes int
	StrictMemory    bool
}

type DefaultAssembler struct {
	skills       SkillIndexSource
	memories     MemorySearcher
	logger       *slog.Logger
	maxIndex     int
	maxMemory    int
	maxOverlay   int
	strictMemory bool
}

var _ Assembler = (*DefaultAssembler)(nil)

func NewDefaultAssembler(deps Dependencies, cfg Config) (*DefaultAssembler, error) {
	if deps.Skills == nil {
		return nil, errors.New("prompt skill source is nil")
	}
	if deps.Memories == nil {
		return nil, errors.New("prompt memory searcher is nil")
	}
	if deps.Logger == nil {
		return nil, errors.New("prompt logger is nil")
	}
	if cfg.MaxOverlayBytes <= 0 {
		return nil, errors.New("prompt overlay budget must be > 0")
	}
	if cfg.MaxIndexBytes < 0 || cfg.MaxMemoryItems < 0 {
		return nil, errors.New("prompt limits must be >= 0")
	}
	return &DefaultAssembler{
		skills: deps.Skills, memories: deps.Memories, logger: deps.Logger,
		maxIndex: cfg.MaxIndexBytes, maxMemory: cfg.MaxMemoryItems,
		maxOverlay: cfg.MaxOverlayBytes, strictMemory: cfg.StrictMemory,
	}, nil
}

// Prepare runs once per request. Retrieval failure is not fatal by default: the
// agent can still answer without recall, and a broken memory directory should
// degrade the answer rather than refuse the request. StrictMemory inverts that
// for installs where a missing constraint is worse than no answer.
func (a *DefaultAssembler) Prepare(ctx context.Context, query ContextQuery) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{SkillIndex: a.skills.Index(a.maxIndex)}
	topK := query.TopK
	if a.maxMemory < topK {
		topK = a.maxMemory
	}
	// No memory budget means no search at all, so a disabled memory layer costs
	// zero work instead of one guaranteed validation error per turn.
	if topK <= 0 || strings.TrimSpace(query.Text) == "" || strings.TrimSpace(query.WorkspaceID) == "" {
		return snapshot, nil
	}
	matches, err := a.memories.Search(ctx, memory.Query{
		Text:            query.Text,
		WorkspaceID:     query.WorkspaceID,
		UserID:          query.UserID,
		TopK:            topK,
		RequireAllTerms: a.strictMemory,
		Now:             query.Now,
	})
	if err != nil {
		if a.strictMemory {
			return Snapshot{}, fmt.Errorf("search memory: %w", err)
		}
		// Only the error is logged. The query text and the recalled content are
		// the two things that must never reach a log file.
		a.logger.WarnContext(ctx, "memory search unavailable", "error", err)
		return snapshot, nil
	}
	snapshot.Memories = memory.CloneMatches(matches)
	return snapshot, nil
}

// Build assembles one turn. It copies Persistent before inserting, so the slice
// the caller owns — which is backed by the Session — is never mutated.
func (a *DefaultAssembler) Build(ctx context.Context, input Input) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if len(input.Persistent) == 0 {
		return Plan{}, errors.New("prompt input has no messages")
	}
	budget := input.MaxBytes
	if budget <= 0 {
		budget = a.maxOverlay
	}
	messages := append([]llm.Message(nil), input.Persistent...)
	if input.Snapshot.empty() {
		return Plan{Messages: messages}, nil
	}
	overlay, ids, err := renderOverlay(input.Snapshot, budget)
	if err != nil {
		return Plan{}, err
	}
	if overlay == "" {
		return Plan{Messages: messages}, nil
	}
	return Plan{
		Messages:  insertBeforeLastUser(messages, llm.Message{Role: llm.RoleSystem, Content: overlay}),
		MemoryIDs: ids,
		Bytes:     len(overlay),
	}, nil
}

// insertBeforeLastUser puts the overlay next to the request it was retrieved
// for. Appending it at the end would place a system message after the user turn,
// which some providers reject, and prepending it would bury the recall behind
// the whole history — the position closest to the question is the one the model
// actually attends to.
func insertBeforeLastUser(messages []llm.Message, overlay llm.Message) []llm.Message {
	insert := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == llm.RoleUser {
			insert = index
			break
		}
	}
	if insert < 0 {
		// No user message yet: keep the base system prompt first and put the
		// overlay directly after the leading system block, never before it.
		insert = 0
		for insert < len(messages) && messages[insert].Role == llm.RoleSystem {
			insert++
		}
	}
	result := make([]llm.Message, 0, len(messages)+1)
	result = append(result, messages[:insert]...)
	result = append(result, overlay)
	return append(result, messages[insert:]...)
}
