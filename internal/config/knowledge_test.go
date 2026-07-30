package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func knowledgeBase(t *testing.T) Config {
	t.Helper()
	data := t.TempDir()
	return Config{
		Agent:     AgentConfig{Provider: "ollama", Model: "model", MaxTurns: 1},
		Models:    ModelConfig{Definitions: []ModelDefinitionConfig{{Provider: "ollama", Name: "model", MaxContext: 4096, Tools: true}}},
		Context:   ContextConfig{MaxTokens: 1},
		Paths:     PathConfig{DataDir: data, SessionDir: filepath.Join(data, "sessions"), Workspace: t.TempDir()},
		Tools:     ToolConfig{Timeout: 1, MaxOutputBytes: 1, ReadMaxLines: 1},
		Log:       LogConfig{Level: "info", Format: "text"},
		Providers: ProviderConfig{Ollama: APIConfig{BaseURL: DefaultOllamaBaseURL}},
		Channel:   ChannelConfig{CLIEnabled: true, CLISessionKey: DefaultCLISessionKey, CLIPrompt: DefaultCLIPrompt, MaxInputBytes: DefaultMaxInputBytes},
		Security:  SecurityConfig{Mode: DefaultSecurityMode, AuditPath: filepath.Join(data, "audit", "tools.jsonl"), ApprovalTimeout: DefaultApprovalTimeout, MaxAuditArgBytes: DefaultMaxAuditArgBytes, MaxAuditOutBytes: DefaultMaxAuditOutBytes},
		Skills: SkillConfig{
			Enabled: true, Dir: filepath.Join(data, "skills"), MaxSkills: DefaultMaxSkills,
			MaxFileBytes: DefaultMaxSkillFileBytes, MaxBodyBytes: DefaultMaxSkillBodyBytes, MaxIndexBytes: DefaultMaxSkillIndexBytes,
		},
		Memory: MemoryConfig{
			Enabled: true, Dir: filepath.Join(data, "memory"), TopK: DefaultMemoryTopK,
			MaxItems: DefaultMemoryMaxItems, MaxItemBytes: DefaultMemoryMaxItemBytes,
			MaxInjectedBytes: DefaultMemoryMaxInjectedBytes, HalfLife: DefaultMemoryHalfLife,
		},
		Prompt: PromptConfig{ReservedContextTokens: DefaultPromptReservedTokens, MaxInjectedBytes: DefaultPromptMaxBytes, MaxMemoryItems: DefaultPromptMaxMemoryItems},
	}
}

