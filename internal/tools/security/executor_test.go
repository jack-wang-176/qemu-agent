package security

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/tools"
	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

type fakeTool struct {
	calls      int
	persistent string
}

func (*fakeTool) Name() string        { return "fake" }
func (*fakeTool) Description() string { return "fake" }
func (*fakeTool) Spec() schema.Spec   { return schema.Spec{Name: "fake"} }
func (*fakeTool) Dangerous() bool     { return false }
func (t *fakeTool) Execute(context.Context, string) (tools.ExecutionResult, error) {
	t.calls++
	return tools.ExecutionResult{ModelOutput: "token=abc", PersistentOutput: t.persistent}, nil
}

type fakeCatalog struct{ tool tools.Tool }

func (c fakeCatalog) Lookup(string) (tools.Tool, bool) { return c.tool, c.tool != nil }

type fixedPolicy struct{ decision Decision }

func (p fixedPolicy) Evaluate(context.Context, Invocation, tools.Tool) (Assessment, error) {
	return Assessment{Decision: p.decision, Rule: "rule", Reason: "reason"}, nil
}

type memoryAudit struct {
	events []AuditEvent
	err    error
}

func (a *memoryAudit) Write(_ context.Context, event AuditEvent) error {
	a.events = append(a.events, event)
	return a.err
}

func newTestExecutor(t *testing.T, decision Decision, audit *memoryAudit) (*Executor, *fakeTool) {
	t.Helper()
	tool := &fakeTool{}
	redactor, _ := NewDefaultRedactor(100, 100)
	executor, err := NewExecutor(ExecutorDependencies{Catalog: fakeCatalog{tool}, Policy: fixedPolicy{decision}, Approver: AllowAllApprover{}, Audit: audit, Redactor: redactor, Now: func() time.Time { return time.Unix(1, 0) }}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return executor, tool
}

func testInvocation() Invocation {
	return Invocation{ID: "invocation", ToolName: "fake", Arguments: `{}`, RequestedAt: time.Now()}
}

func TestExecutorAllowAuditsBeforeAndAfter(t *testing.T) {
	audit := &memoryAudit{}
	executor, tool := newTestExecutor(t, DecisionAllow, audit)
	result, err := executor.Execute(context.Background(), testInvocation())
	if err != nil || tool.calls != 1 || len(audit.events) != 2 || audit.events[0].Phase != "authorized" || audit.events[1].Phase != "completed" || result.Output == "" {
		t.Fatalf("result=%#v err=%v calls=%d events=%#v", result, err, tool.calls, audit.events)
	}
	if audit.events[1].Output == result.Output {
		t.Fatal("audit output was not redacted")
	}
}

func TestExecutorDenyNeverExecutes(t *testing.T) {
	audit := &memoryAudit{}
	executor, tool := newTestExecutor(t, DecisionDeny, audit)
	_, err := executor.Execute(context.Background(), testInvocation())
	if !errors.Is(err, ErrDenied) || tool.calls != 0 || len(audit.events) != 1 {
		t.Fatalf("err=%v calls=%d events=%d", err, tool.calls, len(audit.events))
	}
}

func TestExecutorPreAuditFailureIsFailClosed(t *testing.T) {
	audit := &memoryAudit{err: errors.New("audit failed")}
	executor, tool := newTestExecutor(t, DecisionAllow, audit)
	_, err := executor.Execute(context.Background(), testInvocation())
	if err == nil || tool.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, tool.calls)
	}
}

func TestExecutorDefaultsPersistentOutputToModelOutput(t *testing.T) {
	audit := &memoryAudit{}
	executor, _ := newTestExecutor(t, DecisionAllow, audit)
	result, err := executor.Execute(context.Background(), testInvocation())
	if err != nil {
		t.Fatal(err)
	}
	if result.PersistentOutput != result.Output || result.ProjectionChanged() {
		t.Fatalf("result=%#v", result)
	}
	if audit.events[1].ProjectionChanged || audit.events[1].PersistentOutput != "" {
		t.Fatalf("audit=%#v", audit.events[1])
	}
}

func TestExecutorRecordsProjectionInAudit(t *testing.T) {
	audit := &memoryAudit{}
	executor, tool := newTestExecutor(t, DecisionAllow, audit)
	tool.persistent = "receipt"
	result, err := executor.Execute(context.Background(), testInvocation())
	if err != nil {
		t.Fatal(err)
	}
	if result.PersistentOutput != "receipt" || !result.ProjectionChanged() {
		t.Fatalf("result=%#v", result)
	}
	if !audit.events[1].ProjectionChanged || audit.events[1].PersistentOutput != "receipt" {
		t.Fatalf("audit=%#v", audit.events[1])
	}
}
