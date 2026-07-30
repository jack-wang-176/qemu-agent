package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/skills"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

// BuildSkillRegistry scans the skills directory exactly once, at startup. A
// failure here fails the process: an agent that silently starts without the
// skills an operator installed is worse than one that refuses to start.
func BuildSkillRegistry(cfg config.SkillConfig) (*skills.Registry, error) {
	registry, err := skills.ScanRegistry(cfg.Dir, skillsConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("scan skills: %w", err)
	}
	return registry, nil
}

// RegisterSkillTool adds use_skill only when there is something to load, so the
// model never sees a tool whose entire index is empty. It then verifies every
// declared required tool exists in the same manager the executor will use.
func RegisterSkillTool(manager *tools.Manager, registry *skills.Registry, cfg config.SkillConfig) error {
	if manager == nil {
		return fmt.Errorf("tool manager is nil")
	}
	if !cfg.Enabled || registry.Len() == 0 {
		return nil
	}
	tool, err := skills.NewUseSkillTool(registry, cfg.MaxIndexBytes)
	if err != nil {
		return fmt.Errorf("build use_skill tool: %w", err)
	}
	if err := manager.Register(tool); err != nil {
		return fmt.Errorf("register tool %q: %w", tool.Name(), err)
	}
	if err := skills.ValidateRequiredTools(registry, manager); err != nil {
		return fmt.Errorf("validate skill tools: %w", err)
	}
	return nil
}

func skillsConfig(cfg config.SkillConfig) skills.Config {
	return skills.Config{
		Enabled:   cfg.Enabled,
		Dir:       cfg.Dir,
		MaxSkills: cfg.MaxSkills,
		Limits: skills.Limits{
			MaxFileBytes: int64(cfg.MaxFileBytes),
			MaxBodyBytes: cfg.MaxBodyBytes,
		},
		MaxIndexBytes: cfg.MaxIndexBytes,
	}
}
