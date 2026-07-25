package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
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
func parseFlags(args []string, output io.Writer) (flags, map[string]bool, error) {
	set := flag.NewFlagSet("qemu-agent", flag.ContinueOnError)
	set.SetOutput(output)
	var result flags
	set.StringVar(&result.Prompt, "p", "", "prompt to send to the agent")
	set.StringVar(&result.Provider, "provider", "", "override provider")
	set.StringVar(&result.Model, "model", "", "override model")
	set.IntVar(&result.MaxTurns, "max-turns", 0, "override maximum turns")
	if err := set.Parse(args); err != nil {
		return flags{}, nil, err
	}
	visited := make(map[string]bool)
	set.Visit(func(item *flag.Flag) {
		visited[item.Name] = true
	})
	return result, visited, nil
}

/* main supposed to be very simple. */
func main() {
	os.Exit(run(os.Args[1:]))
}

type processIO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

/* run initialize certain run behavior*/
func run(args []string) int {
	return runWithIO(args, processIO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
}

func runWithIO(args []string, streams processIO) int {
	flags, visited, err := parseFlags(args, streams.Err)
	if err != nil {
		return 2
	}
	if visited["p"] && strings.TrimSpace(flags.Prompt) == "" {
		fmt.Fprintln(streams.Err, "-p prompt must not be empty")
		return 2
	}

	/* load config initializing config behavior/ */
	cfg, err := config.LoadFromOS(flags.Overrides(visited))
	if err != nil {
		fmt.Fprintln(streams.Err, err)
		return 2
	}

	/* self-made build injection. */
	runtime, err := app.Build(app.BuildInput{
		Config:       cfg,
		SystemPrompt: systemPrompt,
		LogOutput:    streams.Err,
		CLI: app.CLIAdapters{
			Input:     streams.In,
			Output:    streams.Out,
			ErrOutput: streams.Err,
		},
	})
	if err != nil {
		fmt.Fprintln(streams.Err, err)
		return 1
	}
	defer runtime.Close()

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if visited["p"] {
		answer, err := runtime.Application.RunOnce(
			ctx,
			newTraceID(),
			flags.Prompt,
		)
		if err != nil {
			runtime.Logger.Error("run prompt", "err", err)
			return 1
		}
		if answer != "" {
			fmt.Fprintln(streams.Out, answer)
		}
		return 0
	}

	if err := runtime.Run(ctx); err != nil {
		runtime.Logger.Error("run runtime", "err", err)
		return 1
	}
	return 0
}

func (f flags) Overrides(visited map[string]bool) config.Overrides {
	var result config.Overrides
	if visited["provider"] {
		result.Provider = &f.Provider
	}
	if visited["model"] {
		result.Model = &f.Model
	}
	if visited["max-turns"] {
		result.MaxTurns = &f.MaxTurns
	}
	return result
}

func newTraceID() string {
	return "cli-" + uuid.NewString()
}
