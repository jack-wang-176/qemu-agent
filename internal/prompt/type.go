// Package prompt turns request-scoped knowledge into exactly one extra system
// message. It is deliberately pure: it reads sources, renders text and returns
// messages, and it never touches a Session. The overlay is model input for one
// request, not conversation history — persisting it would replay a stale recall
// forever and let the same memory be counted twice by the budget.
package prompt

import (
	"context"
	"errors"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/memory"
)

// SkillIndexSource asks the registry to render its own catalogue. The
// alternative — handing over []skills.Meta and formatting it here — would put a
// second index renderer in the tree, and the two would drift the first time
// either side added a field.
type SkillIndexSource interface {
	Index(maxBytes int) string
}

type MemorySearcher interface {
	Search(context.Context, memory.Query) ([]memory.Match, error)
}

// ContextQuery is what the assembler needs to decide what is relevant. The
// identity fields are separate from the text because they authorize the read,
// while the text only ranks it.
type ContextQuery struct {
	Text        string
	WorkspaceID string
	UserID      string
	TopK        int
	Now         time.Time
}

// Snapshot is taken once per request, before the first turn. Re-searching every
// turn would let the recalled set change between tool calls, so the model would
// see facts appear and disappear inside one answer.
type Snapshot struct {
	SkillIndex string
	Memories   []memory.Match
}

func (s Snapshot) empty() bool {
	return s.SkillIndex == "" && len(s.Memories) == 0
}

// Input is one turn's assembly request. Persistent is the trimmed history the
// context manager approved; MaxBytes bounds the overlay only, not the history.
type Input struct {
	Persistent []llm.Message
	Snapshot   Snapshot
	MaxBytes   int
}

// Plan is provider input plus the receipt of what was injected. MemoryIDs is how
// the application can count a use after the run without the assembler holding a
// writable store.
type Plan struct {
	Messages  []llm.Message
	MemoryIDs []string
	Bytes     int
}

type Assembler interface {
	Prepare(context.Context, ContextQuery) (Snapshot, error)
	Build(context.Context, Input) (Plan, error)
}

// ErrPromptBudget reports that the overlay does not fit even after degradation.
// It is an error rather than a silent skip: a budget too small to hold the frame
// is a misconfiguration, and hiding it would disable memory invisibly.
var ErrPromptBudget = errors.New("prompt overlay exceeds its byte budget")

// EmptySkillIndex and EmptyMemorySearcher are used when a capability is
// disabled. The assembler dependency stays non-nil either way, so the Agent has
// one code path instead of a nil check before every call.
type EmptySkillIndex struct{}

func (EmptySkillIndex) Index(int) string { return "" }

type EmptyMemorySearcher struct{}

func (EmptyMemorySearcher) Search(context.Context, memory.Query) ([]memory.Match, error) {
	return nil, nil
}

// NopAssembler passes the history through untouched. It is what the Agent gets
// when both skills and memory are off, so "no knowledge layer" is a dependency
// choice at build time rather than a branch inside the loop.
type NopAssembler struct{}

func (NopAssembler) Prepare(context.Context, ContextQuery) (Snapshot, error) {
	return Snapshot{}, nil
}

func (NopAssembler) Build(_ context.Context, input Input) (Plan, error) {
	return Plan{Messages: append([]llm.Message(nil), input.Persistent...)}, nil
}
