package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

type ExecutionResult struct {
	ModelOutput      string
	PersistentOutput string
}

func (r ExecutionResult) Normalize() (ExecutionResult, error) {
	if r.ModelOutput == "" {
		return ExecutionResult{}, errors.New("tool model output is empty")
	}
	if r.PersistentOutput == "" {
		r.PersistentOutput = r.ModelOutput
	}
	return r, nil
}

// SameOutput is the projection-free case: what the model sees is exactly what
// the session stores. Every tool that has no transient payload uses it, so the
// dual-output contract costs one call instead of two fields.
func SameOutput(value string) ExecutionResult {
	return ExecutionResult{ModelOutput: value, PersistentOutput: value}
}

// DecodeArgs decodes a tool call payload strictly: unknown fields and trailing
// JSON are rejected. A silently ignored field is worse than an error here,
// because the model would believe an argument took effect.
func DecodeArgs[T any](raw string) (T, error) {
	var value T
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return value, errors.New("decode arguments: extra JSON values")
	} else if !errors.Is(err, io.EOF) {
		return value, fmt.Errorf("decode trailing arguments: %w", err)
	}
	return value, nil
}

type Tool interface {
	Name() string
	Description() string
	Spec() schema.Spec
	Dangerous() bool
	Execute(ctx context.Context, args string) (ExecutionResult, error)
}
