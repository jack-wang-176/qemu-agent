package session

import (
	"encoding/json"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/llm"
)

func TestSessionJSONCompatibility(t *testing.T) {
	legacy := `{"id":"id","trace_id":"trace","model":"qwen","messages":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	var sess Session
	if err := json.Unmarshal([]byte(legacy), &sess); err != nil {
		t.Fatal(err)
	}
	if sess.ModelRef.Provider != "" || sess.ModelRef.Model != "qwen" {
		t.Fatalf("model ref = %#v", sess.ModelRef)
	}
	sess.ModelRef.Provider = "ollama"
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if _, legacyExists := wire["model"]; legacyExists || wire["model_ref"] == nil {
		t.Fatalf("json = %s", data)
	}
}

func TestSessionJSONRejectsConflictingFields(t *testing.T) {
	data := `{"id":"id","trace_id":"trace","model":"old","model_ref":{"provider":"ollama","model":"new"},"messages":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	var sess Session
	if err := json.Unmarshal([]byte(data), &sess); err == nil {
		t.Fatal("conflicting fields accepted")
	}
}

func TestNewSessionStoresModelRef(t *testing.T) {
	ref := llm.ModelRef{Provider: "ollama", Model: "qwen"}
	if got := NewSession("trace", "system", ref).ModelRef; got != ref {
		t.Fatalf("ModelRef = %#v", got)
	}
}
