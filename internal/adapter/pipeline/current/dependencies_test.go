package current

import (
	"context"
	"errors"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

type dependencyRunnerFake struct {
	showCalls int
	project   modeling.Project
}

func (f *dependencyRunnerFake) Show(context.Context, string, modeling.Scope) (modeling.Project, error) {
	f.showCalls++
	return f.project, nil
}

func (*dependencyRunnerFake) Advance(context.Context, modeling.RunRequest) (modeling.RunResult, error) {
	return modeling.RunResult{}, errors.New("not used by inspect test")
}

func TestNewEngineRejectsMissingEngineDependency(t *testing.T) {
	if _, err := NewEngine(Dependencies{}); err == nil {
		t.Fatal("NewEngine accepted missing engine dependency")
	}
}

func TestInspectUsesInjectedEngineRunner(t *testing.T) {
	fake := &dependencyRunnerFake{project: modeling.Project{ID: "project-1", WorkspaceID: "workspace-1"}}
	engine, err := NewEngine(Dependencies{Engine: fake})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	view, err := engine.Inspect(context.Background(), pipelineapi.InspectRequest{
		Scope:     pipelineapi.Scope{WorkspaceID: "workspace-1"},
		ProjectID: "project-1",
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if fake.showCalls != 1 {
		t.Fatalf("Show calls = %d, want 1", fake.showCalls)
	}
	if view.ProjectID != "project-1" {
		t.Fatalf("ProjectID = %q, want project-1", view.ProjectID)
	}
}

var _ engineRunner = (*dependencyRunnerFake)(nil)
