package tool

import (
	"fmt"

	openai "github.com/openai/openai-go/v3"
)

type Manager struct {
	Tool map[string]Tool
}

func NewManager() *Manager {
	return &Manager{
		Tool: make(map[string]Tool),
	}
}

func (m *Manager) Register(tool Tool) {
	m.Tool[tool.Name()] = tool
}

func (m *Manager) BuildTools() []openai.ChatCompletionToolUnionParam {
	tools := make([]openai.ChatCompletionToolUnionParam, 0)
	for _, tool := range m.Tool {
		tools = append(tools, tool.Build())
	}
	return tools
}
func (m *Manager) ExecuteTool(name string, args string) (string, error) {
	if tool, ok := m.Tool[name]; ok {
		return tool.Execute(args)
	} else {
		return "", fmt.Errorf("tool %s not found", name)
	}
}
