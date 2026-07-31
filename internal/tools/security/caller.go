// Caller identity carried through a context.
//
// The Agent builds an Invocation directly, because it holds the Session and the
// request side by side. Background subsystems do not: the modeling pipeline's
// stages call tools through a narrow `Run(ctx, name, args)` seam, deliberately,
// so that a stage cannot construct a policy decision or forge an audit identity.
// That seam has nowhere to put "which request am I serving".
//
// Baking a fixed identity into the adapter at startup was the alternative, and it
// is wrong in both directions: a hardcoded Interactive=false denies every write
// and bash call, which disables verify and apply outright, while a hardcoded
// Interactive=true routes an approval prompt to the operator's terminal even for
// a request that arrived over Telegram. So the identity travels with the request,
// which is what context values are for.
//
// Nothing here grants a permission. A Caller only says who is asking; Policy and
// Approver still decide, and a missing Caller must be treated as a refusal by
// whoever needed one — see the fail-closed note on CallerFrom.
package security

import (
	"context"
	"errors"
	"strings"
)

// Caller is the request metadata an Invocation needs and a background subsystem
// cannot derive. The fields mirror Invocation's, minus the ones that belong to
// the individual call (ID, ToolName, Arguments, RequestedAt): those are the
// adapter's to fill in per tool call, while these are the request's and stay
// constant for every call made while serving it.
type Caller struct {
	TraceID   string
	SessionID string
	// SessionKey is the channel-scoped conversation key. It is an audit
	// correlation value only. In particular it is never a source of user
	// identity: a subsystem that needs a UserID takes it from its own scope.
	SessionKey string
	Channel    string
	// Interactive reports whether somebody is on the other end who can answer an
	// approval prompt. It is the channel's capability, copied through, never a
	// guess made by the code that consumes it.
	Interactive bool
}

// Validate rejects a Caller that cannot be audited. Channel is required because
// an audit entry with no channel cannot be traced back to how the request
// arrived, which is most of the value of having the log.
func (c Caller) Validate() error {
	if strings.TrimSpace(c.Channel) == "" {
		return errors.New("caller channel is empty")
	}
	if strings.TrimSpace(c.TraceID) == "" {
		return errors.New("caller trace id is empty")
	}
	return nil
}

// callerKey is unexported and of a private type, so no other package can put a
// Caller into a context without going through WithCaller. That matters: this
// value influences whether an approval prompt is shown, and a string key would
// let any package fabricate one.
type callerKey struct{}

// WithCaller attaches the request's identity to ctx.
//
// It is called at the edge — where a command or a channel handler already knows
// the session and the channel's capabilities — and read much deeper, inside a
// tool adapter that has no other way to learn them.
func WithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// CallerFrom returns the identity attached to ctx, if any.
//
// The bool is not a formality: a caller that needs an identity to build an
// audited Invocation must fail closed when it is absent, because "no identity"
// means the request path forgot to attach one, and guessing would either deny a
// legitimate write or route an approval prompt to a terminal nobody is watching.
func CallerFrom(ctx context.Context) (Caller, bool) {
	if ctx == nil {
		return Caller{}, false
	}
	caller, ok := ctx.Value(callerKey{}).(Caller)
	if !ok {
		return Caller{}, false
	}
	if err := caller.Validate(); err != nil {
		return Caller{}, false
	}
	return caller, true
}
