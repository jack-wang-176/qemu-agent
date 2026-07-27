package llm

import (
	"errors"
	"fmt"
	"strings"
)

type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (r ModelRef) String() string {
	return r.Provider + ":" + r.Model
}

func NormalizeModelRef(ref ModelRef) (ModelRef, error) {
	ref.Provider = strings.ToLower(strings.TrimSpace(ref.Provider))
	ref.Model = strings.TrimSpace(ref.Model)
	if ref.Provider == "" {
		return ModelRef{}, errors.New("model provider is empty")
	}
	if ref.Model == "" {
		return ModelRef{}, errors.New("model name is empty")
	}
	if strings.ContainsAny(ref.Provider, ":/ \t\r\n") {
		return ModelRef{}, fmt.Errorf("invalid model provider %q", ref.Provider)
	}
	return ref, nil
}

type ModelDefinition struct {
	Ref         ModelRef `json:"ref"`
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases,omitempty"`
	MaxContext  int      `json:"max_context"`
	MaxOutput   int      `json:"max_output,omitempty"`
	Tools       bool     `json:"tools"`
	Streaming   bool     `json:"streaming"`
}

type ResolvedModel struct {
	Definition ModelDefinition
	Provider   Provider
}

func normalizeAlias(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", errors.New("model alias is empty")
	}
	if strings.ContainsAny(value, " :/\t\r\n") {
		return "", fmt.Errorf("invalid model alias %q", raw)
	}
	return value, nil
}

func normalizeDefinition(def ModelDefinition) (ModelDefinition, error) {
	ref, err := NormalizeModelRef(def.Ref)
	if err != nil {
		return ModelDefinition{}, err
	}
	def.Ref = ref
	def.DisplayName = strings.TrimSpace(def.DisplayName)
	if def.DisplayName == "" {
		def.DisplayName = ref.String()
	}
	if def.MaxContext <= 0 {
		return ModelDefinition{}, errors.New("model max context must be > 0")
	}
	if def.MaxOutput < 0 || def.MaxOutput >= def.MaxContext {
		return ModelDefinition{}, fmt.Errorf("model max output must be >= 0 and < max context")
	}
	aliases := make([]string, 0, len(def.Aliases))
	seen := make(map[string]struct{}, len(def.Aliases))
	for _, raw := range def.Aliases {
		alias, err := normalizeAlias(raw)
		if err != nil {
			return ModelDefinition{}, err
		}
		if _, ok := seen[alias]; ok {
			return ModelDefinition{}, fmt.Errorf("duplicate model alias %q", alias)
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	def.Aliases = aliases
	return def, nil
}

func cloneModelDefinition(def ModelDefinition) ModelDefinition {
	def.Aliases = append([]string(nil), def.Aliases...)
	return def
}
