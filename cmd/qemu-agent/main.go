package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"

	"os"
	"os/signal"
	"syscall"

	"github.com/jack-wang-176/qemu-agent/internal/app"
	"github.com/jack-wang-176/qemu-agent/internal/config"
)

//go:embed system.md
var systemPrompt string

type flags struct {
	Prompt   string
	Provider string
	Model    string
	MaxTurns int
}

/* use flag input to parse flag struct. */
func parseFlags() flags {
	prompt := flag.String("p", "", "prompt to send to the agent")
	provider := flag.String("provider", "", "override provider")
	model := flag.String("model", "", "override model")
	maxTurns := flag.Int("max-turns", 0, "override maximum turns")
	flag.Parse()
	return flags{
		Prompt:   *prompt,
		Provider: *provider,
		Model:    *model,
		MaxTurns: *maxTurns,
	}
}

/* main supposed to be very simple. */
func main() {
	os.Exit(run())
}

/* run initialize certain run behavior*/
func run() int {
	flags := parseFlags()
	if flags.Prompt == "" {
		fmt.Fprintln(os.Stderr, "-p prompt is required")
		return 2
	}

	/* load config initializing config behavior/ */
	cfg, err := config.LoadFromOS(flags.Overrides())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	/* self-made build injection. */
	runtime, err := app.Build(app.BuildInput{
		Config:       cfg,
		SystemPrompt: systemPrompt,
		LogOutput:    os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer runtime.Close()

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	answer, err := runtime.Application.RunOnce(
		ctx,
		"cli:oneshot",
		flags.Prompt,
	)
	if err != nil {
		runtime.Logger.Error("run prompt", "err", err)
		return 1
	}

	fmt.Fprint(os.Stdout, answer)
	return 0
}

func (f flags) Overrides() config.Overrides {
	var result config.Overrides
	if f.Provider != "" {
		result.Provider = &f.Provider
	}
	if f.Model != "" {
		result.Model = &f.Model
	}
	if f.MaxTurns != 0 {
		result.MaxTurns = &f.MaxTurns
	}
	return result
}
