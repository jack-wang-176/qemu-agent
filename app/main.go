package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jack-wang-176/qemu-agent/app/contextmgr"
	"github.com/jack-wang-176/qemu-agent/app/contextmgr/compactors"
	"github.com/jack-wang-176/qemu-agent/app/pkg"
	"github.com/jack-wang-176/qemu-agent/app/session"
	"github.com/jack-wang-176/qemu-agent/app/tool"
	"github.com/jack-wang-176/qemu-agent/app/tool/tools"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

//go:embed prompts/system.md
var SystemPrompt string

var MaxCall int = 15

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var prompt string
	flag.StringVar(&prompt, "p", "", "Prompt to send to LLM")
	flag.Parse()

	if prompt == "" {
		panic("Prompt must not be empty")
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	baseUrl := os.Getenv("OPENROUTER_BASE_URL")
	model := os.Getenv("OPENROUTER_MODEL_NAME")
	if baseUrl == "" {
		baseUrl = "https://openrouter.ai/api/v1"
	}

	if apiKey == "" {
		panic("Env variable OPENROUTER_API_KEY not found")
	}

	//todo 添加traceid追踪实例
	session := session.NewSession("todo", SystemPrompt)
	session.AddUser(prompt)
	client := openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseUrl))
	manager := tool.NewManager()
	manager.Register(&tools.ReadTool{})
	manager.Register(&tools.WriteTool{})
	manager.Register(&tools.BashTool{})
	tokenizer, err := contextmgr.NewTokenizer("")
	if err != nil {
		log.Fatalf("Init tokenizer failed: %v", err)
	}
	llmCompactor := compactors.NewLLMSummarizer(&client, 4, "gpt-4o-mini")
	ctxManager := contextmgr.NewCompactorManager(160000, *tokenizer, llmCompactor)
	ToolCalls := manager.BuildTools()
	for i := 0; i < MaxCall; i++ {
		trimmed, used, err := ctxManager.EnforceBudget(ctx, model, session.Msg)
		if err != nil {
			log.Printf("compact warn: %v", err)
			continue
		}
		session.Msg = trimmed
		session.TokenUsage = used

		resp, err := pkg.AgentCallWithRetry(ctx, client, model, session.Msg, ToolCalls)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		session.AddChatResult(resp)
		if len(resp.Choices[0].Message.ToolCalls) == 0 {
			fmt.Print(resp.Choices[0].Message.Content)
			break
		}
		for _, toolCall := range resp.Choices[0].Message.ToolCalls {
			data, err := manager.ExecuteTool(toolCall.Function.Name, toolCall.Function.Arguments)
			if err != nil {
				data = fmt.Sprintf("%s\n[exit error] %v", data, err)
			}
			session.AddToolResult(data, toolCall.ID)
		}
	}
	fmt.Fprintln(os.Stderr, "reached max turns, aborting")
}
