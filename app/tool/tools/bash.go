package tools

import (
	"encoding/json"
	"fmt"
	"os/exec"

	openai "github.com/openai/openai-go/v3"
)

type BashToolParam struct {
	Command string `json:"command"`
}
type BashTool struct{}

func (bt *BashTool) Name() string {
	return "bash"
}
func (bt *BashTool) Build() openai.ChatCompletionToolUnionParam {
	params := openai.FunctionParameters{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]string{
				"type":        "string",
				"description": "The bash command to execute",
			},
		},
		"required": []string{"command"},
	}
	return openai.ChatCompletionFunctionTool(
		openai.FunctionDefinitionParam{
			Name:        bt.Name(),
			Parameters:  params,
			Description: openai.String("Execute a bash command on the system"),
		},
	)
}
func (bt *BashTool) Execute(args string) (string, error) {
	var arg BashToolParam
	if err := json.Unmarshal([]byte(args), &arg); err != nil {
		return "", err
	}
	cmd := exec.Command("bash", "-c", arg.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("%s\n[exit error] %v", output, err), nil
	}
	return string(output), nil
}
