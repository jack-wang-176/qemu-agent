package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
	"github.com/jack-wang-176/qemu-agent/internal/tools/builtin"
)

/* this function register series of tool. */
func BuildToolManager(cfg config.Config) (*tools.Manager, error) {
	manager := tools.NewManager()
	toolSet := []tools.Tool{
		&builtin.ReadTool{},
		&builtin.WriteTool{},
		&builtin.BashTool{},
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
