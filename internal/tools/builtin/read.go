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
type ReadTool struct{}

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
	data, err := os.ReadFile(args.FilePath)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", args.FilePath, err)
	}
	return string(data), nil
}
