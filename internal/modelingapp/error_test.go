package modelingapp

import (
	"errors"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

func TestMapInternalErrorUsesTypedPipelineCategory(t *testing.T) {
	cause := errors.New("raw provider response and /absolute/path")
	mapped := mapInternalError(pipelineapi.NewPortError(pipelineapi.PortCompletion, "complete", cause))
	var apiErr *modelingapi.Error
	if !errors.As(mapped, &apiErr) {
		t.Fatalf("mapped error type = %T", mapped)
	}
	if apiErr.Public.Code != modelingapi.ErrorUnavailable || !apiErr.Public.Retryable {
		t.Fatalf("public error = %#v", apiErr.Public)
	}
	if !errors.Is(mapped, cause) {
		t.Fatal("mapped error lost its internal cause")
	}
	if mapped.Error() == cause.Error() {
		t.Fatal("mapped error exposed its internal cause")
	}
}

func TestMapInternalErrorPreservesWrappedModelingAPIError(t *testing.T) {
	original := &modelingapi.Error{
		Public: modelingapi.NewPublicError(modelingapi.ErrorConflict, "The project changed.", true, nil),
		Cause:  errors.New("revision details"),
	}
	wrapped := errors.Join(errors.New("operation failed"), original)
	if mapped := mapInternalError(wrapped); mapped != wrapped {
		t.Fatal("mapInternalError replaced an existing modeling API error chain")
	}
}
