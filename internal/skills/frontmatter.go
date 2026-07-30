package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// frontmatter is the only accepted YAML shape. It is a separate type from Meta
// because Meta carries SHA256, which is computed here and must never be
// supplied by the file itself.
type frontmatter struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Version       string   `yaml:"version"`
	Tags          []string `yaml:"tags"`
	RequiredTools []string `yaml:"required_tools"`
}

var frontmatterDelimiter = []byte("---")

// parseSkillFile reads one SKILL.md and returns a fully validated Skill.
// Every rejection is an error rather than a partial skill: a half-parsed
// instruction file would be injected into the model context verbatim, so
// "skip the bad field" is not an acceptable outcome here.
func parseSkillFile(path string, limits Limits) (Skill, error) {
	if limits.MaxFileBytes <= 0 || limits.MaxBodyBytes <= 0 {
		return Skill{}, errors.New("skill limits must be > 0")
	}
	// Lstat, not Stat: a symlinked SKILL.md could point outside the skills
	// directory and turn the scan into an arbitrary file read.
	info, err := os.Lstat(path)
	if err != nil {
		return Skill{}, fmt.Errorf("stat skill file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Skill{}, errors.New("skill file must be a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > limits.MaxFileBytes {
		return Skill{}, fmt.Errorf("skill file size %d is outside limit %d", info.Size(), limits.MaxFileBytes)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read skill file: %w", err)
	}
	if int64(len(data)) > limits.MaxFileBytes {
		return Skill{}, fmt.Errorf("skill file grew beyond limit %d", limits.MaxFileBytes)
	}
	header, body, err := splitFrontmatter(data)
	if err != nil {
		return Skill{}, err
	}
	if len(body) > limits.MaxBodyBytes {
		return Skill{}, fmt.Errorf("skill body size %d exceeds limit %d", len(body), limits.MaxBodyBytes)
	}

	var raw frontmatter
	if err := yaml.UnmarshalWithOptions([]byte(header), &raw, yaml.DisallowUnknownField()); err != nil {
		return Skill{}, fmt.Errorf("parse skill frontmatter: %w", err)
	}
	meta, err := normalizeMeta(Meta{
		Name:          raw.Name,
		Description:   raw.Description,
		Version:       raw.Version,
		Tags:          raw.Tags,
		RequiredTools: raw.RequiredTools,
	})
	if err != nil {
		return Skill{}, err
	}
	// The directory name is what an operator sees and what the audit log
	// records, so it must agree with the name the model is allowed to call.
	if directory := filepath.Base(filepath.Dir(path)); directory != meta.Name {
		return Skill{}, fmt.Errorf("skill directory %q does not match skill name %q", directory, meta.Name)
	}
	digest := sha256.Sum256([]byte(body))
	meta.SHA256 = hex.EncodeToString(digest[:])
	return Skill{Meta: meta, Body: body}, nil
}

// splitFrontmatter requires the exact layout "---\n<yaml>\n---\n<body>".
// A tolerant parser here would let a file with no header be treated as pure
// body text and silently lose its name and required-tools contract.
func splitFrontmatter(data []byte) (string, string, error) {
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) == 0 || !isDelimiter(lines[0]) {
		return "", "", errors.New("skill file must start with a --- frontmatter delimiter")
	}
	for index := 1; index < len(lines); index++ {
		if !isDelimiter(lines[index]) {
			continue
		}
		header := string(bytes.Join(lines[1:index], []byte("\n")))
		if strings.TrimSpace(header) == "" {
			return "", "", errors.New("skill frontmatter is empty")
		}
		body := strings.TrimSpace(string(bytes.Join(lines[index+1:], []byte("\n"))))
		if body == "" {
			return "", "", errors.New("skill body is empty")
		}
		return header, body, nil
	}
	return "", "", errors.New("skill frontmatter is not terminated by ---")
}

func isDelimiter(line []byte) bool {
	return bytes.Equal(bytes.TrimRight(line, " \t\r"), frontmatterDelimiter)
}

// skillFiles lists <root>/<name>/SKILL.md candidates in sorted order. The scan
// is exactly one level deep so an operator cannot accidentally expose a whole
// source tree by pointing the skills directory at a repository.
func skillFiles(root string, maxSkills int) ([]string, error) {
	if maxSkills <= 0 {
		return nil, errors.New("max skills must be > 0")
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name(), FileName)
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("stat skill candidate: %w", err)
		}
		paths = append(paths, candidate)
		if len(paths) > maxSkills {
			return nil, fmt.Errorf("skill count exceeds %d", maxSkills)
		}
	}
	sort.Strings(paths)
	return paths, nil
}
