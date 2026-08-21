package modelingapp

import (
	"context"
	"errors"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

var errUnsupported = errors.New("modelingapp: operation is not supported by the current engine")

func unsupported(capability string) error {
	return &modelingapi.Error{Public: modelingapi.NewPublicError(modelingapi.ErrorUnavailable, "This modeling capability is not available.", false, map[string]string{"capability": capability}), Cause: errUnsupported}
}

func mapInternalError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*modelingapi.Error); ok {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &modelingapi.Error{Public: modelingapi.NewPublicError(modelingapi.ErrorCanceled, "The modeling operation was canceled.", true, nil), Cause: err}
	}
	return &modelingapi.Error{Public: modelingapi.NewPublicError(modelingapi.ErrorInternal, "The modeling operation could not be completed.", false, nil), Cause: err}
}

func publicErrorForCategory(category string) modelingapi.PublicError {
	code := modelingapi.ErrorInternal
	message := "The previous modeling operation could not be completed."
	switch category {
	case "not_found":
		code, message = modelingapi.ErrorNotFound, "The modeling project was not found."
	case "conflict":
		code, message = modelingapi.ErrorConflict, "The modeling project changed before the operation completed."
	case "unavailable", "disabled":
		code, message = modelingapi.ErrorUnavailable, "The modeling capability is not available."
	}
	return modelingapi.NewPublicError(code, message, false, nil)
}
