package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// modelingBase returns a valid config with Modeling enabled, so each test only
// has to break the one field it is about.
func modelingBase(t *testing.T) Config {
	t.Helper()
	cfg := knowledgeBase(t)
	cfg.Modeling = ModelingConfig{
		Enabled:          true,
		Dir:              filepath.Join(cfg.Paths.DataDir, "modeling"),
		MaxProjects:      DefaultModelingMaxProjects,
		MaxArtifactBytes: DefaultModelingArtifactBytes,
		MaxProjectBytes:  DefaultModelingProjectBytes,
		StageTimeout:     DefaultModelingStageTimeout,
	}
	return cfg
}

func TestValidateModelingAcceptsDefaults(t *testing.T) {
	if err := modelingBase(t).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// A disabled capability only has to be internally consistent: an operator must
// be able to start the agent without creating a modeling directory or owning a
// QEMU source tree.
func TestValidateModelingSkipsWhenDisabled(t *testing.T) {
	cfg := modelingBase(t)
	cfg.Modeling = ModelingConfig{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateModelingRejectsInconsistentValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty dir", mutate: func(c *Config) { c.Modeling.Dir = " " }},
		{name: "relative dir", mutate: func(c *Config) { c.Modeling.Dir = "modeling" }},
		{name: "dir equals memory dir", mutate: func(c *Config) { c.Modeling.Dir = c.Memory.Dir }},
		{name: "dir equals skills dir", mutate: func(c *Config) { c.Modeling.Dir = c.Skills.Dir }},
		{name: "dir inside session dir", mutate: func(c *Config) { c.Modeling.Dir = filepath.Join(c.Paths.SessionDir, "modeling") }},
		{name: "relative qemu root", mutate: func(c *Config) { c.Modeling.QemuRoot = "qemu" }},
		{name: "auto apply without qemu root", mutate: func(c *Config) { c.Modeling.AutoApply = true }},
		{name: "zero max projects", mutate: func(c *Config) { c.Modeling.MaxProjects = 0 }},
		{name: "zero artifact bytes", mutate: func(c *Config) { c.Modeling.MaxArtifactBytes = 0 }},
		{name: "artifact over project limit", mutate: func(c *Config) { c.Modeling.MaxArtifactBytes = c.Modeling.MaxProjectBytes + 1 }},
		{name: "zero stage timeout", mutate: func(c *Config) { c.Modeling.StageTimeout = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := modelingBase(t)
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// An absolute QemuRoot plus AutoApply is the one legal way to ask for automatic
// landing, and it must pass.
func TestValidateModelingAcceptsAutoApplyWithQemuRoot(t *testing.T) {
	cfg := modelingBase(t)
	cfg.Modeling.QemuRoot = t.TempDir()
	cfg.Modeling.AutoApply = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateProfessionalModelingV1RequiresEnabledWithoutAutoApply(t *testing.T) {
	disabled := modelingBase(t)
	disabled.Modeling.Enabled = false
	if err := disabled.ValidateProfessionalModelingV1(); err == nil {
		t.Fatal("professional validation accepted disabled modeling")
	}

	auto := modelingBase(t)
	auto.Modeling.QemuRoot = t.TempDir()
	auto.Modeling.AutoApply = true
	if err := auto.ValidateProfessionalModelingV1(); err == nil {
		t.Fatal("professional validation accepted auto apply")
	}

	if err := modelingBase(t).ValidateProfessionalModelingV1(); err != nil {
		t.Fatalf("professional validation rejected v1 configuration: %v", err)
	}
}

func TestLoadPopulatesModelingDefaults(t *testing.T) {
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
	// Off by default, but with every limit already closed, so enabling the
	// capability never needs a second variable.
	if cfg.Modeling.Enabled || cfg.Modeling.AutoApply {
		t.Fatalf("modeling = %#v", cfg.Modeling)
	}
	if cfg.Modeling.Dir != filepath.Join(data, "modeling") {
		t.Fatalf("modeling dir = %q", cfg.Modeling.Dir)
	}
	if cfg.Modeling.QemuRoot != "" {
		t.Fatalf("modeling qemu root = %q", cfg.Modeling.QemuRoot)
	}
	if cfg.Modeling.MaxProjects != DefaultModelingMaxProjects || cfg.Modeling.StageTimeout != DefaultModelingStageTimeout {
		t.Fatalf("modeling limits = %#v", cfg.Modeling)
	}
	if summary := cfg.Summary(); summary.ModelingDir != cfg.Modeling.Dir || summary.ModelingEnabled {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestLoadReadsModelingOverridesFromEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, qemu := t.TempDir(), t.TempDir()
	cfg, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":                    "ollama",
		"QEMU_AGENT_WORKSPACE":                   t.TempDir(),
		"QEMU_AGENT_DATA_DIR":                    t.TempDir(),
		"QEMU_AGENT_MODELING_ENABLED":            "true",
		"QEMU_AGENT_MODELING_DIR":                dir,
		"QEMU_AGENT_MODELING_QEMU_ROOT":          qemu,
		"QEMU_AGENT_MODELING_MAX_PROJECTS":       "7",
		"QEMU_AGENT_MODELING_STAGE_TIMEOUT":      "90s",
		"QEMU_AGENT_MODELING_MODEL":              "planner",
		"QEMU_AGENT_MODELING_AUTO_APPLY":         "true",
		"QEMU_AGENT_MODELING_MAX_ARTIFACT_BYTES": "2048",
	}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Modeling.Enabled || cfg.Modeling.Dir != dir || cfg.Modeling.QemuRoot != qemu {
		t.Fatalf("modeling = %#v", cfg.Modeling)
	}
	if cfg.Modeling.MaxProjects != 7 || cfg.Modeling.StageTimeout != 90*time.Second {
		t.Fatalf("modeling limits = %#v", cfg.Modeling)
	}
	if cfg.Modeling.Model != "planner" || !cfg.Modeling.AutoApply || cfg.Modeling.MaxArtifactBytes != 2048 {
		t.Fatalf("modeling = %#v", cfg.Modeling)
	}
}

func TestLoadRejectsUnparsableModelingValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Load(lookupFrom(map[string]string{
		"QEMU_AGENT_PROVIDER":               "ollama",
		"QEMU_AGENT_WORKSPACE":              t.TempDir(),
		"QEMU_AGENT_MODELING_STAGE_TIMEOUT": "soon",
	}), Overrides{})
	if err == nil || !strings.Contains(err.Error(), "QEMU_AGENT_MODELING_STAGE_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
}
