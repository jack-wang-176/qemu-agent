package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jack-wang-176/qemu-agent/app/tool"
	"github.com/jack-wang-176/qemu-agent/app/tool/tools"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func main() {
	var prompt string
	flag.StringVar(&prompt, "p", "", "Prompt to send to LLM")
	flag.Parse()

	if prompt == "" {
		panic("Prompt must not be empty")
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	baseUrl := os.Getenv("OPENROUTER_BASE_URL")
	if baseUrl == "" {
		baseUrl = "https://openrouter.ai/api/v1"
	}

	if apiKey == "" {
		panic("Env variable OPENROUTER_API_KEY not found")
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(prompt),
	}
	client := openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseUrl))
	manager := tool.NewManager()
	manager.Register(&tools.ReadTool{})
	manager.Register(&tools.WriteTool{})
	manager.Register(&tools.BashTool{})
	ToolCalls := manager.BuildTools()
	for {
		resp, err := client.Chat.Completions.New(context.Background(),
			openai.ChatCompletionNewParams{
				Model:    "anthropic/claude-haiku-4.5",
				Messages: messages,
				Tools:    ToolCalls,
			},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		var saParam = resp.Choices[0].Message
		messages = append(messages, saParam.ToParam())
		if len(resp.Choices[0].Message.ToolCalls) == 0 {
			fmt.Print(saParam.Content)
			break
		}
		for _, toolCall := range resp.Choices[0].Message.ToolCalls {
			data, err := manager.ExecuteTool(toolCall.Function.Name, toolCall.Function.Arguments)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error executing tool: %v\n", err)
			}
			messages = append(messages, openai.ToolMessage(data, toolCall.ID))
		}
	}
}
