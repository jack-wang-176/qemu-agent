package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

var ErrNilEnvironmentLookup = errors.New("environment lookup is nil")

func (c Config) Validate() error {
	switch c.Security.Mode {
	case "allow", "deny-dangerous", "ask-dangerous":
	default:
		return fmt.Errorf("unsupported tool security mode %q", c.Security.Mode)
	}
	if strings.TrimSpace(c.Security.AuditPath) == "" {
		return errors.New("tool audit path is empty")
	}
	if c.Security.ApprovalTimeout <= 0 {
		return errors.New("tool approval timeout must be > 0")
	}
	if c.Security.MaxAuditArgBytes <= 0 || c.Security.MaxAuditOutBytes <= 0 {
		return errors.New("tool audit limits must be > 0")
	}
	// context config validate
	if c.Context.KeepRecentTurns < 0 {
		return errors.New("QEMU_AGENT_KEEP_RECENT_TURNS must be >= 0")
	}
	if c.Context.MaxTokens <= 0 {
		return errors.New("QEMU_AGENT_MAX_CONTEXT_TOKENS must be > 0")
	}
	// tool config validate
	if c.Tools.Timeout <= 0 {
		return errors.New("tool timeout must be > 0")
	}
	if c.Tools.MaxOutputBytes <= 0 {
		return errors.New("tool max output must be > 0")
	}
	if c.Tools.ReadMaxLines <= 0 {
		return errors.New("read max lines must be > 0")
	}
	// agent config validate
	if strings.TrimSpace(c.Agent.Provider) == "" {
		return errors.New("QEMU_AGENT_PROVIDER is empty")
	}
	if strings.TrimSpace(c.Agent.Model) == "" {
		return errors.New("QEMU_AGENT_MODEL is empty")
	}
	if c.Agent.MaxTurns <= 0 {
		return fmt.Errorf("QEMU_AGENT_MAX_TURNS must be > 0, got %d", c.Agent.MaxTurns)
	}
	if c.Agent.Stream {
		return errors.New("QEMU_AGENT_STREAM=true is not supported yet")
	}
	if len(c.Models.Definitions) == 0 {
		return errors.New("no models configured")
	}
	defaultKey := strings.ToLower(strings.TrimSpace(c.Agent.Provider)) + ":" + strings.TrimSpace(c.Agent.Model)
	refs := make(map[string]struct{}, len(c.Models.Definitions))
	aliases := make(map[string]struct{})
	for index, def := range c.Models.Definitions {
		provider := strings.ToLower(strings.TrimSpace(def.Provider))
		name := strings.TrimSpace(def.Name)
		if provider == "" || name == "" {
			return fmt.Errorf("model definition %d provider/name is empty", index)
		}
		if strings.ContainsAny(provider, ":/ \t\r\n") {
			return fmt.Errorf("model definition %d has invalid provider %q", index, def.Provider)
		}
		if def.MaxContext <= 0 {
			return fmt.Errorf("model %q max context must be > 0", provider+":"+name)
		}
		if def.MaxOutput < 0 || def.MaxOutput >= def.MaxContext {
			return fmt.Errorf("model %q max output must be >= 0 and < max context", provider+":"+name)
		}
		key := provider + ":" + name
		if _, exists := refs[key]; exists {
			return fmt.Errorf("duplicate model definition %q", key)
		}
		refs[key] = struct{}{}
		for _, rawAlias := range def.Aliases {
			alias := strings.ToLower(strings.TrimSpace(rawAlias))
			if alias == "" || strings.ContainsAny(alias, " :/\t\r\n") {
				return fmt.Errorf("model %q has invalid alias %q", key, rawAlias)
			}
			if _, exists := aliases[alias]; exists {
				return fmt.Errorf("duplicate model alias %q", alias)
			}
			aliases[alias] = struct{}{}
		}
	}
	if _, exists := refs[defaultKey]; !exists {
		return fmt.Errorf("default model %q is not registered", defaultKey)
	}

	providers := make(map[string]struct{})
	for _, def := range c.Models.Definitions {
		providers[strings.ToLower(strings.TrimSpace(def.Provider))] = struct{}{}
	}
	for provider := range providers {
		switch provider {
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
			return fmt.Errorf("unsupported provider %q", provider)
		}
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported log level %q", c.Log.Level)
	}
	// log config validate
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("unsupported log format %q", c.Log.Format)
	}

	// channel config validate
	if !c.Channel.CLIEnabled && !c.Channel.Telegram.Enabled {
		return errors.New("at least one channel must be enabled")
	}
	if c.Channel.CLIEnabled && strings.TrimSpace(c.Channel.CLISessionKey) == "" {
		return errors.New("QEMU_AGENT_CLI_SESSION_KEY is empty")
	}
	if c.Channel.CLIEnabled && !strings.HasPrefix(c.Channel.CLISessionKey, "cli:") {
		return fmt.Errorf("QEMU_AGENT_CLI_SESSION_KEY must start with cli:, got %q", c.Channel.CLISessionKey)
	}
	if c.Channel.CLIEnabled && c.Channel.CLIPrompt == "" {
		return errors.New("QEMU_AGENT_CLI_PROMPT is empty")
	}
	if c.Channel.CLIEnabled && strings.ContainsRune(c.Channel.CLIPrompt, '\x00') {
		return errors.New("QEMU_AGENT_CLI_PROMPT contains NUL")
	}
	if c.Channel.CLIEnabled && (c.Channel.MaxInputBytes <= 0 || c.Channel.MaxInputBytes > MaxCLIInputBytes) {
		return fmt.Errorf(
			"QEMU_AGENT_CLI_MAX_INPUT_BYTES must be between 1 and %d",
			MaxCLIInputBytes,
		)
	}
	if c.Channel.Telegram.Enabled {
		tg := c.Channel.Telegram
		if strings.TrimSpace(tg.Token) == "" {
			return errors.New("telegram token is empty")
		}
		if len(tg.AllowedUserIDs) == 0 {
			return errors.New("telegram allowed user ids are empty")
		}
		if tg.PollTimeout < time.Second || tg.PollTimeout > 50*time.Second {
			return errors.New("telegram poll timeout must be between 1s and 50s")
		}
		if tg.RetryMinBackoff <= 0 || tg.RetryMaxBackoff < tg.RetryMinBackoff {
			return errors.New("telegram retry backoff is invalid")
		}
		if tg.MaxConcurrency < 1 || tg.MaxConcurrency > 64 {
			return errors.New("telegram max concurrency must be between 1 and 64")
		}
		if tg.MaxInputBytes <= 0 {
			return errors.New("telegram max input bytes must be > 0")
		}
		if tg.EditInterval < 250*time.Millisecond {
			return errors.New("telegram edit interval must be at least 250ms")
		}
		if tg.MessageChunkSize < 1 || tg.MessageChunkSize > 4096 {
			return errors.New("telegram message chunk size must be between 1 and 4096")
		}
		seen := make(map[int64]struct{}, len(tg.AllowedUserIDs))
		for _, id := range tg.AllowedUserIDs {
			if id <= 0 {
				return errors.New("telegram allowed user id must be positive")
			}
			if _, exists := seen[id]; exists {
				return fmt.Errorf("duplicate telegram allowed user id %d", id)
			}
			seen[id] = struct{}{}
		}
	}

	// path fetch
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
