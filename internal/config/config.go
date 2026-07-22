package config

import (
	"os"
	"time"
)

type LookEnv func(string) (string, bool)

/* global env config, contain different model in. */
type Config struct {
	Agent      AgentConfig
	Paths      PathConfig
	Tools      ToolConfig
	Log        LogConfig
	OpenRouter APIConfig
	OpenAI     APIConfig
	Ollama     APIConfig
}

type AgentConfig struct {
	Provider         string
	Model            string
	MaxTurns         int
	MaxContextTokens int
	KeepRecentTurns  int
	Stream           bool
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

func LoadFromOS(overrides Overrides) (Config, error) {
	return Load(os.LookupEnv, overrides)
}

func Load(lookup LookEnv, overrides Overrides) (Config, error) {
	cfg, err := LoadEnv(lookup)
	if err != nil {
		return Config{}, err
	}
	return cfg.WithOverrides(overrides)
}
