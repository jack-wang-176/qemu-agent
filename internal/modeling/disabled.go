package modeling

// disabled.go is the modeling capability turned off. It exists for the same
// reason memory.DisabledStore does: a build with QEMU_AGENT_MODELING_ENABLED
// unset still wires a complete object graph, so no request-path code contains
// `if r.modeling != nil`. Every method answers ErrDisabled, which the command
// layer maps to one sentence, so "off" is reported the same way everywhere.

import (
	"context"
	"io"
)

// DisabledRunner is a Runner that owns nothing and refuses everything.
type DisabledRunner struct{}

var _ Runner = DisabledRunner{}

func (DisabledRunner) Create(context.Context, string, Scope) (Project, error) {
	return Project{}, ErrDisabled
}

func (DisabledRunner) List(context.Context, Query) ([]Project, error) { return nil, ErrDisabled }

func (DisabledRunner) Show(context.Context, string, Scope) (Project, error) {
	return Project{}, ErrDisabled
}

func (DisabledRunner) Advance(context.Context, RunRequest) (RunResult, error) {
	return RunResult{}, ErrDisabled
}

func (DisabledRunner) Reset(context.Context, string, Stage, Scope) (Project, error) {
	return Project{}, ErrDisabled
}

func (DisabledRunner) Read(context.Context, string, ArtifactRef, Scope) ([]byte, error) {
	return nil, ErrDisabled
}

// DisabledApplier is the applier of a build that has no QEMU tree to write to.
// It is also what a deployment with modeling enabled but QemuRoot empty gets:
// generating code is useful on its own, landing it is not possible.
type DisabledApplier struct{}

var _ Applier = DisabledApplier{}

func (DisabledApplier) Plan(context.Context, string, Scope) (ApplyPlan, error) {
	return ApplyPlan{}, ErrApplyUnavailable
}

func (DisabledApplier) Apply(context.Context, string, Scope) (ApplyResult, error) {
	return ApplyResult{}, ErrApplyUnavailable
}

// DisabledProjects and DisabledArtifacts complete the set so build wiring can
// return a fully populated component struct. A disabled Pipeline is never
// constructed, so these are only ever reached by a mis-wire — which is exactly
// why they answer with an error instead of pretending to store something.
type DisabledProjects struct{}

var _ ProjectStore = DisabledProjects{}

func (DisabledProjects) Create(context.Context, Project) (Project, error) {
	return Project{}, ErrDisabled
}
func (DisabledProjects) Get(context.Context, string, Scope) (Project, error) {
	return Project{}, ErrDisabled
}
func (DisabledProjects) List(context.Context, Query) ([]Project, error) { return nil, ErrDisabled }
func (DisabledProjects) Save(context.Context, Project) (Project, error) {
	return Project{}, ErrDisabled
}
func (DisabledProjects) Delete(context.Context, string, Scope) error { return ErrDisabled }

type DisabledArtifacts struct{}

var _ ArtifactStore = DisabledArtifacts{}

func (DisabledArtifacts) Stage(context.Context, string, []Draft) (Batch, error) {
	return Batch{}, ErrDisabled
}
func (DisabledArtifacts) Commit(context.Context, Batch) ([]ArtifactRef, error) {
	return nil, ErrDisabled
}
func (DisabledArtifacts) Discard(context.Context, Batch) error { return ErrDisabled }
func (DisabledArtifacts) Open(context.Context, string, ArtifactRef) (io.ReadCloser, error) {
	return nil, ErrDisabled
}
