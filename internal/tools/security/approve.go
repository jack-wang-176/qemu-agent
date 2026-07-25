package security

import (
	"context"
	"time"
)

type ApprovalRequest struct {
	Invocation Invocation
	Assessment Assessment
	Deadline   time.Time
}

type Approver interface {
	Approve(context.Context, ApprovalRequest) (Approval, error)
}

type AllowAllApprover struct{}
type DenyAllApprover struct{}

type RoutingApprover struct {
	Interactive Approver
	Fallback    Approver
}

func (a RoutingApprover) Approve(ctx context.Context, request ApprovalRequest) (Approval, error) {
	if request.Invocation.Interactive {
		return a.Interactive.Approve(ctx, request)
	}
	return a.Fallback.Approve(ctx, request)
}

func (AllowAllApprover) Approve(ctx context.Context, _ ApprovalRequest) (Approval, error) {
	if err := ctx.Err(); err != nil {
		return Approval{}, err
	}
	return Approval{Approved: true, Actor: "automatic", Reason: "automatically approved", At: time.Now()}, nil
}

func (DenyAllApprover) Approve(ctx context.Context, req ApprovalRequest) (Approval, error) {
	if err := ctx.Err(); err != nil {
		return Approval{}, err
	}
	return Approval{
		Approved: false,
		Actor:    "non-interactive",
		Reason:   "interactive approval is unavailable",
		At:       time.Now(),
	}, nil
}
