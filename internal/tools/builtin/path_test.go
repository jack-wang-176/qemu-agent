package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToolRejectsPathOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(workspace, 10)
	_, err := tool.Execute(context.Background(), `{"file_path":"`+outside+`"}`)
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestReadToolTruncatesLines(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("1\n2\n3"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(workspace, 2)
	result, err := tool.Execute(context.Background(), `{"file_path":"data.txt"}`)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.ModelOutput, "truncated after 2 lines") {
		t.Fatalf("result = %#v", result)
	}
	if result.PersistentOutput != result.ModelOutput {
		t.Fatalf("read must persist what the model saw: %#v", result)
	}
}
