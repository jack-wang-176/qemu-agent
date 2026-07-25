package security

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

type Catalog interface {
	Lookup(string) (tools.Tool, bool)
}

type Executor struct {
	catalog         Catalog
	policy          Policy
	approver        Approver
	audit           AuditSink
	redactor        Redactor
	approvalTimeout time.Duration
	now             func() time.Time
}

type ExecutorDependencies struct {
	Catalog  Catalog
	Policy   Policy
	Approver Approver
	Audit    AuditSink
	Redactor Redactor
	Now      func() time.Time
}

func NewExecutor(deps ExecutorDependencies, timeout time.Duration) (*Executor, error) {
	if deps.Catalog == nil {
		return nil, errors.New("executor catalog is nil")
	}
	if deps.Policy == nil {
		return nil, errors.New("executor policy is nil")
	}
	if deps.Approver == nil {
		return nil, errors.New("executor approver is nil")
	}
	if deps.Audit == nil {
		return nil, errors.New("executor audit sink is nil")
	}
	if deps.Redactor == nil {
		return nil, errors.New("executor redactor is nil")
	}
	if timeout <= 0 {
		return nil, errors.New("executor approval timeout must be positive")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Executor{catalog: deps.Catalog, policy: deps.Policy, approver: deps.Approver, audit: deps.Audit, redactor: deps.Redactor, approvalTimeout: timeout, now: deps.Now}, nil
}

func (e *Executor) Execute(ctx context.Context, in Invocation) (Result, error) {
	if err := validateInvocation(in); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	tool, ok := e.catalog.Lookup(in.ToolName)
	if !ok {
		return Result{}, fmt.Errorf("tool %q not found", in.ToolName)
	}
	assessment, err := e.policy.Evaluate(ctx, in, tool)
	if err != nil {
		return Result{}, fmt.Errorf("evaluate tool policy: %w", err)
	}
	approval, decisionErr := e.resolveApproval(ctx, in, assessment)
	if decisionErr != nil {
		return Result{}, errors.Join(decisionErr, e.writeAudit(ctx, "decision", in, assessment, approval, Result{}, decisionErr))
	}
	started := e.now()
	if err := e.writeAudit(ctx, "authorized", in, assessment, approval, Result{StartedAt: started}, nil); err != nil {
		return Result{}, fmt.Errorf("write pre-execution audit: %w", err)
	}
	output, execErr := tool.Execute(ctx, in.Arguments)
	result := Result{InvocationID: in.ID, Output: output, Decision: assessment.Decision, Rule: assessment.Rule, StartedAt: started, FinishedAt: e.now()}
	return result, errors.Join(execErr, e.writeAudit(ctx, "completed", in, assessment, approval, result, execErr))
}

func validateInvocation(in Invocation) error {
	if strings.TrimSpace(in.ID) == "" {
		return errors.New("invocation id is empty")
	}
	if strings.TrimSpace(in.ToolName) == "" {
		return errors.New("invocation tool name is empty")
	}
	if strings.TrimSpace(in.Arguments) == "" {
		return errors.New("invocation arguments are empty")
	}
	if in.RequestedAt.IsZero() {
		return errors.New("invocation requested time is zero")
	}
	return nil
}

func (e *Executor) resolveApproval(ctx context.Context, in Invocation, assessment Assessment) (*Approval, error) {
	switch assessment.Decision {
	case DecisionAllow:
		return nil, nil
	case DecisionDeny:
		return nil, DeniedError{Tool: in.ToolName, Rule: assessment.Rule, Reason: assessment.Reason}
	case DecisionAsk:
		approvalCtx, cancel := context.WithTimeout(ctx, e.approvalTimeout)
		defer cancel()
		approval, err := e.approver.Approve(approvalCtx, ApprovalRequest{Invocation: in, Assessment: assessment, Deadline: e.now().Add(e.approvalTimeout)})
		if err != nil {
			return nil, fmt.Errorf("approve tool invocation: %w", err)
		}
		if !approval.Approved {
			return &approval, fmt.Errorf("%w: %s", ErrApprovalDeclined, approval.Reason)
		}
		return &approval, nil
	default:
		return nil, fmt.Errorf("unsupported policy decision %q", assessment.Decision)
	}
}

func (e *Executor) writeAudit(ctx context.Context, phase string, in Invocation, assessment Assessment, approval *Approval, result Result, eventErr error) error {
	event := AuditEvent{Version: 1, Phase: phase, InvocationID: in.ID, TraceID: in.TraceID, SessionID: in.SessionID, SessionKey: in.SessionKey, Channel: in.Channel, ToolName: in.ToolName, Arguments: e.redactor.RedactArguments(in.ToolName, in.Arguments), Decision: assessment.Decision, Rule: assessment.Rule, Reason: assessment.Reason, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt, Output: e.redactor.RedactOutput(in.ToolName, result.Output)}
	if approval != nil {
		approved := approval.Approved
		event.Approved = &approved
		event.Actor = approval.Actor
	}
	if eventErr != nil {
		event.Error = e.redactor.RedactOutput(in.ToolName, eventErr.Error())
	}
	return e.audit.Write(ctx, event)
}
