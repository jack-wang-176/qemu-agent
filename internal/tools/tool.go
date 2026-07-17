package tools

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/tools/schema"
)

type Tool interface {
	Name() string
	Description() string
	Spec() schema.Spec
	Dangerous() bool
	Execute(ctx context.Context, args string) (string, error)
}
