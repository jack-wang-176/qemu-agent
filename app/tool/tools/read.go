package tools

import (
	"encoding/json"
	"os"

	openai "github.com/openai/openai-go/v3"
)

type ReadToolParam struct {
	FilePath string `json:"file_path"`
}
type ReadTool struct {
}

func (rt *ReadTool) Name() string {
	return "read"
}

func (rt *ReadTool) Build() openai.ChatCompletionToolUnionParam {
	params := openai.FunctionParameters{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]string{
				"type":        "string",
				"description": "The path of the file to read",
			},
		},
		"required": []string{"file_path"},
	}
	return openai.ChatCompletionFunctionTool(
		openai.FunctionDefinitionParam{
			Name:        rt.Name(),
			Parameters:  params,
			Description: openai.String("Read a file from the system"),
		},
	)
}

func (rt *ReadTool) Execute(filepath string) (string, error) {
	var arg ReadToolParam
	if err := json.Unmarshal([]byte(filepath), &arg); err != nil {
		return "", err
	}
	data, err := os.ReadFile(arg.FilePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
