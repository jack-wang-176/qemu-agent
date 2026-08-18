package current

import (
	"context"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
)

// engineRunner is the minimum current modeling surface consumed by Engine.
type engineRunner interface {
	Show(context.Context, string, modeling.Scope) (modeling.Project, error)
	Advance(context.Context, modeling.RunRequest) (modeling.RunResult, error)
}

// queryRunner is the current read surface. Its signatures must match the
// modeling implementation before a compile-time assertion is added.
type queryRunner interface {
	List(context.Context, modeling.Query) ([]modeling.Project, error)
	Show(context.Context, string, modeling.Scope) (modeling.Project, error)
	Read(context.Context, string, modeling.ArtifactRef, modeling.Scope) ([]byte, error)
}

type Dependencies struct {
	Engine engineRunner
	Query  queryRunner
}

var _ engineRunner = (*modeling.Pipeline)(nil)
var _ queryRunner = (*modeling.Pipeline)(nil)
