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
	Models    ModelConfig
	Channel   ChannelConfig
	Security  SecurityConfig
	Skills    SkillConfig
	Memory    MemoryConfig
	Prompt    PromptConfig
	Modeling  ModelingConfig
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

type ChannelConfig struct {
	CLIEnabled    bool
	CLISessionKey string
	CLIPrompt     string
	MaxInputBytes int
	Telegram      TelegramConfig
}

type TelegramConfig struct {
	Enabled          bool
	Token            string
	AllowedUserIDs   []int64
	AllowGroupChats  bool
	PollTimeout      time.Duration
	RetryMinBackoff  time.Duration
	RetryMaxBackoff  time.Duration
	MaxConcurrency   int
	MaxInputBytes    int
	EditInterval     time.Duration
	MessageChunkSize int
}

type SecurityConfig struct {
	Mode             string
	AuditPath        string
	ApprovalTimeout  time.Duration
	MaxAuditArgBytes int
	MaxAuditOutBytes int
}

type ModelConfig struct {
	Definitions            []ModelDefinitionConfig
	CompatibilityGenerated bool
}

type ModelDefinitionConfig struct {
	Provider    string   `json:"provider"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases"`
	MaxContext  int      `json:"max_context"`
	MaxOutput   int      `json:"max_output"`
	Tools       bool     `json:"tools"`
	Streaming   bool     `json:"streaming"`
}

type SkillConfig struct {
	Enabled       bool
	Dir           string
	MaxSkills     int
	MaxFileBytes  int
	MaxBodyBytes  int
	MaxIndexBytes int
}

type MemoryConfig struct {
	Enabled          bool
	Dir              string
	TopK             int
	MaxItems         int
	MaxItemBytes     int
	MaxInjectedBytes int
	HalfLife         time.Duration
	StrictSearch     bool
	AutoExtract      bool
	CandidateTTL     time.Duration
}

type PromptConfig struct {
	ReservedContextTokens int
	MaxInjectedBytes      int
	MaxMemoryItems        int
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
