package llm

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrModelNotFound = errors.New("model not found")

type ModelNotFoundError struct {
	Query string
}

func (e ModelNotFoundError) Error() string {
	return fmt.Sprintf("model %q not found", e.Query)
}

func (e ModelNotFoundError) Unwrap() error { return ErrModelNotFound }

type ModelResolver interface {
	Resolve(ModelRef) (ResolvedModel, error)
}

type ModelCatalog interface {
	ResolveName(string) (ResolvedModel, error)
	List() []ModelDefinition
}

type ModelRegistry struct {
	byRef   map[ModelRef]ResolvedModel
	aliases map[string]ModelRef
	ordered []ModelDefinition
	sealed  bool
}

func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		byRef:   make(map[ModelRef]ResolvedModel),
		aliases: make(map[string]ModelRef),
	}
}

func (r *ModelRegistry) Register(def ModelDefinition, provider Provider) error {
	if r == nil {
		return errors.New("model registry is nil")
	}
	if r.sealed {
		return errors.New("model registry is sealed")
	}
	if provider == nil {
		return errors.New("model provider is nil")
	}
	normalized, err := normalizeDefinition(def)
	if err != nil {
		return err
	}
	providerName := strings.ToLower(strings.TrimSpace(provider.Name()))
	if providerName != normalized.Ref.Provider {
		return fmt.Errorf("provider mismatch: definition=%q provider=%q", normalized.Ref.Provider, provider.Name())
	}
	if _, exists := r.byRef[normalized.Ref]; exists {
		return fmt.Errorf("model %q already registered", normalized.Ref.String())
	}
	for _, alias := range normalized.Aliases {
		if current, exists := r.aliases[alias]; exists {
			return fmt.Errorf("model alias %q already maps to %q", alias, current.String())
		}
		if alias == strings.ToLower(normalized.Ref.String()) {
			return fmt.Errorf("model alias %q conflicts with model ref", alias)
		}
	}

	stored := cloneModelDefinition(normalized)
	r.byRef[stored.Ref] = ResolvedModel{Definition: stored, Provider: provider}
	for _, alias := range stored.Aliases {
		r.aliases[alias] = stored.Ref
	}
	r.ordered = append(r.ordered, stored)
	return nil
}

func (r *ModelRegistry) Seal() error {
	if r == nil {
		return errors.New("model registry is nil")
	}
	if r.sealed {
		return nil
	}
	if len(r.byRef) == 0 {
		return errors.New("model registry is empty")
	}
	sort.Slice(r.ordered, func(i, j int) bool {
		return r.ordered[i].Ref.String() < r.ordered[j].Ref.String()
	})
	r.sealed = true
	return nil
}

func (r *ModelRegistry) Resolve(ref ModelRef) (ResolvedModel, error) {
	if r == nil {
		return ResolvedModel{}, errors.New("model registry is nil")
	}
	normalized, err := NormalizeModelRef(ref)
	if err != nil {
		return ResolvedModel{}, err
	}
	resolved, ok := r.byRef[normalized]
	if !ok {
		return ResolvedModel{}, ModelNotFoundError{Query: normalized.String()}
	}
	resolved.Definition = cloneModelDefinition(resolved.Definition)
	return resolved, nil
}

func (r *ModelRegistry) EffectiveDefinition(def ModelDefinition, provider Provider) (ModelDefinition, error) {
	if provider == nil {
		return ModelDefinition{}, errors.New("model provider is nil")
	}
	capability := provider.Capability()
	if def.Tools && !capability.Tools {
		return ModelDefinition{}, fmt.Errorf("model %q enables tools but provider %q does not support them", def.Ref.String(), provider.Name())
	}
	if def.Streaming && !capability.Streaming {
		return ModelDefinition{}, fmt.Errorf("model %q enables streaming but provider %q does not support it", def.Ref.String(), provider.Name())
	}
	if capability.MaxContext > 0 && capability.MaxContext < def.MaxContext {
		def.MaxContext = capability.MaxContext
		if def.MaxOutput >= def.MaxContext {
			return ModelDefinition{}, fmt.Errorf("model %q max output exceeds effective context", def.Ref.String())
		}
	}
	return def, nil
}

func (r *ModelRegistry) ResolveName(raw string) (ResolvedModel, error) {
	query := strings.TrimSpace(raw)
	if query == "" {
		return ResolvedModel{}, ModelNotFoundError{Query: raw}
	}
	if ref, ok := r.aliases[strings.ToLower(query)]; ok {
		return r.Resolve(ref)
	}
	provider, model, ok := strings.Cut(query, ":")
	if !ok {
		return ResolvedModel{}, ModelNotFoundError{Query: raw}
	}
	ref, err := NormalizeModelRef(ModelRef{Provider: provider, Model: model})
	if err != nil {
		return ResolvedModel{}, err
	}
	return r.Resolve(ref)
}

func (r *ModelRegistry) List() []ModelDefinition {
	if r == nil {
		return nil
	}
	result := make([]ModelDefinition, len(r.ordered))
	for i, def := range r.ordered {
		result[i] = cloneModelDefinition(def)
	}
	return result
}
