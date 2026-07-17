package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type RegisterTool struct {
	Tool   Tool
	Schema llm.ToolSchema
}
type Manager struct {
	Tool map[string]RegisterTool
}

func NewManager() *Manager {
	return &Manager{
		Tool: make(map[string]RegisterTool),
	}
}

/* Register the core of this function is generated useful param*/
func (m *Manager) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("cannot register nil tool")
	}
	if tool.Name() == "" {
		return fmt.Errorf("tool name is empty")
	}
	if _, ok := m.Tool[tool.Name()]; ok {
		return fmt.Errorf("tool %q already registered", tool.Name())
	}
	spec := tool.Spec()
	if spec.Name != tool.Name() {
		return fmt.Errorf("tool name mismatch")
	}
	/* generate spec parameter.*/
	params, err := spec.Parameter()
	if err != nil {
		return err
	}
	m.Tool[tool.Name()] = RegisterTool{
		Tool: tool,
		Schema: llm.ToolSchema{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  params,
		},
	}
	return nil
}

/* Schemas use names to keep final result in same seq*/
func (m *Manager) Schemas() []llm.ToolSchema {
	names := make([]string, 0, len(m.Tool))
	for _, t := range m.Tool {
		names = append(names, t.Schema.Name)
	}
	sort.Strings(names)
	out := make([]llm.ToolSchema, 0, len(names))
	for _, name := range names {
		out = append(out, m.Tool[name].Schema)
	}
	return out
}

func (m *Manager) Execute(ctx context.Context, name, args string) (string, error) {
	t, ok := m.Tool[name]
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	return t.Tool.Execute(ctx, args)
}
