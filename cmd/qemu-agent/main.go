package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jack-wang-176/qemu-agent/internal/agent"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/session"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
	"github.com/jack-wang-176/qemu-agent/internal/tools/builtin"
)

//go:embed system.md
var systemPrompt string

func main() {
	prompt := flag.String("p", "", "prompt to send to the agent")
	maxTurns := flag.Int("max-turns", 15, "maximum agent turns")
	flag.Parse()
	if *prompt == "" {
		log.Fatal("-p prompt is required")
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENROUTER_API_KEY is required")
	}
	baseURL := os.Getenv("OPENROUTER_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	model := os.Getenv("OPENROUTER_MODEL_NAME")
	if model == "" {
		model = "openai/gpt-4o-mini"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	provider := llm.NewOpenAIProvider("openrouter", apiKey, baseURL)
	manager := tools.NewManager()
	for _, t := range []tools.Tool{&builtin.ReadTool{}, &builtin.WriteTool{}, &builtin.BashTool{}} {
		if err := manager.Register(t); err != nil {
			log.Fatalf("register tool: %v", err)
		}
	}
	tok, err := contextmgr.NewTokenizer(model)
	if err != nil {
		log.Fatalf("create tokenizer: %v", err)
	}
	compactor := contextmgr.NewLLMSummarizer(provider, 4, model)
	cm := contextmgr.NewCompactorManager(160000, *tok, compactor)
	storeDir := filepath.Join(os.TempDir(), "qemu-agent", "sessions")
	store := session.NewFileStore(storeDir)
	ag, err := agent.New(provider, manager, &cm, store, model, *maxTurns)
	if err != nil {
		log.Fatalf("create agent: %v", err)
	}
	sess := session.NewSession("cli", systemPrompt, model)
	answer, err := ag.Run(ctx, sess, *prompt)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(answer)
}
