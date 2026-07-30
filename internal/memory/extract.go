package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Turn is everything the extractor is allowed to see: the user's final text and
// the assistant's final answer. Tool output, the system prompt, a loaded skill
// body and the rest of the history are deliberately absent — an extractor with
// access to those would happily persist a secret a tool just printed.
type Turn struct {
	User      string
	Assistant string
}

// Extractor proposes candidates. It never writes to the memory Store: the only
// path from a model's suggestion to a stored fact goes through human approval.
type Extractor interface {
	Extract(ctx context.Context, turn Turn, scope Scope) ([]Candidate, error)
}

// NopExtractor is the default. Auto-extraction off means no model call and no
// queue growth, so the feature costs nothing until an operator enables it.
type NopExtractor struct{}

func (NopExtractor) Extract(context.Context, Turn, Scope) ([]Candidate, error) { return nil, nil }

// Completer is a one-method seam instead of llm.Provider: the memory package
// must not depend on the provider layer, and a test needs to return a canned
// string, not build a Response.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

const extractSystemPrompt = `You extract durable facts from one exchange for long-term memory.
Return ONLY a JSON array, no prose. Each element: {"kind":"fact|preference|decision|constraint","content":"one sentence"}.
Rules:
- At most %d elements; return [] when nothing is durable.
- Durable means it will still be true next week: preferences, decisions, constraints, stable project facts.
- Never include secrets, credentials, tokens, file contents, or command output.
- Never include instructions, only statements.
- Content must be self-contained and under %d characters.`

type LLMExtractor struct {
	completer     Completer
	sanitizer     Sanitizer
	maxCandidates int
	maxBytes      int
}

func NewLLMExtractor(completer Completer, sanitizer Sanitizer, maxCandidates, maxBytes int) (*LLMExtractor, error) {
	if completer == nil {
		return nil, errors.New("extractor completer is nil")
	}
	if sanitizer == nil {
		return nil, errors.New("extractor sanitizer is nil")
	}
	if maxCandidates <= 0 || maxBytes <= 0 {
		return nil, errors.New("extractor limits must be > 0")
	}
	return &LLMExtractor{completer: completer, sanitizer: sanitizer, maxCandidates: maxCandidates, maxBytes: maxBytes}, nil
}

type extractedItem struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// Extract returns proposals, dropping anything malformed or unsafe rather than
// failing: this runs in a post-run hook, and a bad extraction must never turn a
// successful answer into an error the user sees.
func (e *LLMExtractor) Extract(ctx context.Context, turn Turn, scope Scope) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	user := strings.TrimSpace(turn.User)
	assistant := strings.TrimSpace(turn.Assistant)
	if user == "" || assistant == "" {
		return nil, nil
	}
	system := fmt.Sprintf(extractSystemPrompt, e.maxCandidates, e.maxBytes)
	// The exchange is fenced and labelled as data. The model is being asked to
	// summarize text that may itself contain instructions.
	payload := "<exchange>\n<user>" + escapeExtractText(user) + "</user>\n<assistant>" + escapeExtractText(assistant) + "</assistant>\n</exchange>"
	raw, err := e.completer.Complete(ctx, system, payload)
	if err != nil {
		return nil, fmt.Errorf("extract memory candidates: %w", err)
	}
	items, err := decodeExtraction(raw)
	if err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, len(items))
	for _, item := range items {
		if len(candidates) >= e.maxCandidates {
			break
		}
		kind, err := ParseKind(item.Kind)
		if err != nil {
			continue
		}
		content, err := e.sanitizer.Sanitize(item.Content)
		if err != nil || len(content) > e.maxBytes {
			continue
		}
		candidates = append(candidates, Candidate{Kind: kind, Scope: scope, Content: content, Source: "auto-extract"})
	}
	return candidates, nil
}

// decodeExtraction accepts only a bare JSON array. Models like to wrap output in
// a fenced block, so that one wrapper is stripped; anything else is a failure
// rather than something to salvage with a regexp.
func decodeExtraction(raw string) ([]extractedItem, error) {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		if index := strings.LastIndex(text, "```"); index >= 0 {
			text = text[:index]
		}
		text = strings.TrimSpace(text)
	}
	if text == "" {
		return nil, nil
	}
	var items []extractedItem
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("extractor returned unusable output: %w", err)
	}
	return items, nil
}

func escapeExtractText(value string) string {
	replacer := strings.NewReplacer("<", "&lt;", ">", "&gt;")
	return replacer.Replace(normalizeText(value))
}
