package memory

import (
	"context"
	"sort"
)

// Search is the request-path entry point. It takes a read lock, copies the
// visible candidates and releases the lock before scoring: scoring is pure
// arithmetic, and holding the lock through it would block every other session's
// /remember for the duration of one retrieval.
func (s *FileStore) Search(ctx context.Context, query Query) ([]Match, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateQuery(query); err != nil {
		return nil, err
	}
	tokens := tokenSet(tokenize(query.Text))
	// No usable term is not an error: an empty prompt overlay is the correct
	// answer to "hi", and returning an error here would fail the whole turn.
	if len(tokens) == 0 {
		return nil, nil
	}
	now := query.Now
	if now.IsZero() {
		now = s.now()
	}

	s.mu.RLock()
	candidates := make([]Memory, 0, len(s.ordered))
	for _, id := range s.ordered {
		item := s.byID[id]
		if visibleTo(item.Scope, query.WorkspaceID, query.UserID) && kindAllowed(item.Kind, query.Kinds) {
			candidates = append(candidates, cloneMemory(item))
		}
	}
	s.mu.RUnlock()

	matches := make([]Match, 0, len(candidates))
	for _, item := range candidates {
		score, terms := s.ranker.Score(tokens, item, now, query.RequireAllTerms)
		if score <= 0 {
			continue
		}
		matches = append(matches, Match{Memory: item, Score: score, Terms: terms})
	}
	sortMatches(matches)
	if len(matches) > query.TopK {
		matches = matches[:query.TopK]
	}
	return matches, nil
}

// sortMatches breaks ties twice on purpose. Equal scores are common once two
// items share the same single matched term, and without the id tie-break the
// prompt prefix would differ between two identical requests, defeating both
// provider-side prompt caching and any attempt to reproduce a bad answer.
func sortMatches(matches []Match) {
	sort.SliceStable(matches, func(i, j int) bool {
		left, right := matches[i], matches[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if !left.Memory.UpdatedAt.Equal(right.Memory.UpdatedAt) {
			return left.Memory.UpdatedAt.After(right.Memory.UpdatedAt)
		}
		return left.Memory.ID < right.Memory.ID
	})
}
