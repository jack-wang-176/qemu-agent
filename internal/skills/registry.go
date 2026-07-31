package skills

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Registry is immutable after ScanRegistry returns. That is what removes the
// need for any lock: the maps are never written again, and every accessor
// returns deep copies, so concurrent sessions share one read-only snapshot.
type Registry struct {
	byName  map[string]Skill
	ordered []Meta
}

// ScanRegistry parses every skill under root once, at process start. Scanning
// per turn would let the instruction set change under a running conversation
// and would make the sha256 in the audit log meaningless.
func ScanRegistry(root string, cfg Config) (*Registry, error) {
	registry := &Registry{byName: make(map[string]Skill)}
	if !cfg.Enabled {
		return registry, nil
	}
	paths, err := skillFiles(root, cfg.MaxSkills)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		item, err := parseSkillFile(path, cfg.Limits)
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", path, err)
		}
		if _, exists := registry.byName[item.Meta.Name]; exists {
			return nil, fmt.Errorf("duplicate skill name %q", item.Meta.Name)
		}
		registry.byName[item.Meta.Name] = item
		registry.ordered = append(registry.ordered, cloneMeta(item.Meta))
	}
	// Sorted by name so the index, and therefore the prompt prefix, is byte
	// identical across restarts and stays cacheable.
	sort.Slice(registry.ordered, func(i, j int) bool {
		return registry.ordered[i].Name < registry.ordered[j].Name
	})
	return registry, nil
}

func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.byName)
}

func (r *Registry) List(ctx context.Context) ([]Meta, error) {
	if r == nil {
		return nil, errors.New("skill registry is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cloneMetas(r.ordered), nil
}

// Load takes a canonical skill name only. It never accepts a path, so the
// model cannot steer this into an arbitrary file read.
func (r *Registry) Load(ctx context.Context, name string) (Skill, error) {
	if r == nil {
		return Skill{}, errors.New("skill registry is nil")
	}
	if err := ctx.Err(); err != nil {
		return Skill{}, err
	}
	normalized, err := normalizeName(name)
	if err != nil {
		return Skill{}, err
	}
	item, ok := r.byName[normalized]
	if !ok {
		return Skill{}, SkillNotFoundError{Name: normalized}
	}
	return cloneSkill(item), nil
}

// Index renders the model-visible catalogue: one line per skill, truncated at
// a whole entry boundary. Cutting mid-line would hand the model a half name it
// would then try to load.
func (r *Registry) Index(maxBytes int) string {
	if r == nil || len(r.ordered) == 0 || maxBytes <= 0 {
		return ""
	}
	var builder strings.Builder
	for _, meta := range r.ordered {
		line := meta.Name + " | " + meta.Version + " | " + meta.Description
		if len(meta.RequiredTools) > 0 {
			line += " | requires: " + strings.Join(meta.RequiredTools, ",")
		}
		if builder.Len() > 0 {
			line = "\n" + line
		}
		if builder.Len()+len(line) > maxBytes {
			break
		}
		builder.WriteString(line)
	}
	return builder.String()
}

// ToolChecker is the consumer-side view of the tool manager. Declaring it here
// keeps skills independent of the concrete tools.Manager and lets tests pass a
// two-line fake.
type ToolChecker interface {
	Has(name string) bool
}

// ValidateRequiredTools fails startup when a skill promises a workflow the
// process cannot execute. Discovering this mid-run would surface as a confusing
// tool-not-found error long after the skill was loaded.
func ValidateRequiredTools(registry *Registry, catalog ToolChecker) error {
	if registry == nil || len(registry.ordered) == 0 {
		return nil
	}
	if catalog == nil {
		return errors.New("tool catalog is nil")
	}
	for _, meta := range registry.ordered {
		for _, tool := range meta.RequiredTools {
			if !catalog.Has(tool) {
				return fmt.Errorf("skill %q requires unregistered tool %q", meta.Name, tool)
			}
		}
	}
	return nil
}
