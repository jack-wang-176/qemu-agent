package skills

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MaxDescriptionRunes bounds the only skill field that is always injected into
// the model context, so a single skill can never dominate the index.
const MaxDescriptionRunes = 300

// FileName is the fixed leaf every skill directory must provide. A fixed name
// keeps the scan one level deep and removes any path guessing at load time.
const FileName = "SKILL.md"

type Meta struct {
	Name          string   `json:"name" yaml:"name"`
	Description   string   `json:"description" yaml:"description"`
	Version       string   `json:"version" yaml:"version"`
	Tags          []string `json:"tags,omitempty" yaml:"tags"`
	RequiredTools []string `json:"required_tools,omitempty" yaml:"required_tools"`
	SHA256        string   `json:"sha256" yaml:"-"`
}

type Skill struct {
	Meta Meta
	Body string
}

type Limits struct {
	MaxFileBytes int64
	MaxBodyBytes int
}

// Config is the skills view of the process configuration. The package never
// reads environment variables itself: app.Build translates config.SkillConfig
// into this struct so the registry stays testable with plain literals.
type Config struct {
	Enabled       bool
	Dir           string
	MaxSkills     int
	Limits        Limits
	MaxIndexBytes int
}

var (
	skillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	toolNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

// ErrSkillNotFound lets callers test for a missing skill with errors.Is while
// SkillNotFoundError still carries the requested name for the model.
var ErrSkillNotFound = errors.New("skill not found")

type SkillNotFoundError struct {
	Name string
}

func (e SkillNotFoundError) Error() string {
	return fmt.Sprintf("skill %q not found", e.Name)
}

func (SkillNotFoundError) Unwrap() error { return ErrSkillNotFound }

// normalizeName is the single gate between a model-supplied string and a
// registry lookup. It accepts canonical names only, so a value such as
// "../../etc/passwd" or "skills/foo/SKILL.md" can never reach the filesystem.
func normalizeName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", errors.New("skill name is empty")
	}
	if !skillNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q", raw)
	}
	return name, nil
}

// normalizeMeta returns a validated copy of meta with tags and required tools
// lowercased, de-duplicated and sorted, so two skill files that differ only in
// list order produce an identical index and an identical audit trail.
func normalizeMeta(meta Meta) (Meta, error) {
	name, err := normalizeName(meta.Name)
	if err != nil {
		return Meta{}, err
	}
	meta.Name = name
	meta.Description = strings.TrimSpace(meta.Description)
	if meta.Description == "" {
		return Meta{}, errors.New("skill description is empty")
	}
	if len([]rune(meta.Description)) > MaxDescriptionRunes {
		return Meta{}, fmt.Errorf("skill description exceeds %d runes", MaxDescriptionRunes)
	}
	if strings.ContainsAny(meta.Description, "\n\r") {
		return Meta{}, errors.New("skill description must be a single line")
	}
	meta.Version = strings.TrimSpace(meta.Version)
	if meta.Version == "" {
		return Meta{}, errors.New("skill version is empty")
	}
	tags, err := normalizeList(meta.Tags, skillNamePattern, "tag")
	if err != nil {
		return Meta{}, err
	}
	tools, err := normalizeList(meta.RequiredTools, toolNamePattern, "required tool")
	if err != nil {
		return Meta{}, err
	}
	meta.Tags = tags
	meta.RequiredTools = tools
	return meta, nil
}

func normalizeList(values []string, pattern *regexp.Regexp, kind string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if !pattern.MatchString(value) {
			return nil, fmt.Errorf("invalid skill %s %q", kind, raw)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// cloneMeta and friends keep the registry immutable: every value that leaves
// the registry is a deep copy, so a tool that mutates a returned slice cannot
// corrupt the scan result shared by all sessions.
func cloneMeta(meta Meta) Meta {
	meta.Tags = append([]string(nil), meta.Tags...)
	meta.RequiredTools = append([]string(nil), meta.RequiredTools...)
	return meta
}

func cloneMetas(metas []Meta) []Meta {
	if metas == nil {
		return nil
	}
	result := make([]Meta, 0, len(metas))
	for _, meta := range metas {
		result = append(result, cloneMeta(meta))
	}
	return result
}

func cloneSkill(item Skill) Skill {
	return Skill{Meta: cloneMeta(item.Meta), Body: item.Body}
}
