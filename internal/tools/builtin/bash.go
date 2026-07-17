package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

type BashToolParam struct {
	Command string `json:"command"`
}
type BashTool struct{}

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
	var arg BashToolParam
	if err := json.Unmarshal([]byte(args), &arg); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", arg.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("%s\n[exit error] %v", output, err), nil
	}
	return string(output), nil
}
