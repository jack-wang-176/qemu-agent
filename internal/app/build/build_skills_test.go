package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/tools"
)

const buildTestSkill = `---
name: demo-skill
description: Demo workflow used by build tests.
version: "1"
required_tools:
  - read
---

Do the thing.
`

func skillConfigFor(t *testing.T, dir string) config.SkillConfig {
	t.Helper()
	return config.SkillConfig{
		Enabled: true, Dir: dir, MaxSkills: config.DefaultMaxSkills,
		MaxFileBytes: config.DefaultMaxSkillFileBytes, MaxBodyBytes: config.DefaultMaxSkillBodyBytes,
		MaxIndexBytes: config.DefaultMaxSkillIndexBytes,
	}
}

func writeBuildTestSkill(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, "demo-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func toolManagerFor(t *testing.T, workspace string) *tools.Manager {
	t.Helper()
	manager, err := BuildToolManager(config.PathConfig{Workspace: workspace}, config.ToolConfig{Timeout: 1, MaxOutputBytes: 1, ReadMaxLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestRegisterSkillToolAddsUseSkill(t *testing.T) {
	root := t.TempDir()
	writeBuildTestSkill(t, root, buildTestSkill)
	cfg := skillConfigFor(t, root)
	registry, err := BuildSkillRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager := toolManagerFor(t, t.TempDir())
	if err := RegisterSkillTool(manager, registry, cfg); err != nil {
		t.Fatal(err)
	}
	if !manager.Has("use_skill") {
		t.Fatalf("tools = %v", manager.Names())
	}
	if len(manager.Schemas()) != 4 {
		t.Fatalf("schemas = %v", manager.Names())
	}
}

func TestRegisterSkillToolSkippedWithoutSkills(t *testing.T) {
	cfg := skillConfigFor(t, filepath.Join(t.TempDir(), "absent"))
	registry, err := BuildSkillRegistry(cfg)
	if err != nil {
		t.Fatalf("a missing skills dir must not fail startup: %v", err)
	}
	manager := toolManagerFor(t, t.TempDir())
	if err := RegisterSkillTool(manager, registry, cfg); err != nil {
		t.Fatal(err)
	}
	if manager.Has("use_skill") {
		t.Fatal("use_skill must not be offered with an empty index")
	}
}

func TestRegisterSkillToolRejectsUnknownRequiredTool(t *testing.T) {
	root := t.TempDir()
	writeBuildTestSkill(t, root, "---\nname: demo-skill\ndescription: d\nversion: \"1\"\nrequired_tools:\n  - qmp\n---\nbody\n")
	cfg := skillConfigFor(t, root)
	registry, err := BuildSkillRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterSkillTool(toolManagerFor(t, t.TempDir()), registry, cfg); err == nil {
		t.Fatal("err = nil for a skill requiring an unregistered tool")
	}
}

func TestBuildSkillRegistryFailsOnBrokenSkill(t *testing.T) {
	root := t.TempDir()
	writeBuildTestSkill(t, root, "no frontmatter here\n")
	if _, err := BuildSkillRegistry(skillConfigFor(t, root)); err == nil {
		t.Fatal("err = nil for a malformed skill file")
	}
}
