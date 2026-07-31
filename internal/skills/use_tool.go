package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/tools"
	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

// UseSkillTool is a normal Tool. The agent loop has no branch for it: it is
// routed through Catalog -> Policy -> Executor -> Audit like read or bash,
// which is what keeps the skill load auditable.
type UseSkillTool struct {
	registry *Registry
	index    string
}

// NewUseSkillTool captures the skill index at construction time. Until I7-G
// gives the PromptAssembler a real skills section, the tool description is the
// only channel that tells the model which names exist — that placement is
// temporary and is expected to move into the system prompt.
func NewUseSkillTool(registry *Registry, maxIndexBytes int) (*UseSkillTool, error) {
	if registry == nil || registry.Len() == 0 {
		return nil, errors.New("use_skill needs a non-empty skill registry")
	}
	return &UseSkillTool{registry: registry, index: registry.Index(maxIndexBytes)}, nil
}

func (*UseSkillTool) Name() string { return "use_skill" }

func (t *UseSkillTool) Description() string {
	description := "Load one registered skill by exact name when its workflow is relevant. " +
		"Available skills (name | version | description):"
	if t.index == "" {
		return description + " none"
	}
	return description + "\n" + t.index
}

// Dangerous stays false: loading a skill reads no workspace file and mutates
// nothing. The instructions it returns are still data, not a new policy.
func (*UseSkillTool) Dangerous() bool { return false }

func (t *UseSkillTool) Spec() schema.Spec {
	return schema.NewSpec(t.Name(), t.Description(), schema.Object(
		schema.Required("name", schema.String("Exact skill name from the available skill index")),
	))
}

type useSkillArgs struct {
	Name string `json:"name"`
}

// Execute returns the two projections of one call: the full instructions for
// this request only, and a short receipt for the persisted transcript. The
// body can be tens of kilobytes, and replaying it in every later request is
// what silently exhausts the context budget.
func (t *UseSkillTool) Execute(ctx context.Context, raw string) (tools.ExecutionResult, error) {
	args, err := tools.DecodeArgs[useSkillArgs](raw)
	if err != nil {
		return tools.ExecutionResult{}, fmt.Errorf("use_skill: %w", err)
	}
	item, err := t.registry.Load(ctx, args.Name)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	modelOutput := strings.Join([]string{
		"<loaded_skill>",
		"name: " + item.Meta.Name,
		"version: " + item.Meta.Version,
		"sha256: " + item.Meta.SHA256,
		"instructions:",
		item.Body,
		"</loaded_skill>",
	}, "\n")
	receipt := fmt.Sprintf(
		"Skill %s version=%s sha256=%s was loaded for this run; full instructions were transient.",
		item.Meta.Name, item.Meta.Version, item.Meta.SHA256,
	)
	return tools.ExecutionResult{ModelOutput: modelOutput, PersistentOutput: receipt}, nil
}
