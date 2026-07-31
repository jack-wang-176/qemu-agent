package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
	"github.com/jack-wang-176/qemu-agent/internal/tools/builtin"
)

/* this function register series of tool. */
func BuildToolManager(paths config.PathConfig, cfg config.ToolConfig) (*tools.Manager, error) {
	manager := tools.NewManager()
	toolSet := []tools.Tool{
		builtin.NewReadTool(paths.Workspace, cfg.ReadMaxLines),
		builtin.NewWriteTool(paths.Workspace),
		builtin.NewBashTool(paths.Workspace, cfg.Timeout, cfg.MaxOutputBytes),
	}
	for _, tool := range toolSet {
		if err := manager.Register(tool); err != nil {
			return nil, fmt.Errorf(
				"register tool %q: %w",
				tool.Name(),
				err,
			)
		}
	}
	return manager, nil
}
