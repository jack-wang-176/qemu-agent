package schema_test

import (
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

func TestReadSchema(t *testing.T) {
	spec := schema.NewSpec("read", "Read", schema.Object(schema.Required("file_path", schema.String("Path"))))
	got, err := spec.Parameter()
	if err != nil {
		t.Fatal(err)
	}
	props := got["properties"].(map[string]any)
	if props["file_path"] == nil {
		t.Fatal("missing file_path")
	}
	required := got["required"].([]string)
	if len(required) != 1 || required[0] != "file_path" {
		t.Fatal(required)
	}
}

func TestRejectsDuplicateField(t *testing.T) {
	spec := schema.NewSpec("bad", "bad", schema.Object(schema.Required("x", schema.String("one")), schema.Optional("x", schema.Integar("two"))))
	if _, err := spec.Parameter(); err == nil {
		t.Fatal("expected error")
	}
}
