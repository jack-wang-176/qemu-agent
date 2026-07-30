package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrDisabled is returned by every operation of the disabled implementations.
// A disabled knowledge layer is wired as these types rather than as nil fields:
// the request path then needs no nil checks, and a management command can turn
// one sentinel into one recoverable "memory is disabled" message instead of each
// call site inventing its own.
var ErrDisabled = errors.New("memory is disabled")

// DisabledStore satisfies Store while storing nothing. Reads answer empty, which
// is what lets Search stay on the request path with memory turned off, and every
// write refuses, so a user is told the feature is off instead of silently
// watching their fact disappear.
type DisabledStore struct{}

var _ Store = DisabledStore{}

func (DisabledStore) Save(_ context.Context, _ Memory) (Memory, error) {
	return Memory{}, ErrDisabled
}

func (DisabledStore) Get(_ context.Context, id string, _ Scope) (Memory, error) {
	return Memory{}, fmt.Errorf("%w: %s", ErrDisabled, id)
}

func (DisabledStore) List(_ context.Context, _ Query) ([]Memory, error) { return nil, nil }

func (DisabledStore) Delete(_ context.Context, id string, _ Scope) error {
	return fmt.Errorf("%w: %s", ErrDisabled, id)
}

func (DisabledStore) Search(_ context.Context, _ Query) ([]Match, error) { return nil, nil }

func (DisabledStore) Touch(_ context.Context, _ []string, _ time.Time) error { return nil }

// DisabledCandidates is the same idea for the review queue: nothing is pending,
// and resolving anything is an error rather than a silent no-op.
type DisabledCandidates struct{}

func (DisabledCandidates) Add(_ context.Context, _ Candidate) (Candidate, error) {
	return Candidate{}, ErrDisabled
}

func (DisabledCandidates) ListPending(_ context.Context, _, _ string) ([]Candidate, error) {
	return nil, nil
}

func (DisabledCandidates) Get(_ context.Context, id string, _ Scope) (Candidate, error) {
	return Candidate{}, fmt.Errorf("%w: %s", ErrDisabled, id)
}

func (DisabledCandidates) Resolve(_ context.Context, id string, _ Scope, _ CandidateStatus, _ string) (Candidate, error) {
	return Candidate{}, fmt.Errorf("%w: %s", ErrDisabled, id)
}
