package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// normalizeMemory is the single gate every stored item passes through, on
// create, on update and on load from disk. Keywords and Fingerprint are always
// recomputed here and never trusted from the caller or from the file: a
// hand-edited keyword list would silently change retrieval, and a stale
// fingerprint would break deduplication.
func normalizeMemory(item Memory, sanitizer Sanitizer, maxBytes int, now time.Time) (Memory, error) {
	if _, err := ParseKind(string(item.Kind)); err != nil {
		return Memory{}, err
	}
	if err := validateScope(item.Scope); err != nil {
		return Memory{}, err
	}
	if sanitizer == nil {
		return Memory{}, errors.New("memory sanitizer is nil")
	}
	content, err := sanitizer.Sanitize(item.Content)
	if err != nil {
		return Memory{}, err
	}
	if maxBytes > 0 && len(content) > maxBytes {
		return Memory{}, fmt.Errorf("memory content exceeds %d bytes", maxBytes)
	}
	item.Content = content
	item.Source = normalizeText(item.Source)
	if item.Source == "" {
		item.Source = "unspecified"
	}
	item.Keywords = ExtractKeywords(content)
	if len(item.Keywords) == 0 {
		return Memory{}, errors.New("memory content has no indexable terms")
	}
	item.Fingerprint = Fingerprint(item.Scope, item.Kind, content)
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	return item, nil
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.WorkspaceID) == "" {
		return errors.New("memory workspace id is empty")
	}
	if _, err := ParseVisibility(string(scope.Visibility)); err != nil {
		return err
	}
	// A private item without an owner would be visible to nobody and deletable
	// by nobody, so it is rejected instead of written.
	if scope.Visibility == VisibilityPrivate && strings.TrimSpace(scope.UserID) == "" {
		return errors.New("private memory requires a user id")
	}
	return nil
}

func validateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: invalid id", ErrNotFound)
	}
	return nil
}

func validateQuery(query Query) error {
	if strings.TrimSpace(query.WorkspaceID) == "" {
		return errors.New("memory query workspace id is empty")
	}
	if query.TopK <= 0 {
		return errors.New("memory query top k must be > 0")
	}
	for _, kind := range query.Kinds {
		if _, err := ParseKind(string(kind)); err != nil {
			return err
		}
	}
	return nil
}
