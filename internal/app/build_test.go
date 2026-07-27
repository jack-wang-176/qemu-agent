package app

import (
	"io"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/config"
)

func TestValidateBuildInputRejectsMissingAdapters(t *testing.T) {
	valid := BuildInput{
		LogOutput: io.Discard,
		CLI:       CLIAdapters{Input: &emptyReader{}, Output: io.Discard, ErrOutput: io.Discard},
	}
	tests := []BuildInput{
		{CLI: valid.CLI},
		{LogOutput: io.Discard, CLI: CLIAdapters{Output: io.Discard, ErrOutput: io.Discard}},
		{LogOutput: io.Discard, CLI: CLIAdapters{Input: &emptyReader{}, ErrOutput: io.Discard}},
		{LogOutput: io.Discard, CLI: CLIAdapters{Input: &emptyReader{}, Output: io.Discard}},
	}
	for _, input := range tests {
		if err := validateBuildInput(input); err == nil {
			t.Fatal("error = nil")
		}
	}
}

type emptyReader struct{}

func (*emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

func validBuildConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Agent:     config.AgentConfig{Provider: "ollama", Model: "model", MaxTurns: 1},
		Models:    config.ModelConfig{Definitions: []config.ModelDefinitionConfig{{Provider: "ollama", Name: "model", MaxContext: 4096, Tools: true}}},
		Context:   config.ContextConfig{MaxTokens: 1024},
		Paths:     config.PathConfig{DataDir: t.TempDir(), SessionDir: t.TempDir(), Workspace: t.TempDir()},
		Tools:     config.ToolConfig{Timeout: 1, MaxOutputBytes: 1, ReadMaxLines: 1},
		Log:       config.LogConfig{Level: "info", Format: "text"},
		Providers: config.ProviderConfig{Ollama: config.APIConfig{BaseURL: config.DefaultOllamaBaseURL}},
		Channel:   config.ChannelConfig{CLISessionKey: config.DefaultCLISessionKey, CLIPrompt: config.DefaultCLIPrompt, MaxInputBytes: config.DefaultMaxInputBytes},
		Security:  config.SecurityConfig{Mode: config.DefaultSecurityMode, AuditPath: t.TempDir() + "/tools.jsonl", ApprovalTimeout: config.DefaultApprovalTimeout, MaxAuditArgBytes: config.DefaultMaxAuditArgBytes, MaxAuditOutBytes: config.DefaultMaxAuditOutBytes},
	}
}

func TestBuildCreatesCLIChannel(t *testing.T) {
	runtime, err := Build(BuildInput{
		Config: validBuildConfig(t), SystemPrompt: "system", LogOutput: io.Discard,
		CLI: CLIAdapters{Input: &emptyReader{}, Output: io.Discard, ErrOutput: io.Discard},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Channels) != 1 || runtime.Channels[0].Name() != "cli" {
		t.Fatalf("channels = %#v", runtime.Channels)
	}
}
