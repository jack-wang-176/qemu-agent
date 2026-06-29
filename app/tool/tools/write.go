package tools

import (
	"encoding/json"
	"os"

	openai "github.com/openai/openai-go/v3"
)

type WriteToolParam struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type WriteTool struct {
}

func (wt *WriteTool) Name() string {
	return "write"
}
func (wt *WriteTool) Build() openai.ChatCompletionToolUnionParam {
	params := openai.FunctionParameters{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]string{
				"type":        "string",
				"description": "The path of the file to write",
			},
			"content": map[string]string{
				"type":        "string",
				"description": "The content to write into the file",
			},
		},
		"required": []string{"file_path", "content"},
	}
	return openai.ChatCompletionFunctionTool(
		openai.FunctionDefinitionParam{
			Name:        wt.Name(),
			Parameters:  params,
			Description: openai.String("Write a file to the system"),
		},
	)
}
func (wt *WriteTool) Execute(args string) (string, error) {
	var arg WriteToolParam
	if err := json.Unmarshal([]byte(args), &arg); err != nil {
		return "", err
	}
	if err := os.WriteFile(arg.FilePath, []byte(arg.Content), 0644); err != nil {
		return "", err
	}
	return "File written successfully", nil
}
