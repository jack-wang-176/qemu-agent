package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLimits() Limits { return Limits{MaxFileBytes: 64 * 1024, MaxBodyBytes: 32 * 1024} }

func writeSkill(t *testing.T, root, directory, content string) string {
	t.Helper()
	dir := filepath.Join(root, directory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validSkill = `---
name: demo-skill
description: Demo workflow used by tests.
version: "2"
tags:
  - Qemu
  - demo
  - qemu
required_tools:
  - read
  - bash
---

# Demo

Step one.
`

func TestParseSkillFileNormalizesAndHashesBody(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "demo-skill", validSkill)
	skill, err := parseSkillFile(path, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if skill.Meta.Name != "demo-skill" || skill.Meta.Version != "2" {
		t.Fatalf("meta = %#v", skill.Meta)
	}
	if strings.Join(skill.Meta.Tags, ",") != "demo,qemu" {
		t.Fatalf("tags = %v, want sorted and de-duplicated", skill.Meta.Tags)
	}
	if strings.Join(skill.Meta.RequiredTools, ",") != "bash,read" {
		t.Fatalf("required tools = %v", skill.Meta.RequiredTools)
	}
	if skill.Body != "# Demo\n\nStep one." {
		t.Fatalf("body = %q", skill.Body)
	}
	digest := sha256.Sum256([]byte(skill.Body))
	if skill.Meta.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("sha256 = %q", skill.Meta.SHA256)
	}
}

func TestParseSkillFileRejectsBadInput(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		content   string
	}{
		{name: "no frontmatter", directory: "demo-skill", content: "# body only\n"},
		{name: "unterminated frontmatter", directory: "demo-skill", content: "---\nname: demo-skill\n"},
		{name: "unknown field", directory: "demo-skill", content: "---\nname: demo-skill\ndescription: d\nversion: \"1\"\nauthor: me\n---\nbody\n"},
		{name: "empty body", directory: "demo-skill", content: "---\nname: demo-skill\ndescription: d\nversion: \"1\"\n---\n   \n"},
		{name: "directory mismatch", directory: "other-dir", content: "---\nname: demo-skill\ndescription: d\nversion: \"1\"\n---\nbody\n"},
		{name: "invalid name", directory: "Demo", content: "---\nname: Demo\ndescription: d\nversion: \"1\"\n---\nbody\n"},
		{name: "missing version", directory: "demo-skill", content: "---\nname: demo-skill\ndescription: d\n---\nbody\n"},
		{name: "invalid required tool", directory: "demo-skill", content: "---\nname: demo-skill\ndescription: d\nversion: \"1\"\nrequired_tools:\n  - \"../bash\"\n---\nbody\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeSkill(t, t.TempDir(), test.directory, test.content)
			if _, err := parseSkillFile(path, testLimits()); err == nil {
				t.Fatal("err = nil")
			}
		})
	}
}

func TestParseSkillFileRejectsOversizedBody(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "demo-skill", validSkill)
	if _, err := parseSkillFile(path, Limits{MaxFileBytes: 64 * 1024, MaxBodyBytes: 4}); err == nil {
		t.Fatal("err = nil")
	}
}

func TestSkillFilesIgnoresSymlinkedDirectories(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	target := t.TempDir()
	writeSkill(t, target, "linked-skill", validSkill)
	if err := os.Symlink(filepath.Join(target, "linked-skill"), filepath.Join(root, "linked-skill")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	paths, err := skillFiles(root, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.Contains(paths[0], "demo-skill") {
		t.Fatalf("paths = %v", paths)
	}
}

func TestScanRegistryIsSortedAndImmutable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	writeSkill(t, root, "alpha-skill", strings.Replace(validSkill, "name: demo-skill", "name: alpha-skill", 1))
	registry, err := ScanRegistry(root, Config{Enabled: true, Dir: root, MaxSkills: 8, Limits: testLimits(), MaxIndexBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	metas, err := registry.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].Name != "alpha-skill" || metas[1].Name != "demo-skill" {
		t.Fatalf("metas = %#v", metas)
	}
	metas[0].Name = "mutated"
	metas[0].Tags[0] = "mutated"
	again, err := registry.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Name != "alpha-skill" || again[0].Tags[0] != "demo" {
		t.Fatalf("registry was mutated through List: %#v", again[0])
	}
}

func TestScanRegistryDisabledReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	registry, err := ScanRegistry(root, Config{Dir: root, MaxSkills: 8, Limits: testLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 0 {
		t.Fatalf("len = %d", registry.Len())
	}
}

func TestScanRegistryRejectsTooManySkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	writeSkill(t, root, "alpha-skill", strings.Replace(validSkill, "name: demo-skill", "name: alpha-skill", 1))
	if _, err := ScanRegistry(root, Config{Enabled: true, Dir: root, MaxSkills: 1, Limits: testLimits()}); err == nil {
		t.Fatal("err = nil")
	}
}

func TestLoadRejectsNonCanonicalNames(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	registry, err := ScanRegistry(root, Config{Enabled: true, Dir: root, MaxSkills: 8, Limits: testLimits()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../demo-skill", "demo-skill/SKILL.md", filepath.Join(root, "demo-skill")} {
		if _, err := registry.Load(context.Background(), name); err == nil {
			t.Fatalf("Load(%q) error = nil", name)
		}
	}
	if _, err := registry.Load(context.Background(), "missing-skill"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := registry.Load(context.Background(), "  DEMO-SKILL  "); err != nil {
		t.Fatalf("canonical lookup after normalization failed: %v", err)
	}
}

func TestIndexTruncatesAtWholeEntries(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	writeSkill(t, root, "alpha-skill", strings.Replace(validSkill, "name: demo-skill", "name: alpha-skill", 1))
	registry, err := ScanRegistry(root, Config{Enabled: true, Dir: root, MaxSkills: 8, Limits: testLimits()})
	if err != nil {
		t.Fatal(err)
	}
	full := registry.Index(4096)
	if lines := strings.Split(full, "\n"); len(lines) != 2 {
		t.Fatalf("index = %q", full)
	}
	partial := registry.Index(len(strings.Split(full, "\n")[0]) + 1)
	if strings.Contains(partial, "\n") || !strings.HasPrefix(partial, "alpha-skill | 2 | ") {
		t.Fatalf("partial index = %q", partial)
	}
	if registry.Index(0) != "" {
		t.Fatal("index must be empty for a zero budget")
	}
}

type fakeChecker map[string]bool

func (c fakeChecker) Has(name string) bool { return c[name] }

func TestValidateRequiredTools(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	registry, err := ScanRegistry(root, Config{Enabled: true, Dir: root, MaxSkills: 8, Limits: testLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequiredTools(registry, fakeChecker{"read": true, "bash": true}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequiredTools(registry, fakeChecker{"read": true}); err == nil {
		t.Fatal("err = nil for a missing required tool")
	}
}
