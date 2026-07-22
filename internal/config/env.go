package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

/* below function parse env into project*/
func envString(lookup LookEnv, key, fallback string) string {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func envInt(lookup LookEnv, key string, fallback int) (int, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	result, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q as integer: %w", key, raw, err)
	}
	return result, nil
}

func envBool(lookup LookEnv, key string, fallback bool) (bool, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	result, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("parse %s=%q as boolean: %w", key, raw, err)
	}
	return result, nil
}

func envDuration(lookup LookEnv, key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q as duration: %w", key, raw, err)
	}
	return value, nil
}

func envInt64List(lookup LookEnv, key string) ([]int64, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	result := make([]int64, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		value, err := strconv.ParseInt(item, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s item %q as int64: %w", key, item, err)
		}
		result = append(result, value)
	}
	return result, nil
}

/* this funciton resolve file path. */
func resolveDataDir(lookup LookEnv) (string, error) {
	if value := envString(lookup, "QEMU_AGENT_DATA_DIR", ""); value != "" {
		/* insurance the file path is absolute path. */
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("resolve data dir: %w", err)
		}
		return filepath.Clean(absolute), nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config dir: %w", err)
	}
	return filepath.Join(root, "qemu-agent"), nil
}

func resolveWorkspace(lookup LookEnv) (string, error) {
	value := envString(lookup, "QEMU_AGENT_WORKSPACE", "")
	if value == "" {
		current, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
		value = current
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return filepath.Clean(absolute), nil
}

/* LoadEnv build a comprehensive config and return. this stage do not consider override. */
func LoadEnv(lookup LookEnv) (Config, error) {
	maxTurns, err := envInt(lookup, "QEMU_AGENT_MAX_TURNS", DefaultMaxTurns)
	if err != nil {
		return Config{}, err
	}
	maxContext, err := envInt(
		lookup, "QEMU_AGENT_MAX_CONTEXT_TOKENS", DefaultMaxContextTokens,
	)
	if err != nil {
		return Config{}, err
	}
	keepRecent, err := envInt(
		lookup, "QEMU_AGENT_KEEP_RECENT_TURNS", DefaultKeepRecentTurns,
	)
	if err != nil {
		return Config{}, err
	}
	stream, err := envBool(lookup, "QEMU_AGENT_STREAM", true)
	if err != nil {
		return Config{}, err
	}
	toolTimeout, err := envDuration(
		lookup, "QEMU_AGENT_TOOL_TIMEOUT", DefaultToolTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	maxOutput, err := envInt(
		lookup, "QEMU_AGENT_TOOL_MAX_OUTPUT_BYTES", DefaultToolMaxOutput,
	)
	if err != nil {
		return Config{}, err
	}
	readMaxLines, err := envInt(
		lookup, "QEMU_AGENT_READ_MAX_LINES", DefaultReadMaxLines,
	)
	if err != nil {
		return Config{}, err
	}

	model := envString(lookup, "QEMU_AGENT_MODEL", "")
	if model == "" {
		model = envString(lookup, "OPENROUTER_MODEL_NAME", DefaultModel)
	}

	dataDir, err := resolveDataDir(lookup)
	if err != nil {
		return Config{}, err
	}
	workspace, err := resolveWorkspace(lookup)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Agent: AgentConfig{
			Provider:         envString(lookup, "QEMU_AGENT_PROVIDER", DefaultProvider),
			Model:            model,
			MaxTurns:         maxTurns,
			MaxContextTokens: maxContext,
			KeepRecentTurns:  keepRecent,
			Stream:           stream,
		},
		Paths: PathConfig{
			DataDir:    dataDir,
			SessionDir: filepath.Join(dataDir, "sessions"),
			Workspace:  workspace,
		},
		Tools: ToolConfig{
			Timeout:        toolTimeout,
			MaxOutputBytes: maxOutput,
			ReadMaxLines:   readMaxLines,
		},
		Log: LogConfig{
			Level:  envString(lookup, "QEMU_AGENT_LOG_LEVEL", "info"),
			Format: envString(lookup, "QEMU_AGENT_LOG_FORMAT", "text"),
		},
		OpenRouter: APIConfig{
			APIKey: envString(lookup, "OPENROUTER_API_KEY", ""),
			BaseURL: envString(
				lookup, "OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1",
			),
		},
		OpenAI: APIConfig{
			APIKey: envString(lookup, "OPENAI_API_KEY", ""),
			BaseURL: envString(
				lookup, "OPENAI_BASE_URL", "https://api.openai.com/v1",
			),
		},
		Ollama: APIConfig{
			BaseURL: envString(
				lookup, "OLLAMA_BASE_URL", "http://localhost:11434/v1",
			),
		},
	}

	return cfg, nil
}
