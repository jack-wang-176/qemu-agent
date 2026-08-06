package modelingapi

import "strings"

// context.go defines the trusted invocation context contract.
//
// Design principles (v1-06, parts 3 and 5):
//   - Adapters inject CallContext fields; model and MCP tool arguments cannot set them.
//   - Fields are stable identifiers and never contain absolute paths, provider secrets,
//     or current Pipeline implementation details.
//   - WorkspaceID is a stable identifier, not an absolute workspace path.
//   - Mutating use cases require an IdempotencyKey; read-only use cases do not.
//   - Interactive=false is valid and only restricts approval-dependent capabilities.
//
// modelingapi does not generate identifiers. The entry adapter or composition root
// generates RequestID, TraceID, and IdempotencyKey for testing, auditing, and deduplication.

// CallContext carries trusted runtime identity for one use-case invocation.
//
// All untrusted input belongs in request DTOs, never CallContext. This prevents
// model or MCP arguments from forging authorization data.
type CallContext struct {
	RequestID      string
	TraceID        string
	WorkspaceID    string
	UserID         string // May be empty for CLI; MCP and Agent adapters should inject it.
	SessionID      string // May be empty for MCP or command invocations.
	SessionKey     string // Channel-level correlation key; never used for authorization.
	Channel        string // Audited source such as "cli", "agent", or "mcp".
	Interactive    bool   // Whether a human is available to answer approval prompts.
	IdempotencyKey string // Required for mutating use cases.
}

// MutationKind reports whether a use case changes domain state and therefore
// requires idempotency validation.
type MutationKind int

const (
	// ReadOnly covers Capabilities, List, Show, ReadArtifact, Evidence, and PlanApply.
	ReadOnly MutationKind = iota
	// Mutating covers Create, Advance, Reset, and Apply.
	Mutating
)

// Validate checks required CallContext fields and length limits.
//
// Read-only calls may omit IdempotencyKey; mutating calls require it to prevent
// replay. Identifiers must be canonical, non-empty where required, bounded, and
// free of control characters. CallContext deliberately has no path fields.
func (c CallContext) Validate(kind MutationKind) error {
	if strings.TrimSpace(c.RequestID) == "" {
		return errMissing("request_id")
	}
	if strings.TrimSpace(c.TraceID) == "" {
		return errMissing("trace_id")
	}
	if strings.TrimSpace(c.WorkspaceID) == "" {
		return errMissing("workspace_id")
	}
	if strings.TrimSpace(c.Channel) == "" {
		return errMissing("channel")
	}
	if hasControlChar(c.RequestID) || hasControlChar(c.TraceID) ||
		hasControlChar(c.WorkspaceID) || hasControlChar(c.Channel) ||
		hasControlChar(c.UserID) || hasControlChar(c.SessionID) ||
		hasControlChar(c.SessionKey) || hasControlChar(c.IdempotencyKey) {
		return errControlChar()
	}
	if len(c.RequestID) > MaxIDBytes || len(c.TraceID) > MaxIDBytes ||
		len(c.WorkspaceID) > MaxIDBytes || len(c.UserID) > MaxIDBytes ||
		len(c.SessionID) > MaxIDBytes || len(c.SessionKey) > MaxIDBytes ||
		len(c.Channel) > MaxIDBytes || len(c.IdempotencyKey) > MaxIDBytes {
		return errTooLong()
	}
	switch kind {
	case ReadOnly:
	case Mutating:
		if strings.TrimSpace(c.IdempotencyKey) == "" {
			return errMissing("idempotency_key")
		}
	default:
		return errInvalid("modelingapi: unknown mutation kind")
	}
	if strings.TrimSpace(c.UserID) != c.UserID ||
		strings.TrimSpace(c.SessionID) != c.SessionID ||
		strings.TrimSpace(c.SessionKey) != c.SessionKey ||
		strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey {
		return errInvalid("modelingapi: identifiers must be canonical")
	}
	if strings.TrimSpace(c.RequestID) != c.RequestID ||
		strings.TrimSpace(c.TraceID) != c.TraceID ||
		strings.TrimSpace(c.WorkspaceID) != c.WorkspaceID ||
		strings.TrimSpace(c.Channel) != c.Channel {
		return errInvalid("modelingapi: identifiers must be canonical")
	}
	return nil
}

// Clone returns a copy of CallContext. All fields are value strings.
func (c CallContext) Clone() CallContext { return c }
