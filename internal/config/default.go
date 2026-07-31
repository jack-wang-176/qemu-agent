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

	// Skill default
	DefaultSkillsEnabled      = true
	DefaultMaxSkills          = 128
	DefaultMaxSkillFileBytes  = 256 * 1024
	DefaultMaxSkillBodyBytes  = 192 * 1024
	DefaultMaxSkillIndexBytes = 16 * 1024

	// Memory default. Recall is on because a Store now exists and reading is
	// harmless: nothing is written without an explicit /remember or an approved
	// candidate. Auto-extraction stays off — it spends a model call on every turn
	// and fills a review queue nobody asked for.
	DefaultMemoryEnabled          = true
	DefaultMemoryTopK             = 6
	DefaultMemoryMaxItems         = 10000
	DefaultMemoryMaxItemBytes     = 8 * 1024
	DefaultMemoryMaxInjectedBytes = 24 * 1024
	DefaultMemoryStrictSearch     = false
	DefaultMemoryAutoExtract      = false
	DefaultMemoryHalfLife         = 30 * 24 * time.Hour
	DefaultCandidateTTL           = 7 * 24 * time.Hour

	// Prompt default
	DefaultPromptReservedTokens = 4096
	DefaultPromptMaxBytes       = 40 * 1024
	DefaultPromptMaxMemoryItems = DefaultMemoryTopK
	MaxMemoryTopK               = 100

	// Modeling default. Disabled by default because I8 writes into a QEMU
	// source tree, which is outside the workspace sandbox; AutoApply is off for
	// the same reason — Emit produces a staged plan, and landing it is a
	// separate, approved step.
	DefaultModelingEnabled       = false
	DefaultModelingMaxProjects   = 1000
	DefaultModelingArtifactBytes = 1 << 20
	DefaultModelingProjectBytes  = 8 << 20
	DefaultModelingStageTimeout  = 10 * time.Minute
	DefaultModelingAutoApply     = false
)

const DefaultToolTimeout = 60 * time.Second
