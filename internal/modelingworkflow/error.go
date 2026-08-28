package modelingworkflow

import (
	"context"
	"errors"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

// ErrorKind is the stable workflow failure category exposed to entry adapters.
type ErrorKind string

const (
	ErrorInvalidInput     ErrorKind = "invalid_input"
	ErrorConflict         ErrorKind = "conflict"
	ErrorUnavailable      ErrorKind = "unavailable"
	ErrorNotFound         ErrorKind = "not_found"
	ErrorDenied           ErrorKind = "denied"
	ErrorApprovalRequired ErrorKind = "approval_required"
	ErrorApprovalDeclined ErrorKind = "approval_declined"
	ErrorCanceled         ErrorKind = "canceled"
	ErrorInternal         ErrorKind = "internal"
)

// Error keeps an inspectable cause while exposing only bounded, approved data.
type Error struct {
	Kind      ErrorKind
	Public    string
	Retryable bool
	Details   map[string]string
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Public
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func mapModelingError(err error) *Error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) {
		return existing
	}

	public := modelingapi.NewPublicError(
		modelingapi.ErrorInternal,
		"The modeling workflow could not be completed.",
		false,
		nil,
	)
	var apiErr *modelingapi.Error
	if errors.As(err, &apiErr) {
		public = modelingapi.ClonePublicError(apiErr.Public)
	} else if errors.Is(err, context.Canceled) {
		public = modelingapi.NewPublicError(modelingapi.ErrorCanceled, "The modeling request was canceled.", true, nil)
	} else if errors.Is(err, context.DeadlineExceeded) {
		public = modelingapi.NewPublicError(modelingapi.ErrorUnavailable, "The modeling request timed out.", true, nil)
	}

	return &Error{
		Kind:      errorKindForCode(public.Code),
		Public:    public.Message,
		Retryable: public.Retryable,
		Details:   cloneErrorDetails(public.Details),
		Cause:     err,
	}
}

func errorKindForCode(code modelingapi.ErrorCode) ErrorKind {
	switch code {
	case modelingapi.ErrorInvalidInput:
		return ErrorInvalidInput
	case modelingapi.ErrorConflict:
		return ErrorConflict
	case modelingapi.ErrorUnavailable:
		return ErrorUnavailable
	case modelingapi.ErrorNotFound:
		return ErrorNotFound
	case modelingapi.ErrorDenied:
		return ErrorDenied
	case modelingapi.ErrorApprovalRequired:
		return ErrorApprovalRequired
	case modelingapi.ErrorApprovalDeclined:
		return ErrorApprovalDeclined
	case modelingapi.ErrorCanceled:
		return ErrorCanceled
	default:
		return ErrorInternal
	}
}

func cloneErrorDetails(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
