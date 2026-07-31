package config

import "time"

const (
	// agent default
	DefaultProvider         = "openrouter"
	DefaultModel            = "openai/gpt-4o-mini"
	DefaultMaxTurns         = 15
	DefaultMaxContextTokens = 160000
	DefaultKeepRecentTurns  = 4
	DefaultToolMaxOutput    = 64 * 1024
	DefaultReadMaxLines     = 500
	//log default
	DefaultLogLevel  = "info"
	DefaultLogFormat = "text"

	// Provide default
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	DefaultOpenAIBaseURL     = "https://api.openai.com/v1"
	DefaultOllamaBaseURL     = "http://localhost:11434/v1"

	// CLI default
	DefaultCLISessionKey = "cli:default"
	DefaultCLIPrompt     = "> "
	DefaultMaxInputBytes = 1024 * 1024
	MaxCLIInputBytes     = 16 * 1024 * 1024

	// Telegram defaults. Plain text leaves room below Telegram's 4096-rune limit.
	DefaultTelegramPollTimeout      = 30 * time.Second
	DefaultTelegramRetryMinBackoff  = 500 * time.Millisecond
	DefaultTelegramRetryMaxBackoff  = 30 * time.Second
	DefaultTelegramMaxConcurrency   = 4
	DefaultTelegramMaxInputBytes    = 64 * 1024
	DefaultTelegramEditInterval     = 750 * time.Millisecond
	DefaultTelegramMessageChunkSize = 3500

	// Security default
	DefaultSecurityMode     = "ask-dangerous"
	DefaultApprovalTimeout  = 5 * time.Minute
	DefaultMaxAuditArgBytes = 16 * 1024
	DefaultMaxAuditOutBytes = 16 * 1024
)

const DefaultToolTimeout = 60 * time.Second
