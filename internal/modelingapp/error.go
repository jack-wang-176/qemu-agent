package modelingapp

import (
	"context"
	"errors"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

var errUnsupported = errors.New("modelingapp: operation is not supported by the current engine")

func unsupported(capability string) error {
	return &modelingapi.Error{Public: modelingapi.NewPublicError(modelingapi.ErrorUnavailable, "This modeling capability is not available.", false, map[string]string{"capability": capability}), Cause: errUnsupported}
}

func mapInternalError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *modelingapi.Error
	if errors.As(err, &apiErr) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &modelingapi.Error{Public: modelingapi.NewPublicError(modelingapi.ErrorCanceled, "The modeling operation was canceled.", true, nil), Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &modelingapi.Error{Public: modelingapi.NewPublicError(modelingapi.ErrorUnavailable, "The modeling operation timed out.", true, nil), Cause: err}
	}
	public := publicErrorForCategory(pipelineapi.ErrorCategory(err))
	return &modelingapi.Error{Public: public, Cause: err}
}

func publicErrorForCategory(category string) modelingapi.PublicError {
	code := modelingapi.ErrorInternal
	message := "The previous modeling operation could not be completed."
	switch category {
	case "not_found":
		code, message = modelingapi.ErrorNotFound, "The modeling project was not found."
	case "conflict":
		code, message = modelingapi.ErrorConflict, "The modeling project changed before the operation completed."
	case "tool_denied":
		code, message = modelingapi.ErrorDenied, "A required modeling action was denied."
	case "unavailable", "disabled":
		code, message = modelingapi.ErrorUnavailable, "The modeling capability is not available."
	}
	retryable := code == modelingapi.ErrorConflict || code == modelingapi.ErrorUnavailable
	return modelingapi.NewPublicError(code, message, retryable, nil)
}
