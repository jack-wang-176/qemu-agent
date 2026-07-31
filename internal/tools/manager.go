package tools

import (
	"fmt"
	"sort"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

type Catalog interface {
	Schema() []llm.ToolSchema
	Lookup(name string) (Tool, bool)
}

type RegisterTool struct {
	Tool   Tool
	Schema llm.ToolSchema
}
type Manager struct {
	tools map[string]RegisterTool
}

func NewManager() *Manager {
	return &Manager{
		tools: make(map[string]RegisterTool),
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
	if _, ok := m.tools[tool.Name()]; ok {
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
	m.tools[tool.Name()] = RegisterTool{
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
	names := make([]string, 0, len(m.tools))
	for _, t := range m.tools {
		names = append(names, t.Schema.Name)
	}
	sort.Strings(names)
	out := make([]llm.ToolSchema, 0, len(names))
	for _, name := range names {
		out = append(out, m.tools[name].Schema)
	}
	return out
}

func (m *Manager) Lookup(name string) (Tool, bool) {
	register, ok := m.tools[name]
	if !ok {
		return nil, false
	}
	return register.Tool, true
}
