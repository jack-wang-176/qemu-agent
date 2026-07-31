package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

type WriteToolParam struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type WriteTool struct {
	workspace string
}

func NewWriteTool(workspace string) *WriteTool {
	return &WriteTool{workspace: workspace}
}

func (wt *WriteTool) Name() string {
	return "write"
}
func (wt *WriteTool) Description() string { return "Write content to a file" }
func (wt *WriteTool) Dangerous() bool     { return true }
func (wt *WriteTool) Spec() schema.Spec {
	return schema.NewSpec(wt.Name(), wt.Description(), schema.Object(
		schema.Required("file_path", schema.String("The path of the file to write")),
		schema.Required("content", schema.String("The content to write into the file")),
	))
}
func (wt *WriteTool) Execute(ctx context.Context, args string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var arg WriteToolParam
	if err := json.Unmarshal([]byte(args), &arg); err != nil {
		return "", fmt.Errorf("decode write args: %w", err)
	}
	if strings.TrimSpace(arg.FilePath) == "" {
		return "", fmt.Errorf("file_path is required")
	}
	path, err := resolveWorkspacePath(wt.workspace, arg.FilePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(arg.Content), 0644); err != nil {
		return "", fmt.Errorf("write %q: %w", path, err)
	}
	return fmt.Sprintf("File written successfully: %s", path), nil
}
