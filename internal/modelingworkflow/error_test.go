package modelingworkflow

import (
	"errors"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

func TestMapModelingErrorPreservesCauseAndSafePublicFields(t *testing.T) {
	cause := errors.New("raw-provider-token-and-/absolute/path")
	apiErr := &modelingapi.Error{
		Public: modelingapi.NewPublicError(
			modelingapi.ErrorConflict,
			"The project changed.",
			true,
			map[string]string{"expected_revision": "2", "current_revision": "3"},
		),
		Cause: cause,
	}

	mapped := mapModelingError(apiErr)
	if mapped.Kind != ErrorConflict || mapped.Public != "The project changed." || !mapped.Retryable {
		t.Fatalf("mapped error = %#v", mapped)
	}
	if !errors.Is(mapped, cause) {
		t.Fatal("mapped error did not preserve the original cause")
	}
	if mapped.Error() == cause.Error() {
		t.Fatal("mapped error exposed its internal cause")
	}
	mapped.Details["current_revision"] = "changed"
	if apiErr.Public.Details["current_revision"] != "3" {
		t.Fatal("mapped details alias the modeling API error")
	}
}
