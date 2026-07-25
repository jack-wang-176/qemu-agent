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

// execute return struct
type Result struct {
	InvocationID string
	Output       string
	Decision     Decision
	Rule         string
	StartedAt    time.Time
	FinishedAt   time.Time
}
