package config

import (
	"fmt"
	"strings"
	"testing"
)

func lookupFrom(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadAppliesOverrideBeforeProviderValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	provider := "ollama"
	cfg, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_WORKSPACE": t.TempDir(),
	}), Overrides{Provider: &provider})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Agent.Provider != "ollama" {
		t.Fatalf("provider = %q, want ollama", cfg.Agent.Provider)
	}
}

func TestLoadRejectsExplicitZeroMaxTurns(t *testing.T) {
	maxTurns := 0
	_, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":  "ollama",
		"QEMU_AGENT_WORKSPACE": t.TempDir(),
	}), Overrides{MaxTurns: &maxTurns})
	if err == nil || !strings.Contains(err.Error(), "MAX_TURNS") {
		t.Fatalf("Load() error = %v, want max turns error", err)
	}
}

func TestSummaryDoesNotExposeAPIKeys(t *testing.T) {
	cfg := Config{
		Agent: AgentConfig{Provider: "openrouter", Model: "model"},
		Providers: ProviderConfig{
			OpenRouter: APIConfig{APIKey: "secret-value"},
		},
	}
	if strings.Contains(fmt.Sprintf("%+v", cfg.Summary()), "secret-value") {
		t.Fatal("Summary exposed API key")
	}
}
