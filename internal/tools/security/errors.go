package security

import (
	"errors"
	"fmt"
)

var (
	ErrDenied           = errors.New("tool invocation denied")
	ErrApprovalRequired = errors.New("tool approval required")
	ErrApprovalDeclined = errors.New("tool approval declined")
)

type DeniedError struct {
	Tool   string
	Rule   string
	Reason string
}

func (e DeniedError) Error() string {
	return fmt.Sprintf("tool %q denied by %s: %s", e.Tool, e.Rule, e.Reason)
}

func (e DeniedError) Unwrap() error { return ErrDenied }
