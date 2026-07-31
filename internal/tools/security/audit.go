package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type AuditEvent struct {
	Version      int       `json:"version"`
	Phase        string    `json:"phase"`
	InvocationID string    `json:"invocation_id"`
	TraceID      string    `json:"trace_id,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	SessionKey   string    `json:"session_key,omitempty"`
	Channel      string    `json:"channel,omitempty"`
	ToolName     string    `json:"tool_name"`
	Arguments    string    `json:"arguments,omitempty"`
	Decision     Decision  `json:"decision"`
	Rule         string    `json:"rule,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Approved     *bool     `json:"approved,omitempty"`
	Actor        string    `json:"actor,omitempty"`
	Output       string    `json:"output,omitempty"`
	Error        string    `json:"error,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

type Redactor interface {
	RedactArguments(toolName, raw string) string
	RedactOutput(toolName, raw string) string
}

type DefaultRedactor struct {
	maxArguments int
	maxOutput    int
}

func NewDefaultRedactor(maxArguments, maxOutput int) (*DefaultRedactor, error) {
	if maxArguments <= 0 || maxOutput <= 0 {
		return nil, errors.New("redactor limits must be positive")
	}
	return &DefaultRedactor{maxArguments: maxArguments, maxOutput: maxOutput}, nil
}

var secretPattern = regexp.MustCompile(`(?i)(authorization|api[_-]?key|token|password|secret)(["' ]*[:=]["' ]*)([^,"'\s}]+)`)

func redact(raw string, limit int) string {
	return truncate(secretPattern.ReplaceAllString(raw, `$1$2[REDACTED]`), limit)
}

func (r *DefaultRedactor) RedactArguments(_ string, raw string) string {
	return redact(raw, r.maxArguments)
}

func (r *DefaultRedactor) RedactOutput(_ string, raw string) string {
	return redact(raw, r.maxOutput)
}

type AuditSink interface {
	Write(context.Context, AuditEvent) error
}

type JSONLAuditSink struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

func NewJSONLAuditSink(path string) (*JSONLAuditSink, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("audit path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &JSONLAuditSink{file: file, enc: json.NewEncoder(file)}, nil
}

func (s *JSONLAuditSink) Write(ctx context.Context, event AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.file == nil {
		return errors.New("audit sink is closed")
	}
	if err := s.enc.Encode(event); err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	return nil
}

func (s *JSONLAuditSink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	syncErr := s.file.Sync()
	closeErr := s.file.Close()
	s.file = nil
	s.enc = nil
	return errors.Join(syncErr, closeErr)
}
