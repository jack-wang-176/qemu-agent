package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Sanitizer runs before persistence, never after. Once an item is on disk it is
// replayed into every future prompt, so rejecting content here is the only
// place where a secret or an instruction-shaped line can still be stopped.
type Sanitizer interface {
	Sanitize(raw string) (string, error)
}

var (
	ErrSensitiveContent = errors.New("memory content looks like a secret")
	ErrPromptControl    = errors.New("memory content contains prompt-control instructions")
	ErrEmptyContent     = errors.New("memory content is empty")
)

type DefaultSanitizer struct {
	maxBytes int
	patterns []*regexp.Regexp
}

// secretPatterns is deliberately small and high-signal. A broad rule (any long
// random-looking string) would reject legitimate register values and commit
// hashes, and a sanitizer users learn to fight is a sanitizer they disable.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-]{20,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|secret|token|passwd|password)\b\s*[:=]\s*\S{8,}`),
}

// promptControlPatterns catch text that tries to act as an instruction rather
// than a fact. The prompt overlay escapes its payload, so this is defence in
// depth against the model treating a remembered line as a directive.
var promptControlPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+|any\s+)?(previous|prior|above|earlier)\s+(instruction|prompt|rule|message)`),
	regexp.MustCompile(`(?i)disregard\s+(the\s+)?(above|previous|system)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)\b`),
	regexp.MustCompile(`(?i)</?(system|system_prompt|available_skills|memory_context|loaded_skill)\b`),
	regexp.MustCompile(`(?i)^\s*(system|assistant)\s*:`),
	regexp.MustCompile(`<\|.*?\|>`),
}

func NewDefaultSanitizer(maxBytes int) (*DefaultSanitizer, error) {
	if maxBytes <= 0 {
		return nil, errors.New("memory sanitizer max bytes must be > 0")
	}
	// The pattern list is copied into the value and the constructor is the only
	// way to obtain one, so a zero DefaultSanitizer can never be mistaken for a
	// working one: a silently pattern-less sanitizer accepts every secret it was
	// installed to reject.
	return &DefaultSanitizer{maxBytes: maxBytes, patterns: append([]*regexp.Regexp(nil), secretPatterns...)}, nil
}

// Sanitize returns the normalized content or an error whose text never contains
// the offending input: an audit log or a Telegram reply that echoes the matched
// substring would publish the very secret this check exists to contain.
func (s *DefaultSanitizer) Sanitize(raw string) (string, error) {
	if s == nil || s.maxBytes <= 0 {
		return "", errors.New("memory sanitizer is not configured")
	}
	value := normalizeText(raw)
	if value == "" {
		return "", ErrEmptyContent
	}
	if len(value) > s.maxBytes {
		return "", fmt.Errorf("memory content exceeds %d bytes", s.maxBytes)
	}
	for _, pattern := range s.patterns {
		if pattern.MatchString(value) {
			return "", ErrSensitiveContent
		}
	}
	for _, pattern := range promptControlPatterns {
		if pattern.MatchString(value) {
			return "", ErrPromptControl
		}
	}
	return value, nil
}

// normalizeText makes one canonical form used by hashing, indexing and storage.
// Without it the same fact typed with a zero-width space, a bidi override or a
// stray tab would produce a different fingerprint and defeat deduplication.
func normalizeText(raw string) string {
	replaced := strings.ToValidUTF8(raw, "")
	var builder strings.Builder
	builder.Grow(len(replaced))
	for _, r := range replaced {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			builder.WriteRune(' ')
		case isInvisible(r):
			// Dropped, not replaced: these runes exist only to make two
			// different strings look identical to a human reader.
		case unicode.IsControl(r):
		default:
			builder.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func isInvisible(r rune) bool {
	switch {
	case r == '\ufeff' || r == '\u00ad':
		return true
	case r >= '\u200b' && r <= '\u200f': // zero width and directional marks
		return true
	case r >= '\u202a' && r <= '\u202e': // bidi embedding and override
		return true
	case r >= '\u2060' && r <= '\u2064': // word joiner and invisible operators
		return true
	case r >= '\u2066' && r <= '\u2069': // bidi isolates
		return true
	default:
		return false
	}
}
