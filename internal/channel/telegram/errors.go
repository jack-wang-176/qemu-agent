package telegram

import (
	"errors"
	"fmt"
	"time"
)

type ErrorKind string

const (
	ErrorTransient      ErrorKind = "transient"
	ErrorRateLimited    ErrorKind = "rate_limited"
	ErrorAuthentication ErrorKind = "authentication"
	ErrorBadRequest     ErrorKind = "bad_request"
	ErrorProtocol       ErrorKind = "protocol"
)

type APIError struct {
	Kind       ErrorKind
	Operation  string
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s failed (%s, status=%d)", e.Operation, e.Kind, e.StatusCode)
}
func (e *APIError) Unwrap() error { return e.Err }

func IsRetryable(err error) bool {
	var target *APIError
	return errors.As(err, &target) && (target.Kind == ErrorTransient || target.Kind == ErrorRateLimited)
}
func RetryAfter(err error) time.Duration {
	var target *APIError
	if errors.As(err, &target) {
		return target.RetryAfter
	}
	return 0
}
func IsAuthentication(err error) bool {
	var target *APIError
	return errors.As(err, &target) && target.Kind == ErrorAuthentication
}
