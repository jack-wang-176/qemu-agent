// Package memory is the durable knowledge store: short, already-vetted facts
// that outlive a session. It knows nothing about prompts, models or tools —
// internal/prompt decides how a retrieved item is injected, and nothing here
// calls a model except the optional extractor in extract.go.
package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Kind string

const (
	KindFact       Kind = "fact"
	KindPreference Kind = "preference"
	KindDecision   Kind = "decision"
	KindConstraint Kind = "constraint"
)

type Visibility string

const (
	VisibilityPrivate   Visibility = "private"
	VisibilityWorkspace Visibility = "workspace"
)

// Scope is the authorization tuple of one item. Every read, write and delete is
// filtered through it, so a Telegram user cannot reach another user's private
// item even with a guessed id.
type Scope struct {
	WorkspaceID string     `json:"workspace_id"`
	UserID      string     `json:"user_id,omitempty"`
	Visibility  Visibility `json:"visibility"`
}

type Memory struct {
	ID          string    `json:"id"`
	Kind        Kind      `json:"kind"`
	Scope       Scope     `json:"scope"`
	Content     string    `json:"content"`
	Keywords    []string  `json:"keywords"`
	Source      string    `json:"source"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
	UseCount    uint64    `json:"use_count"`
}

// Query is one retrieval request. Now is injected rather than read from the
// clock inside the loop so every candidate of one request decays against the
// same instant and tests are reproducible.
type Query struct {
	Text            string
	WorkspaceID     string
	UserID          string
	Kinds           []Kind
	TopK            int
	RequireAllTerms bool
	Now             time.Time
}

type Match struct {
	Memory Memory
	Score  float64
	Terms  []string
}

type Limits struct {
	MaxItems     int
	MaxItemBytes int
}

var (
	ErrNotFound  = errors.New("memory not found")
	ErrDuplicate = errors.New("memory already exists")
)

// idPattern also guards the file name: the id is the only caller-visible value
// that becomes a path element, so it may not contain separators or dots.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ParseKind(raw string) (Kind, error) {
	switch kind := Kind(strings.ToLower(strings.TrimSpace(raw))); kind {
	case KindFact, KindPreference, KindDecision, KindConstraint:
		return kind, nil
	default:
		return "", fmt.Errorf("invalid memory kind %q; want fact|preference|decision|constraint", raw)
	}
}

func ParseVisibility(raw string) (Visibility, error) {
	switch value := Visibility(strings.ToLower(strings.TrimSpace(raw))); value {
	case VisibilityPrivate, VisibilityWorkspace:
		return value, nil
	default:
		return "", fmt.Errorf("invalid memory scope %q; want private|workspace", raw)
	}
}

// visibleTo is the single implementation of the read rule. An empty workspace or
// user id never matches: a failed workspace derivation must reduce recall to
// zero, not turn every private item into a shared one.
func visibleTo(scope Scope, workspaceID, userID string) bool {
	if scope.WorkspaceID == "" || workspaceID == "" || scope.WorkspaceID != workspaceID {
		return false
	}
	switch scope.Visibility {
	case VisibilityWorkspace:
		return true
	case VisibilityPrivate:
		return scope.UserID != "" && userID != "" && scope.UserID == userID
	default:
		return false
	}
}

// writableBy is the write and delete rule. Workspace items are deliberately
// shared: anyone in the workspace may remove them. Private items require the
// same user, and both require a non-empty workspace.
func writableBy(scope Scope, requested Scope) bool {
	if scope.WorkspaceID == "" || scope.WorkspaceID != requested.WorkspaceID {
		return false
	}
	if scope.Visibility == VisibilityPrivate {
		return scope.UserID != "" && scope.UserID == requested.UserID
	}
	return scope.Visibility == VisibilityWorkspace
}

func kindAllowed(kind Kind, allowed []Kind) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == kind {
			return true
		}
	}
	return false
}

func cloneMemory(item Memory) Memory {
	item.Keywords = append([]string(nil), item.Keywords...)
	return item
}

func cloneMemories(items []Memory) []Memory {
	if items == nil {
		return nil
	}
	result := make([]Memory, 0, len(items))
	for _, item := range items {
		result = append(result, cloneMemory(item))
	}
	return result
}

// CloneMatches lets callers hand results to another goroutine without sharing
// the keyword slices the store handed out.
func CloneMatches(matches []Match) []Match {
	if matches == nil {
		return nil
	}
	result := make([]Match, 0, len(matches))
	for _, match := range matches {
		result = append(result, Match{
			Memory: cloneMemory(match.Memory),
			Score:  match.Score,
			Terms:  append([]string(nil), match.Terms...),
		})
	}
	return result
}
