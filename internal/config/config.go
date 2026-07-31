package config

import (
	"os"
	"time"
)

type LookupEnv func(string) (string, bool)

// Config is the immutable application configuration assembled at startup.
// It contains values only; runtime services are created by internal/app.
type Config struct {
	Agent     AgentConfig
	Context   ContextConfig
	Paths     PathConfig
	Tools     ToolConfig
	Log       LogConfig
	Providers ProviderConfig
}

type AgentConfig struct {
	Provider string
	Model    string
	MaxTurns int
	Stream   bool
}

type ContextConfig struct {
	MaxTokens       int
	KeepRecentTurns int
}

type PathConfig struct {
	DataDir    string
	SessionDir string
	Workspace  string
}

type ToolConfig struct {
	Timeout        time.Duration
	MaxOutputBytes int
	ReadMaxLines   int
}

type LogConfig struct {
	Level  string
	Format string
}

type APIConfig struct {
	BaseURL string
	APIKey  string
}

type ProviderConfig struct {
	OpenRouter APIConfig
	OpenAI     APIConfig
	Ollama     APIConfig
}

func LoadFromOS(overrides Overrides) (Config, error) {
	return Load(os.LookupEnv, overrides)
}

func Load(lookup LookupEnv, overrides Overrides) (Config, error) {
	if lookup == nil {
		return Config{}, ErrNilEnvironmentLookup
	}
	cfg, err := LoadEnv(lookup)
	if err != nil {
		return Config{}, err
	}
	return cfg.WithOverrides(overrides)
}