func TestValidateKnowledgeAcceptsDefaults(t *testing.T) {
	if err := knowledgeBase(t).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateKnowledgeRejectsInconsistentValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty skills dir", mutate: func(c *Config) { c.Skills.Dir = " " }},
		{name: "relative skills dir", mutate: func(c *Config) { c.Skills.Dir = "skills" }},
		{name: "zero max skills", mutate: func(c *Config) { c.Skills.MaxSkills = 0 }},
		{name: "zero skill index bytes", mutate: func(c *Config) { c.Skills.MaxIndexBytes = 0 }},
		{name: "body over file limit", mutate: func(c *Config) { c.Skills.MaxBodyBytes = c.Skills.MaxFileBytes + 1 }},
		{name: "skills inside session dir", mutate: func(c *Config) { c.Skills.Dir = filepath.Join(c.Paths.SessionDir, "skills") }},
		{name: "memory equals skills", mutate: func(c *Config) { c.Memory.Dir = c.Skills.Dir }},
		{name: "memory top k too large", mutate: func(c *Config) { c.Memory.TopK = MaxMemoryTopK + 1 }},
		{name: "memory top k zero", mutate: func(c *Config) { c.Memory.TopK = 0; c.Prompt.MaxMemoryItems = 0 }},
		{name: "memory half life zero", mutate: func(c *Config) { c.Memory.HalfLife = 0 }},
		{name: "auto extract without ttl", mutate: func(c *Config) { c.Memory.AutoExtract = true; c.Memory.CandidateTTL = 0 }},
		{name: "prompt reserved zero", mutate: func(c *Config) { c.Prompt.ReservedContextTokens = 0 }},
		{name: "prompt injected zero", mutate: func(c *Config) { c.Prompt.MaxInjectedBytes = 0 }},
		{name: "prompt memory items negative", mutate: func(c *Config) { c.Prompt.MaxMemoryItems = -1 }},
		{name: "prompt memory items over top k", mutate: func(c *Config) { c.Prompt.MaxMemoryItems = c.Memory.TopK + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := knowledgeBase(t)
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// A disabled capability only has to be internally consistent: an operator must
// be able to start the agent without creating any skill or memory directory.
func TestValidateKnowledgeSkipsDisabledCapabilities(t *testing.T) {
	cfg := knowledgeBase(t)
	cfg.Skills = SkillConfig{}
	cfg.Memory = MemoryConfig{}
	cfg.Prompt.MaxMemoryItems = 999
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadPopulatesKnowledgeDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := t.TempDir()
	cfg, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":  "ollama",
		"QEMU_AGENT_WORKSPACE": t.TempDir(),
		"QEMU_AGENT_DATA_DIR":  data,
	}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Skills.Dir != filepath.Join(data, "skills") || !cfg.Skills.Enabled {
		t.Fatalf("skills = %#v", cfg.Skills)
	}
	if cfg.Memory.Dir != filepath.Join(data, "memory") || !cfg.Memory.Enabled {
		t.Fatalf("memory = %#v", cfg.Memory)
	}
	// Recall on, proposals off: reading costs nothing, and auto-extraction spends
	// a model call per turn and needs a human to drain its queue.
	if cfg.Memory.AutoExtract {
		t.Fatalf("auto extract defaults on: %#v", cfg.Memory)
	}
	if cfg.Prompt.ReservedContextTokens != DefaultPromptReservedTokens || cfg.Prompt.MaxMemoryItems != DefaultPromptMaxMemoryItems {
		t.Fatalf("prompt = %#v", cfg.Prompt)
	}
	if summary := cfg.Summary(); summary.SkillsDir != cfg.Skills.Dir {
		t.Fatalf("summary skills dir = %q", summary.SkillsDir)
	}
}

func TestLoadReadsKnowledgeOverridesFromEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	skills, memory := t.TempDir(), t.TempDir()
	cfg, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":                "ollama",
		"QEMU_AGENT_WORKSPACE":               t.TempDir(),
		"QEMU_AGENT_DATA_DIR":                t.TempDir(),
		"QEMU_AGENT_SKILLS_DIR":              skills,
		"QEMU_AGENT_SKILLS_MAX_COUNT":        "3",
		"QEMU_AGENT_MEMORY_ENABLED":          "true",
		"QEMU_AGENT_MEMORY_DIR":              memory,
		"QEMU_AGENT_MEMORY_TOP_K":            "4",
		"QEMU_AGENT_MEMORY_HALF_LIFE":        "48h",
		"QEMU_AGENT_MEMORY_AUTO_EXTRACT":     "true",
		"QEMU_AGENT_PROMPT_MAX_MEMORY_ITEMS": "2",
	}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Skills.Dir != skills || cfg.Skills.MaxSkills != 3 {
		t.Fatalf("skills = %#v", cfg.Skills)
	}
	if !cfg.Memory.Enabled || cfg.Memory.Dir != memory || cfg.Memory.TopK != 4 || cfg.Memory.HalfLife != 48*time.Hour {
		t.Fatalf("memory = %#v", cfg.Memory)
	}
	if !cfg.Memory.AutoExtract || cfg.Memory.CandidateTTL != DefaultCandidateTTL {
		t.Fatalf("auto extract config = %#v", cfg.Memory)
	}
	if cfg.Prompt.MaxMemoryItems != 2 {
		t.Fatalf("prompt = %#v", cfg.Prompt)
	}
}

func TestLoadRejectsUnparsableKnowledgeValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":         "ollama",
		"QEMU_AGENT_WORKSPACE":        t.TempDir(),
		"QEMU_AGENT_MEMORY_HALF_LIFE": "forever",
	}), Overrides{})
	if err == nil || !strings.Contains(err.Error(), "QEMU_AGENT_MEMORY_HALF_LIFE") {
		t.Fatalf("err = %v", err)
	}
}
