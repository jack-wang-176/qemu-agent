package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

type BashToolParam struct {
	Command string `json:"command"`
}
type BashTool struct {
	workspace      string
	timeout        time.Duration
	maxOutputBytes int
}

func NewBashTool(workspace string, timeout time.Duration, maxOutputBytes int) *BashTool {
	return &BashTool{
		workspace: workspace, timeout: timeout, maxOutputBytes: maxOutputBytes,
	}
}

func (bt *BashTool) Name() string {
	return "bash"
}
func (bt *BashTool) Description() string { return "Execute a bash command on the system" }
func (bt *BashTool) Dangerous() bool     { return true }
func (bt *BashTool) Spec() schema.Spec {
	return schema.NewSpec(bt.Name(), bt.Description(), schema.Object(
		schema.Required("command", schema.String("The bash command to execute")),
	))
}
func (bt *BashTool) Execute(ctx context.Context, args string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var arg BashToolParam
	if err := json.Unmarshal([]byte(args), &arg); err != nil {
		return "", fmt.Errorf("decode bash args: %w", err)
	}
	if strings.TrimSpace(arg.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if bt.timeout <= 0 || bt.maxOutputBytes <= 0 {
		return "", fmt.Errorf("invalid bash tool limits")
	}
	commandCtx, cancel := context.WithTimeout(ctx, bt.timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "bash", "-c", arg.Command)
	cmd.Dir = bt.workspace
	output, err := cmd.CombinedOutput()
	truncated := false
	if len(output) > bt.maxOutputBytes {
		output = output[:bt.maxOutputBytes]
		truncated = true
	}
	result := string(output)
	if truncated {
		result += fmt.Sprintf("\n[truncated after %d bytes]", bt.maxOutputBytes)
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("bash command timed out after %s", bt.timeout)
	}
	if err != nil {
		return result, fmt.Errorf("bash command failed: %w", err)
	}
	return result, nil
}
