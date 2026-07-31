package main

import (
	"io"
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
