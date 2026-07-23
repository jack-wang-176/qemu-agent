package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

type ReadArgs struct {
	FilePath string `json:"file_path"`
}
type ReadTool struct {
	workspace string
	maxLines  int
}

func NewReadTool(workspace string, maxLines int) *ReadTool {
	return &ReadTool{workspace: workspace, maxLines: maxLines}
}

func (t *ReadTool) Name() string        { return "read" }
func (t *ReadTool) Description() string { return "Read a file" }
func (t *ReadTool) Dangerous() bool     { return false }
func (t *ReadTool) Spec() schema.Spec {
	return schema.NewSpec(t.Name(), t.Description(), schema.Object(
		schema.Required("file_path", schema.String("The path of the file to read")),
	))
}
func (t *ReadTool) Execute(ctx context.Context, raw string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var args ReadArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("decode read args: %w", err)
	}
	if strings.TrimSpace(args.FilePath) == "" {
		return "", fmt.Errorf("file_path is required")
	}
	path, err := resolveWorkspacePath(t.workspace, args.FilePath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	if t.maxLines <= 0 {
		return "", fmt.Errorf("read max lines must be positive")
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > t.maxLines {
		lines = lines[:t.maxLines]
		return strings.Join(lines, "\n") + fmt.Sprintf("\n[truncated after %d lines]", t.maxLines), nil
	}
	return strings.Join(lines, "\n"), nil
}
