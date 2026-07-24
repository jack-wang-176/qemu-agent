package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestExplicitZeroMaxTurnsProducesOverride(t *testing.T) {
	parsed, visited, err := parseFlags([]string{"-p", "hello", "-max-turns=0"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	overrides := parsed.Overrides(visited)
	if overrides.MaxTurns == nil || *overrides.MaxTurns != 0 {
		t.Fatalf("MaxTurns override = %#v, want explicit zero", overrides.MaxTurns)
	}
}

func configureTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("QEMU_AGENT_PROVIDER", "ollama")
	t.Setenv("QEMU_AGENT_WORKSPACE", t.TempDir())
	t.Setenv("QEMU_AGENT_DATA_DIR", t.TempDir())
}

func TestRunWithIOOneShotCommand(t *testing.T) {
	configureTestEnvironment(t)
	var output, errOutput bytes.Buffer
	code := runWithIO([]string{"-p", "/help"}, processIO{
		In: strings.NewReader(""), Out: &output, Err: &errOutput,
	})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOutput.String())
	}
	if !strings.Contains(output.String(), "/help") {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestRunWithIOInteractiveExit(t *testing.T) {
	configureTestEnvironment(t)
	var output, errOutput bytes.Buffer
	code := runWithIO(nil, processIO{
		In: strings.NewReader("/exit\n"), Out: &output, Err: &errOutput,
	})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOutput.String())
	}
	if !strings.Contains(output.String(), "> ") {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestRunWithIORejectsExplicitEmptyPrompt(t *testing.T) {
	var errOutput bytes.Buffer
	code := runWithIO([]string{"-p", ""}, processIO{
		In: strings.NewReader(""), Out: io.Discard, Err: &errOutput,
	})
	if code != 2 || !strings.Contains(errOutput.String(), "must not be empty") {
		t.Fatalf("code = %d, stderr = %q", code, errOutput.String())
	}
}
