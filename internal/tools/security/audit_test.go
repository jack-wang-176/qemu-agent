package security

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONLAuditSinkWritesJSONLinesAndClosesIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "tools.jsonl")
	sink, err := NewJSONLAuditSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(context.Background(), AuditEvent{Version: 1, Phase: "authorized", InvocationID: "one", ToolName: "fake", Decision: DecisionAllow}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(context.Background(), AuditEvent{Version: 1, Phase: "completed", InvocationID: "one", ToolName: "fake", Decision: DecisionAllow}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines=%d raw=%q", len(lines), raw)
	}
	for _, line := range lines {
		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
	}
	if err := sink.Write(context.Background(), AuditEvent{}); err == nil {
		t.Fatal("write after close error=nil")
	}
}

func TestDefaultRedactorRedactsAndTruncates(t *testing.T) {
	redactor, err := NewDefaultRedactor(40, 30)
	if err != nil {
		t.Fatal(err)
	}
	arguments := redactor.RedactArguments("fake", `{"token":"secret-value","other":"abcdefghijklmnopqrstuvwxyz"}`)
	if strings.Contains(arguments, "secret-value") || !strings.Contains(arguments, "[REDACTED]") || !strings.Contains(arguments, "[truncated]") {
		t.Fatalf("arguments=%q", arguments)
	}
}
