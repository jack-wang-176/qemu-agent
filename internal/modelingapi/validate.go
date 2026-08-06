package modelingapi

// validate.go defines validation helpers for public use-case values.
//
// Centralized constants and rules keep adapters from reimplementing validation.
// Adapters call these Validate functions after mapping values into this package.
//
// Validation failures return modelingapi.Error with ErrorInvalidInput. They never
// retain unsafe raw input, and error details use only allowlisted keys.

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Centralized size limits from v1-06, part 12.
const (
	MaxTitleRunes          = 120
	MaxInstructionBytes    = 8 * 1024 // Larger inputs should be supplied as sources.
	MaxSources             = 16
	MaxSourceValueBytes    = 1024
	MaxSourceDigestBytes   = 64
	MaxSummaryBytes        = 4 * 1024
	MaxPageSize            = 100
	MaxEventTextBytes      = 512
	MaxOperationNameBytes  = 64
	MaxIDBytes             = 128
	MaxPreviewSummaryBytes = 2 * 1024
	MaxFileChanges         = 256
)

// ValidateCallContext is the common entry point used by adapters.
func ValidateCallContext(call CallContext, kind MutationKind) error {
	return call.Validate(kind)
}

// errInvalid returns an ErrorInvalidInput without unsafe details.
func errInvalid(msg string) error {
	return &Error{Public: PublicError{
		Code:    ErrorInvalidInput,
		Message: msg,
	}}
}

func errMissing(field string) error {
	return errInvalid("modelingapi: missing required field: " + field)
}

func errControlChar() error { return errInvalid("modelingapi: control character not allowed") }

func errTooLong() error { return errInvalid("modelingapi: field exceeds length limit") }

// hasControlChar reports whether s contains a Unicode control character.
func hasControlChar(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ValidateTitle validates CreateRequest.Title.
func ValidateTitle(title string) error {
	t := strings.TrimSpace(title)
	if t == "" {
		return errMissing("title")
	}
	if utf8.RuneCountInString(t) > MaxTitleRunes {
		return errTooLong()
	}
	if hasControlChar(t) {
		return errControlChar()
	}
	return nil
}

// ValidateInstruction validates the bounded, untrusted instruction field.
func ValidateInstruction(instruction string) error {
	if len(instruction) > MaxInstructionBytes {
		return errTooLong()
	}
	return nil // Empty is valid because some operations need no instruction.
}

// ValidateSources checks source count, item sizes, digests, paths, and duplicates.
func ValidateSources(sources []SourceRef) error {
	if len(sources) > MaxSources {
		return errTooLong()
	}
	seen := make(map[string]struct{}, len(sources))
	for i, s := range sources {
		if s.Kind == "" {
			return errMissing("source.kind")
		}
		if len(s.Kind) > MaxSourceValueBytes {
			return errTooLong()
		}
		if s.Value == "" {
			return errMissing("source.value")
		}
		if len(s.Value) > MaxSourceValueBytes {
			return errTooLong()
		}
		if hasControlChar(s.Kind) || hasControlChar(s.Value) {
			return errControlChar()
		}
		if s.Digest != "" && !isLowerHex(s.Digest, MaxSourceDigestBytes) {
			return errInvalid("modelingapi: source digest must be sha256 hex")
		}
		if s.Kind == "workspace_path" {
			if err := validateRelativePath(s.Value); err != nil {
				return err
			}
		}
		key := s.Kind + "|" + s.Value
		if _, dup := seen[key]; dup {
			// Adapters may normalize sources, but validation still rejects duplicates.
			_ = i
			return errInvalid("modelingapi: duplicate source: " + key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func isLowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validateRelativePath(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errInvalid("modelingapi: path must be canonical")
	}
	if strings.ContainsRune(value, '\\') || path.IsAbs(value) || path.Clean(value) != value ||
		value == ".." || strings.HasPrefix(value, "../") {
		return errInvalid("modelingapi: path must be a canonical relative path")
	}
	return nil
}

// ValidateOperationName checks the [a-z][a-z0-9._-]{0,63} format.
//
// A well-formed unknown operation remains valid here; capabilities and the
// application layer decide whether the current Engine supports it.
func ValidateOperationName(op OperationName) error {
	s := string(op)
	if s == "" {
		return nil // Empty selects the current or recommended operation.
	}
	if len(s) > MaxOperationNameBytes {
		return errTooLong()
	}
	for i, r := range s {
		if i == 0 {
			if !(r >= 'a' && r <= 'z') {
				return errInvalid("modelingapi: operation must start with [a-z]")
			}
			continue
		}
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-') {
			return errInvalid("modelingapi: operation contains invalid char")
		}
	}
	return nil
}

// ValidatePageSize validates List and Evidence limits. Zero requests the adapter
// default; values greater than MaxPageSize are rejected.
func ValidatePageSize(limit int) error {
	if limit < 0 {
		return errInvalid("modelingapi: negative page size")
	}
	if limit > MaxPageSize {
		return errTooLong()
	}
	return nil
}

// ValidateSummary validates the public OperationResult summary.
func ValidateSummary(summary string) error {
	if len(summary) > MaxSummaryBytes {
		return errTooLong()
	}
	if hasControlChar(summary) {
		return errControlChar()
	}
	return nil
}

// ValidateEventText validates bounded event progress and summary text.
func ValidateEventText(s string) error {
	if len(s) > MaxEventTextBytes {
		return errTooLong()
	}
	if hasControlChar(s) {
		return errControlChar()
	}
	return nil
}

// ValidatePublicError checks the error code, public message, and detail allowlist.
func ValidatePublicError(p PublicError) error {
	switch p.Code {
	case ErrorInvalidInput, ErrorNotFound, ErrorConflict, ErrorDenied,
		ErrorApprovalRequired, ErrorApprovalDeclined, ErrorUnavailable,
		ErrorCanceled, ErrorInternal:
	default:
		return errInvalid("modelingapi: unknown public error code")
	}
	if p.Message == "" {
		return errMissing("error.message")
	}
	if len(p.Message) > MaxSummaryBytes {
		return errTooLong()
	}
	if hasControlChar(p.Message) {
		return errControlChar()
	}
	for k, v := range p.Details {
		if _, ok := allowedDetailKeys[k]; !ok {
			return errInvalid("modelingapi: disallowed error detail key: " + k)
		}
		if hasControlChar(v) {
			return errControlChar()
		}
		if len(v) > MaxIDBytes {
			return errTooLong()
		}
	}
	return nil
}
