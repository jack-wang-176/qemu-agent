package build

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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

// BuildModelingToolManager registers the same three tools rooted at the QEMU
// source tree instead of the workspace.
//
// A second catalog is needed because the tools enforce their root themselves: the
// workspace-rooted write tool refuses any path outside Paths.Workspace, and a
// QEMU checkout is not inside it. The alternative — widening the agent's own
// write tool to cover both trees — would let a model write into QEMU during an
// ordinary conversation, which is exactly the capability the modeling pipeline is
// built to keep behind a reviewed plan.
//
// Everything else about the resulting executions is unchanged: the caller pairs
// this catalog with the same Policy, Approver, Redactor and audit sink, so a write
// into QEMU is judged and logged by the same rules as any other write. The
// modeling pipeline never reaches the workspace catalog, and the agent never
// reaches this one.
func BuildModelingToolManager(qemuRoot string, cfg config.ToolConfig) (*tools.Manager, error) {
	if strings.TrimSpace(qemuRoot) == "" {
		return nil, errors.New("modeling tool root is empty")
	}
	if !filepath.IsAbs(qemuRoot) {
		// A relative root would resolve against whatever directory the process was
		// started in, which is not a property anybody reviewing an apply can see.
		return nil, fmt.Errorf("modeling tool root %q must be absolute", qemuRoot)
	}
	root := filepath.Clean(qemuRoot)
	manager := tools.NewManager()
	toolSet := []tools.Tool{
		builtin.NewReadTool(root, cfg.ReadMaxLines),
		builtin.NewWriteTool(root),
		builtin.NewBashTool(root, cfg.Timeout, cfg.MaxOutputBytes),
	}
	for _, tool := range toolSet {
		if err := manager.Register(tool); err != nil {
			return nil, fmt.Errorf("register modeling tool %q: %w", tool.Name(), err)
		}
	}
	return manager, nil
}
