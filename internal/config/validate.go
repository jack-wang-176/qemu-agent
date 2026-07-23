package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

var ErrNilEnvironmentLookup = errors.New("environment lookup is nil")

func (c Config) Validate() error {
	if strings.TrimSpace(c.Agent.Provider) == "" {
		return errors.New("QEMU_AGENT_PROVIDER is empty")
	}
	if strings.TrimSpace(c.Agent.Model) == "" {
		return errors.New("QEMU_AGENT_MODEL is empty")
	}
	if c.Agent.MaxTurns <= 0 {
		return fmt.Errorf("QEMU_AGENT_MAX_TURNS must be > 0, got %d", c.Agent.MaxTurns)
	}
	if c.Context.MaxTokens <= 0 {
		return errors.New("QEMU_AGENT_MAX_CONTEXT_TOKENS must be > 0")
	}
	if c.Context.KeepRecentTurns < 0 {
		return errors.New("QEMU_AGENT_KEEP_RECENT_TURNS must be >= 0")
	}
	if c.Agent.Stream {
		return errors.New("QEMU_AGENT_STREAM=true is not supported yet")
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
		if c.Providers.OpenRouter.APIKey == "" {
			return errors.New("OPENROUTER_API_KEY is required")
		}
		if err := validateBaseURL("OPENROUTER_BASE_URL", c.Providers.OpenRouter.BaseURL); err != nil {
			return err
		}
	case "openai":
		if c.Providers.OpenAI.APIKey == "" {
			return errors.New("OPENAI_API_KEY is required")
		}
		if err := validateBaseURL("OPENAI_BASE_URL", c.Providers.OpenAI.BaseURL); err != nil {
			return err
		}
	case "ollama":
		if err := validateBaseURL("OLLAMA_BASE_URL", c.Providers.Ollama.BaseURL); err != nil {
			return err
		}
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

func validateBaseURL(name, raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", name)
	}
	return nil
}
