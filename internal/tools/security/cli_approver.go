package security

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type CLIApprover struct {
	mu     sync.Mutex
	reader *bufio.Reader
	writer io.Writer
	now    func() time.Time
}

func NewCLIApprover(reader *bufio.Reader, writer io.Writer) (*CLIApprover, error) {
	if reader == nil {
		return nil, errors.New("CLI approver reader is nil")
	}
	if writer == nil {
		return nil, errors.New("CLI approver writer is nil")
	}
	return &CLIApprover{reader: reader, writer: writer, now: time.Now}, nil
}

func (a *CLIApprover) Approve(ctx context.Context, request ApprovalRequest) (Approval, error) {
	if err := ctx.Err(); err != nil {
		return Approval{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := fmt.Fprintf(a.writer, "\nTool approval required\n  tool: %s\n  rule: %s\n  reason: %s\n  summary: %s\nApprove? [y/N]: ", request.Invocation.ToolName, request.Assessment.Rule, request.Assessment.Reason, request.Assessment.Summary); err != nil {
		return Approval{}, fmt.Errorf("write approval prompt: %w", err)
	}
	line, err := a.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Approval{}, fmt.Errorf("read approval response: %w", err)
	}
	answer := strings.TrimSpace(line)
	approved := strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
	reason := "user declined"
	if approved {
		reason = "user approved"
	}
	return Approval{Approved: approved, Actor: "cli-user", Reason: reason, At: a.now()}, nil
}
