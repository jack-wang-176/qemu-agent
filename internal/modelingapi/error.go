package modelingapi

// error.go defines the stable public error contract.
//
// A1 defines error categories, safe-message rules, and the Error wrapper. Mapping
// current modeling errors into this contract belongs to A5 modelingapp.
//
// Safety rules (v1-06, part 10):
//   - Message uses stable public wording and never copies err.Error().
//   - Details accepts only allowlisted keys.
//   - Absolute paths, provider secrets, tool stderr, and raw model output are forbidden.

// ErrorCode is a stable, finite category that external adapters can translate.
type ErrorCode string

// Public error categories. Adding a category is an API-versioned change.
const (
	ErrorInvalidInput     ErrorCode = "invalid_input"
	ErrorNotFound         ErrorCode = "not_found"
	ErrorConflict         ErrorCode = "conflict"
	ErrorDenied           ErrorCode = "denied"
	ErrorApprovalRequired ErrorCode = "approval_required"
	ErrorApprovalDeclined ErrorCode = "approval_declined"
	ErrorUnavailable      ErrorCode = "unavailable"
	ErrorCanceled         ErrorCode = "canceled"
	ErrorInternal         ErrorCode = "internal"
)

// allowedDetailKeys is the allowlist for PublicError.Details.
// ValidatePublicError rejects every other key.
var allowedDetailKeys = map[string]struct{}{
	"project_id":        {},
	"operation":         {},
	"current_revision":  {},
	"expected_revision": {},
	"capability":        {},
}

// PublicError is a stable, safe, serializable error description for callers.
//
// It never contains Cause, raw err.Error() text, absolute paths, or provider
// secrets. modelingapp maps internal implementation failures into PublicError.
type PublicError struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   map[string]string
}

// Error carries a public error and an internal cause together.
//
// modelingapi does not map Cause to PublicError. This type only lets adapters and
// modelingapp retain both values in one error chain. Cause is never validated as
// public data and must never be serialized into a caller-facing response.
type Error struct {
	Public PublicError
	Cause  error
}

// Error returns only the public message and never appends Cause.Error().
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Public.Message
}

// Unwrap supports errors.Is and errors.As.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewPublicError constructs a PublicError. details may be nil. Unknown keys panic
// so programming mistakes are exposed during development.
func NewPublicError(code ErrorCode, message string, retryable bool, details map[string]string) PublicError {
	for k := range details {
		if _, ok := allowedDetailKeys[k]; !ok {
			// Never silently discard unknown keys; doing so could hide a future data leak.
			panic("modelingapi: disallowed PublicError detail key: " + k)
		}
	}
	cloned := make(map[string]string, len(details))
	for key, value := range details {
		cloned[key] = value
	}
	return PublicError{
		Code:      code,
		Message:   message,
		Retryable: retryable,
		Details:   cloned,
	}
}
