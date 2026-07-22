package config

import (
	"errors"
	"fmt"
	"os"
)

/* this	function ensure config param is validate. */
func (c Config) Validate() error {
	if c.Agent.Model == "" {
		return errors.New("QEMU_AGENT_MODEL is empty")
	}
	if c.Agent.MaxTurns <= 0 {
		return fmt.Errorf("QEMU_AGENT_MAX_TURNS must be > 0, got %d", c.Agent.MaxTurns)
	}
	if c.Agent.MaxContextTokens <= 0 {
		return errors.New("QEMU_AGENT_MAX_CONTEXT_TOKENS must be > 0")
	}
	if c.Agent.KeepRecentTurns < 0 {
		return errors.New("QEMU_AGENT_KEEP_RECENT_TURNS must be >= 0")
	}
	if c.Tools.Timeout <= 0 {
		return errors.New("tool timeout must be > 0")
	}
	if c.Tools.MaxOutputBytes <= 0 {
		return errors.New("tool max output must be > 0")
	}
	if c.Tools.ReadMaxLines <= 0 {
		return errors.New("read max lines must be > 0")
	}

	switch c.Agent.Provider {
	case "openrouter":
		if c.OpenRouter.APIKey == "" {
			return errors.New("OPENROUTER_API_KEY is required")
		}
	case "openai":
		if c.OpenAI.APIKey == "" {
			return errors.New("OPENAI_API_KEY is required")
		}
	case "ollama":

	default:
		return fmt.Errorf("unsupported provider %q", c.Agent.Provider)
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported log level %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("unsupported log format %q", c.Log.Format)
	}

	info, err := os.Stat(c.Paths.Workspace)
	if err != nil {
		return fmt.Errorf("stat workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace %q is not a directory", c.Paths.Workspace)
	}
	return nil
}
