package skills

import (
	"context"
	"strings"
	"testing"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	root := t.TempDir()
	writeSkill(t, root, "demo-skill", validSkill)
	registry, err := ScanRegistry(root, Config{Enabled: true, Dir: root, MaxSkills: 8, Limits: testLimits(), MaxIndexBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestNewUseSkillToolRequiresSkills(t *testing.T) {
	if _, err := NewUseSkillTool(nil, 4096); err == nil {
		t.Fatal("err = nil for a nil registry")
	}
	empty, err := ScanRegistry(t.TempDir(), Config{Enabled: true, MaxSkills: 8, Limits: testLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewUseSkillTool(empty, 4096); err == nil {
		t.Fatal("err = nil for an empty registry")
	}
}

func TestUseSkillToolExecuteSplitsOutputs(t *testing.T) {
	tool, err := NewUseSkillTool(testRegistry(t), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Dangerous() {
		t.Fatal("loading a skill must not be classified dangerous")
	}
	if !strings.Contains(tool.Description(), "demo-skill") {
		t.Fatalf("description does not advertise the index: %q", tool.Description())
	}
	if _, err := tool.Spec().Parameter(); err != nil {
		t.Fatalf("spec = %v", err)
	}
	result, err := tool.Execute(context.Background(), `{"name":"demo-skill"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.ModelOutput, "Step one.") || !strings.Contains(result.ModelOutput, "<loaded_skill>") {
		t.Fatalf("model output = %q", result.ModelOutput)
	}
	if strings.Contains(result.PersistentOutput, "Step one.") {
		t.Fatalf("receipt leaked the instructions: %q", result.PersistentOutput)
	}
	if !strings.Contains(result.PersistentOutput, "sha256=") || result.PersistentOutput == result.ModelOutput {
		t.Fatalf("persistent output = %q", result.PersistentOutput)
	}
	normalized, err := result.Normalize()
	if err != nil || normalized != result {
		t.Fatalf("normalize changed a complete result: %#v err=%v", normalized, err)
	}
}

func TestUseSkillToolRejectsBadArguments(t *testing.T) {
	tool, err := NewUseSkillTool(testRegistry(t), 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{}`,
		`{"name":"demo-skill","extra":1}`,
		`{"name":"../demo-skill"}`,
		`{"name":"missing"}`,
		`{"name":"demo-skill"} {"name":"demo-skill"}`,
		`not json`,
	} {
		if _, err := tool.Execute(context.Background(), raw); err == nil {
			t.Fatalf("Execute(%q) error = nil", raw)
		}
	}
}
