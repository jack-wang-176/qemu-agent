package build

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/contextmgr"
	"github.com/jack-wang-176/qemu-agent/internal/llm"
	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
	"github.com/jack-wang-176/qemu-agent/internal/prompt"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

type workflowModeling struct{ modelingapi.Service }
type workflowSessionStore struct{ session.Store }

type workflowContext struct{}

func (workflowContext) EnforceBudget(_ context.Context, _ contextmgr.ModelBudget, messages []llm.Message) ([]llm.Message, int, error) {
	return append([]llm.Message(nil), messages...), 1, nil
}

type workflowPrompts struct{}

func (workflowPrompts) Prepare(context.Context, prompt.ContextQuery) (prompt.Snapshot, error) {
	return prompt.Snapshot{}, nil
}
func (workflowPrompts) Build(_ context.Context, in prompt.Input) (prompt.Plan, error) {
	return prompt.Plan{Messages: append([]llm.Message(nil), in.Persistent...)}, nil
}

type workflowResolver struct{ resolved llm.ResolvedModel }

func (r workflowResolver) Resolve(llm.ModelRef) (llm.ResolvedModel, error) { return r.resolved, nil }
func (r workflowResolver) ResolveName(string) (llm.ResolvedModel, error)   { return r.resolved, nil }

type workflowProvider struct{}

func (workflowProvider) Name() string                 { return "test" }
func (workflowProvider) Capability() llm.Capabilities { return llm.Capabilities{MaxContext: 4096} }
func (workflowProvider) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return nil, errors.New("unused")
}
func (workflowProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("unused")
}

func workflowBuildConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	return config.Config{
		Modeling: config.ModelingConfig{
			Enabled: true, Dir: filepath.Join(root, "modeling"), MaxProjects: 10,
			MaxArtifactBytes: 1024, MaxProjectBytes: 4096, StageTimeout: time.Minute,
		},
		Prompt: config.PromptConfig{MaxInjectedBytes: 4096},
		Memory: config.MemoryConfig{TopK: 4},
	}
}

func TestBuildModelingWorkflowBuildsWithoutInstallingRunner(t *testing.T) {
	ref := llm.ModelRef{Provider: "test", Model: "model"}
	components, err := BuildModelingWorkflow(ModelingWorkflowInput{
		Config: workflowBuildConfig(t), Modeling: workflowModeling{},
		Models: workflowResolver{resolved: llm.ResolvedModel{
			Definition: llm.ModelDefinition{Ref: ref, MaxContext: 4096}, Provider: workflowProvider{},
		}},
		DefaultModel: ref, Context: workflowContext{}, Prompts: workflowPrompts{},
		Store: workflowSessionStore{}, Logger: testLogger(t), NewID: func() string { return "request-1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if components.Workflow == nil || components.Runner == nil {
		t.Fatalf("incomplete workflow components: %#v", components)
	}
}

func TestBuildModelingWorkflowRejectsProductConfig(t *testing.T) {
	cfg := workflowBuildConfig(t)
	cfg.Modeling.AutoApply = true
	cfg.Modeling.QemuRoot = t.TempDir()
	_, err := BuildModelingWorkflow(ModelingWorkflowInput{Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "AUTO_APPLY=false") {
		t.Fatalf("BuildModelingWorkflow error = %v", err)
	}
}
