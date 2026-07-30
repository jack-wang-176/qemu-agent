package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func loadModelDefinitions(lookup LookupEnv, provider, model string, maxContext int, stream bool) ([]ModelDefinitionConfig, error) {
	raw, ok := lookup("QEMU_AGENT_MODELS_JSON")
	if ok && strings.TrimSpace(raw) != "" {
		var definitions []ModelDefinitionConfig
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&definitions); err != nil {
			return nil, fmt.Errorf("parse QEMU_AGENT_MODELS_JSON: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err == nil {
			return nil, fmt.Errorf("parse QEMU_AGENT_MODELS_JSON: extra JSON values")
		} else if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse trailing QEMU_AGENT_MODELS_JSON: %w", err)
		}
		return definitions, nil
	}
	return []ModelDefinitionConfig{{
		Provider:    provider,
		Name:        model,
		DisplayName: provider + ":" + model,
		MaxContext:  maxContext,
		Tools:       true,
		Streaming:   stream,
	}}, nil
}

/* below function parse env into project*/
func envString(lookup LookupEnv, key, fallback string) string {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func envInt(lookup LookupEnv, key string, fallback int) (int, error) {
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

func envBool(lookup LookupEnv, key string, fallback bool) (bool, error) {
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

func envDuration(lookup LookupEnv, key string, fallback time.Duration) (time.Duration, error) {
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

func envInt64List(lookup LookupEnv, key string) ([]int64, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := make(map[int64]struct{})
	result := make([]int64, 0)
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("parse %s user id %q as positive integer", key, part)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func resolveDataDir(lookup LookupEnv) (string, error) {
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

func resolveWorkspace(lookup LookupEnv) (string, error) {
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

// resolveDir returns an absolute clean directory for key, falling back to
// fallback when the variable is absent. The fallback is already derived from
// DataDir, which LoadEnv resolved to an absolute path.
func resolveDir(lookup LookupEnv, key, fallback string) (string, error) {
	value := envString(lookup, key, "")
	if value == "" {
		return filepath.Clean(fallback), nil
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", key, err)
	}
	return filepath.Clean(absolute), nil
}

// loadSkillConfig reads the Skill group. It never touches the filesystem:
// scanning belongs to skills.ScanRegistry during Build.
func loadSkillConfig(lookup LookupEnv, dataDir string) (SkillConfig, error) {
	enabled, err := envBool(lookup, "QEMU_AGENT_SKILLS_ENABLED", DefaultSkillsEnabled)
	if err != nil {
		return SkillConfig{}, err
	}
	dir, err := resolveDir(lookup, "QEMU_AGENT_SKILLS_DIR", filepath.Join(dataDir, "skills"))
	if err != nil {
		return SkillConfig{}, err
	}
	maxSkills, err := envInt(lookup, "QEMU_AGENT_SKILLS_MAX_COUNT", DefaultMaxSkills)
	if err != nil {
		return SkillConfig{}, err
	}
	maxFileBytes, err := envInt(lookup, "QEMU_AGENT_SKILL_MAX_FILE_BYTES", DefaultMaxSkillFileBytes)
	if err != nil {
		return SkillConfig{}, err
	}
	maxBodyBytes, err := envInt(lookup, "QEMU_AGENT_SKILL_MAX_BODY_BYTES", DefaultMaxSkillBodyBytes)
	if err != nil {
		return SkillConfig{}, err
	}
	maxIndexBytes, err := envInt(lookup, "QEMU_AGENT_SKILL_MAX_INDEX_BYTES", DefaultMaxSkillIndexBytes)
	if err != nil {
		return SkillConfig{}, err
	}
	return SkillConfig{
		Enabled:       enabled,
		Dir:           dir,
		MaxSkills:     maxSkills,
		MaxFileBytes:  maxFileBytes,
		MaxBodyBytes:  maxBodyBytes,
		MaxIndexBytes: maxIndexBytes,
	}, nil
}

// loadMemoryConfig reads the Memory group. I7-A only closes the values; the
// Store, Sanitizer and Ranker arrive with I7-E/I7-F.
func loadMemoryConfig(lookup LookupEnv, dataDir string) (MemoryConfig, error) {
	enabled, err := envBool(lookup, "QEMU_AGENT_MEMORY_ENABLED", DefaultMemoryEnabled)
	if err != nil {
		return MemoryConfig{}, err
	}
	dir, err := resolveDir(lookup, "QEMU_AGENT_MEMORY_DIR", filepath.Join(dataDir, "memory"))
	if err != nil {
		return MemoryConfig{}, err
	}
	topK, err := envInt(lookup, "QEMU_AGENT_MEMORY_TOP_K", DefaultMemoryTopK)
	if err != nil {
		return MemoryConfig{}, err
	}
	maxItems, err := envInt(lookup, "QEMU_AGENT_MEMORY_MAX_ITEMS", DefaultMemoryMaxItems)
	if err != nil {
		return MemoryConfig{}, err
	}
	maxItemBytes, err := envInt(lookup, "QEMU_AGENT_MEMORY_MAX_ITEM_BYTES", DefaultMemoryMaxItemBytes)
	if err != nil {
		return MemoryConfig{}, err
	}
	maxInjectedBytes, err := envInt(lookup, "QEMU_AGENT_MEMORY_MAX_INJECTED_BYTES", DefaultMemoryMaxInjectedBytes)
	if err != nil {
		return MemoryConfig{}, err
	}
	halfLife, err := envDuration(lookup, "QEMU_AGENT_MEMORY_HALF_LIFE", DefaultMemoryHalfLife)
	if err != nil {
		return MemoryConfig{}, err
	}
	strictSearch, err := envBool(lookup, "QEMU_AGENT_MEMORY_STRICT_SEARCH", DefaultMemoryStrictSearch)
	if err != nil {
		return MemoryConfig{}, err
	}
	autoExtract, err := envBool(lookup, "QEMU_AGENT_MEMORY_AUTO_EXTRACT", DefaultMemoryAutoExtract)
	if err != nil {
		return MemoryConfig{}, err
	}
	candidateTTL, err := envDuration(lookup, "QEMU_AGENT_MEMORY_CANDIDATE_TTL", DefaultCandidateTTL)
	if err != nil {
		return MemoryConfig{}, err
	}
	return MemoryConfig{
		Enabled:          enabled,
		Dir:              dir,
		TopK:             topK,
		MaxItems:         maxItems,
		MaxItemBytes:     maxItemBytes,
		MaxInjectedBytes: maxInjectedBytes,
		HalfLife:         halfLife,
		StrictSearch:     strictSearch,
		AutoExtract:      autoExtract,
		CandidateTTL:     candidateTTL,
	}, nil
}

// loadPromptConfig reads the request-level injection budget used by the
// I7-G PromptAssembler.
func loadPromptConfig(lookup LookupEnv) (PromptConfig, error) {
	reserved, err := envInt(lookup, "QEMU_AGENT_PROMPT_RESERVED_TOKENS", DefaultPromptReservedTokens)
	if err != nil {
		return PromptConfig{}, err
	}
	maxInjected, err := envInt(lookup, "QEMU_AGENT_PROMPT_MAX_INJECTED_BYTES", DefaultPromptMaxBytes)
	if err != nil {
		return PromptConfig{}, err
	}
	maxMemoryItems, err := envInt(lookup, "QEMU_AGENT_PROMPT_MAX_MEMORY_ITEMS", DefaultPromptMaxMemoryItems)
	if err != nil {
		return PromptConfig{}, err
	}
	return PromptConfig{
		ReservedContextTokens: reserved,
		MaxInjectedBytes:      maxInjected,
		MaxMemoryItems:        maxMemoryItems,
	}, nil
}

// LoadEnv reads environment-backed values without performing final validation.
// Validation is delayed until explicit overrides have been applied.
func LoadEnv(lookup LookupEnv) (Config, error) {
	// Agent load config
	maxTurns, err := envInt(lookup, "QEMU_AGENT_MAX_TURNS", DefaultMaxTurns)
	if err != nil {
		return Config{}, err
	}
	stream, err := envBool(lookup, "QEMU_AGENT_STREAM", false)
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
	// tool config read
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
	// channel config read
	maxInputBytes, err := envInt(lookup, "QEMU_AGENT_CLI_MAX_INPUT_BYTES", DefaultMaxInputBytes)
	if err != nil {
		return Config{}, err
	}
	cliEnabled, err := envBool(lookup, "QEMU_AGENT_CLI_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	telegramEnabled, err := envBool(lookup, "QEMU_AGENT_TELEGRAM_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	allowedUserIDs, err := envInt64List(lookup, "QEMU_AGENT_TELEGRAM_ALLOWED_USER_IDS")
	if err != nil {
		return Config{}, err
	}
	allowGroups, err := envBool(lookup, "QEMU_AGENT_TELEGRAM_ALLOW_GROUP_CHATS", false)
	if err != nil {
		return Config{}, err
	}
	pollTimeout, err := envDuration(lookup, "QEMU_AGENT_TELEGRAM_POLL_TIMEOUT", DefaultTelegramPollTimeout)
	if err != nil {
		return Config{}, err
	}
	retryMin, err := envDuration(lookup, "QEMU_AGENT_TELEGRAM_RETRY_MIN_BACKOFF", DefaultTelegramRetryMinBackoff)
	if err != nil {
		return Config{}, err
	}
	retryMax, err := envDuration(lookup, "QEMU_AGENT_TELEGRAM_RETRY_MAX_BACKOFF", DefaultTelegramRetryMaxBackoff)
	if err != nil {
		return Config{}, err
	}
	telegramConcurrency, err := envInt(lookup, "QEMU_AGENT_TELEGRAM_MAX_CONCURRENCY", DefaultTelegramMaxConcurrency)
	if err != nil {
		return Config{}, err
	}
	telegramMaxInput, err := envInt(lookup, "QEMU_AGENT_TELEGRAM_MAX_INPUT_BYTES", DefaultTelegramMaxInputBytes)
	if err != nil {
		return Config{}, err
	}
	editInterval, err := envDuration(lookup, "QEMU_AGENT_TELEGRAM_EDIT_INTERVAL", DefaultTelegramEditInterval)
	if err != nil {
		return Config{}, err
	}
	chunkSize, err := envInt(lookup, "QEMU_AGENT_TELEGRAM_MESSAGE_CHUNK_SIZE", DefaultTelegramMessageChunkSize)
	if err != nil {
		return Config{}, err
	}

	//workspace and data direction read
	dataDir, err := resolveDataDir(lookup)
	if err != nil {
		return Config{}, err
	}
	workspace, err := resolveWorkspace(lookup)
	if err != nil {
		return Config{}, err
	}

	// tool security config read
	auditPath := envString(lookup, "QEMU_AGENT_TOOL_AUDIT_PATH", "")
	if auditPath == "" {
		auditPath = filepath.Join(dataDir, "audit", "tools.jsonl")
	} else {
		absolute, err := filepath.Abs(auditPath)
		if err != nil {
			return Config{}, fmt.Errorf("resolve tool audit path: %w", err)
		}
		auditPath = filepath.Clean(absolute)
	}
	approvalTimeout, err := envDuration(lookup, "QEMU_AGENT_TOOL_APPROVAL_TIMEOUT", DefaultApprovalTimeout)
	if err != nil {
		return Config{}, err
	}
	maxAuditArgs, err := envInt(lookup, "QEMU_AGENT_TOOL_MAX_AUDIT_ARG_BYTES", DefaultMaxAuditArgBytes)
	if err != nil {
		return Config{}, err
	}
	maxAuditOutput, err := envInt(lookup, "QEMU_AGENT_TOOL_MAX_AUDIT_OUT_BYTES", DefaultMaxAuditOutBytes)
	if err != nil {
		return Config{}, err
	}

	provider := envString(lookup, "QEMU_AGENT_PROVIDER", DefaultProvider)
	definitions, err := loadModelDefinitions(lookup, provider, model, maxContext, stream)
	if err != nil {
		return Config{}, err
	}
	// skill, memory and prompt config read
	skillConfig, err := loadSkillConfig(lookup, dataDir)
	if err != nil {
		return Config{}, err
	}
	memoryConfig, err := loadMemoryConfig(lookup, dataDir)
	if err != nil {
		return Config{}, err
	}
	promptConfig, err := loadPromptConfig(lookup)
	if err != nil {
		return Config{}, err
	}

	// general config build
	cfg := Config{
		Agent: AgentConfig{
			Provider: provider,
			Model:    model,
			MaxTurns: maxTurns,
			Stream:   stream,
		},
		Context: ContextConfig{
			MaxTokens:       maxContext,
			KeepRecentTurns: keepRecent,
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
			Level:  envString(lookup, "QEMU_AGENT_LOG_LEVEL", DefaultLogLevel),
			Format: envString(lookup, "QEMU_AGENT_LOG_FORMAT", DefaultLogFormat),
		},
		Providers: ProviderConfig{
			OpenRouter: APIConfig{
				APIKey:  envString(lookup, "OPENROUTER_API_KEY", ""),
				BaseURL: envString(lookup, "OPENROUTER_BASE_URL", DefaultOpenRouterBaseURL),
			},
			OpenAI: APIConfig{
				APIKey:  envString(lookup, "OPENAI_API_KEY", ""),
				BaseURL: envString(lookup, "OPENAI_BASE_URL", DefaultOpenAIBaseURL),
			},
			Ollama: APIConfig{
				BaseURL: envString(lookup, "OLLAMA_BASE_URL", DefaultOllamaBaseURL),
			},
		},
		Models: ModelConfig{Definitions: definitions, CompatibilityGenerated: func() bool { raw, ok := lookup("QEMU_AGENT_MODELS_JSON"); return !ok || strings.TrimSpace(raw) == "" }()},
		Channel: ChannelConfig{
			CLIEnabled:    cliEnabled,
			CLISessionKey: envString(lookup, "QEMU_AGENT_CLI_SESSION_KEY", DefaultCLISessionKey),
			CLIPrompt:     envString(lookup, "QEMU_AGENT_CLI_PROMPT", DefaultCLIPrompt),
			MaxInputBytes: maxInputBytes,
			Telegram: TelegramConfig{
				Enabled: telegramEnabled, Token: envString(lookup, "QEMU_AGENT_TELEGRAM_TOKEN", ""),
				AllowedUserIDs: allowedUserIDs, AllowGroupChats: allowGroups,
				PollTimeout: pollTimeout, RetryMinBackoff: retryMin, RetryMaxBackoff: retryMax,
				MaxConcurrency: telegramConcurrency, MaxInputBytes: telegramMaxInput,
				EditInterval: editInterval, MessageChunkSize: chunkSize,
			},
		},
		Security: SecurityConfig{
			Mode:             envString(lookup, "QEMU_AGENT_TOOL_SECURITY_MODE", DefaultSecurityMode),
			AuditPath:        auditPath,
			ApprovalTimeout:  approvalTimeout,
			MaxAuditArgBytes: maxAuditArgs,
			MaxAuditOutBytes: maxAuditOutput,
		},
		Skills: skillConfig,
		Memory: memoryConfig,
		Prompt: promptConfig,
	}

	return cfg, nil
}
