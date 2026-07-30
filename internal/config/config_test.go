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
		Channel: ChannelConfig{Telegram: TelegramConfig{Token: "telegram-secret"}},
	}
	summary := fmt.Sprintf("%+v", cfg.Summary())
	if strings.Contains(summary, "secret-value") || strings.Contains(summary, "telegram-secret") {
		t.Fatal("Summary exposed a credential")
	}
}

func TestLoadPopulatesTelegramConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER": "ollama", "QEMU_AGENT_WORKSPACE": t.TempDir(),
		"QEMU_AGENT_CLI_ENABLED": "false", "QEMU_AGENT_TELEGRAM_ENABLED": "true",
		"QEMU_AGENT_TELEGRAM_TOKEN": "token", "QEMU_AGENT_TELEGRAM_ALLOWED_USER_IDS": "20,10,20",
	}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channel.CLIEnabled || !cfg.Channel.Telegram.Enabled {
		t.Fatalf("channel=%#v", cfg.Channel)
	}
	got := cfg.Channel.Telegram.AllowedUserIDs
	if len(got) != 2 || got[0] != 20 || got[1] != 10 {
		t.Fatalf("allowed=%v", got)
	}
}

func TestLoadPopulatesCLIConfigDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":  "ollama",
		"QEMU_AGENT_WORKSPACE": t.TempDir(),
	}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channel.CLISessionKey != DefaultCLISessionKey {
		t.Fatalf("session key = %q", cfg.Channel.CLISessionKey)
	}
	if cfg.Channel.CLIPrompt != DefaultCLIPrompt {
		t.Fatalf("prompt = %q", cfg.Channel.CLIPrompt)
	}
	if cfg.Channel.MaxInputBytes != DefaultMaxInputBytes {
		t.Fatalf("max input bytes = %d", cfg.Channel.MaxInputBytes)
	}
}

func TestLoadPopulatesCLIConfigFromEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":            "ollama",
		"QEMU_AGENT_WORKSPACE":           t.TempDir(),
		"QEMU_AGENT_CLI_SESSION_KEY":     "cli:project",
		"QEMU_AGENT_CLI_PROMPT":          "agent>",
		"QEMU_AGENT_CLI_MAX_INPUT_BYTES": "4096",
	}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channel.CLISessionKey != "cli:project" || cfg.Channel.CLIPrompt != "agent>" || cfg.Channel.MaxInputBytes != 4096 {
		t.Fatalf("channel config = %#v", cfg.Channel)
	}
}

func TestChannelConfigValidation(t *testing.T) {
	base := Config{
		Agent:     AgentConfig{Provider: "ollama", Model: "model", MaxTurns: 1},
		Models:    ModelConfig{Definitions: []ModelDefinitionConfig{{Provider: "ollama", Name: "model", MaxContext: 4096, Tools: true}}},
		Context:   ContextConfig{MaxTokens: 1},
		Paths:     PathConfig{Workspace: t.TempDir()},
		Tools:     ToolConfig{Timeout: 1, MaxOutputBytes: 1, ReadMaxLines: 1},
		Log:       LogConfig{Level: "info", Format: "text"},
		Providers: ProviderConfig{Ollama: APIConfig{BaseURL: DefaultOllamaBaseURL}},
		Channel:   ChannelConfig{CLIEnabled: true, CLISessionKey: DefaultCLISessionKey, CLIPrompt: DefaultCLIPrompt, MaxInputBytes: DefaultMaxInputBytes},
		Security:  SecurityConfig{Mode: DefaultSecurityMode, AuditPath: t.TempDir() + "/tools.jsonl", ApprovalTimeout: DefaultApprovalTimeout, MaxAuditArgBytes: DefaultMaxAuditArgBytes, MaxAuditOutBytes: DefaultMaxAuditOutBytes},
		Prompt:    PromptConfig{ReservedContextTokens: DefaultPromptReservedTokens, MaxInjectedBytes: DefaultPromptMaxBytes, MaxMemoryItems: DefaultPromptMaxMemoryItems},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("base config must be valid: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty key", mutate: func(cfg *Config) { cfg.Channel.CLISessionKey = "" }},
		{name: "wrong key prefix", mutate: func(cfg *Config) { cfg.Channel.CLISessionKey = "telegram:1" }},
		{name: "empty prompt", mutate: func(cfg *Config) { cfg.Channel.CLIPrompt = "" }},
		{name: "NUL prompt", mutate: func(cfg *Config) { cfg.Channel.CLIPrompt = ">\x00" }},
		{name: "zero size", mutate: func(cfg *Config) { cfg.Channel.MaxInputBytes = 0 }},
		{name: "oversized", mutate: func(cfg *Config) { cfg.Channel.MaxInputBytes = MaxCLIInputBytes + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
