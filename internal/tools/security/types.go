package security

import (
	"time"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
	DecisionAsk   Decision = "ask"
)

// access to channel and session
type Invocation struct {
	ID          string
	TraceID     string
	SessionID   string
	SessionKey  string
	Channel     string
	Interactive bool
	ToolName    string
	Arguments   string
	RequestedAt time.Time
}

// assess of tool property
type Assessment struct {
	Decision Decision
	Rule     string
	Reason   string
	Summary  string
}

// description of certain tool
type Approval struct {
	Approved bool
	Actor    string
	Reason   string
	At       time.Time
}

// execute return struct. Output is what the model may see for this request;
// PersistentOutput is what the session is allowed to keep. They differ only for
// tools that return transient payloads, such as use_skill.
type Result struct {
	InvocationID     string
	Output           string
	PersistentOutput string
	Decision         Decision
	Rule             string
	StartedAt        time.Time
	FinishedAt       time.Time
}

// ProjectionChanged reports whether the persisted text differs from what the
// model saw. The audit log records this so a shrinking transcript is explained
// by a projection rather than looking like data loss.
func (r Result) ProjectionChanged() bool {
	return r.PersistentOutput != "" && r.PersistentOutput != r.Output
}
