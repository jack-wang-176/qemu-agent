package pipelineapi

import "errors"

// error.go - Internal pipeline contract errors.
//
// pipelineapi remains independent of modelingapi. modelingapp maps these errors
// to stable, caller-safe public errors.

// ErrMissingPort reports a required runtime port that was not injected.
type ErrMissingPort PortName

// Error implements error.
func (e ErrMissingPort) Error() string {
	return "pipelineapi: missing runtime port: " + string(e)
}

// PortError reports a runtime port failure while retaining its internal cause.
//
// Message is internal diagnostic text and must not be copied into a public error.
type PortError struct {
	Port    PortName
	Cause   error
	Message string
}

// Error returns the port name and internal diagnostic message.
func (e *PortError) Error() string {
	if e == nil {
		return ""
	}
	return "pipelineapi: port " + string(e.Port) + " error: " + e.Message
}

// Unwrap supports errors.Is and errors.As.
func (e *PortError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewPortError constructs a PortError.
func NewPortError(port PortName, message string, cause error) *PortError {
	return &PortError{
		Port:    port,
		Cause:   cause,
		Message: message,
	}
}

type categorizedError interface {
	ErrorCategory() string
}

// ErrorCategory returns a stable internal category without parsing error text.
func ErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	var categorized categorizedError
	if errors.As(err, &categorized) {
		return categorized.ErrorCategory()
	}
	var missing ErrMissingPort
	if errors.As(err, &missing) {
		return "unavailable"
	}
	var portErr *PortError
	if errors.As(err, &portErr) {
		return "unavailable"
	}
	return "internal"
}
