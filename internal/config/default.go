package config

import "time"

const (
	DefaultProvider         = "openrouter"
	DefaultModel            = "openai/gpt-4o-mini"
	DefaultMaxTurns         = 15
	DefaultMaxContextTokens = 160000
	DefaultKeepRecentTurns  = 4
	DefaultToolMaxOutput    = 64 * 1024
	DefaultReadMaxLines     = 500

	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	DefaultOpenAIBaseURL     = "https://api.openai.com/v1"
	DefaultOllamaBaseURL     = "http://localhost:11434/v1"
)

const DefaultToolTimeout = 60 * time.Second
