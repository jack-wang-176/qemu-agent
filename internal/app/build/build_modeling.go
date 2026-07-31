// Package build, modeling half: assembling the device-modeling pipeline.
//
// This file is the one place that decides whether this process can write into a
// QEMU source tree. That makes it the most safety-relevant wiring in the package,
// and its shape follows build_knowledge.go for a reason: every field of the
// returned value is given a working, refusing default *first*, and only then
// replaced according to configuration. Nothing downstream ever holds nil, so no
// command and no stage contains "is modeling on?" — that question is answered
// exactly once, here, at startup.
//
// There are three capability levels rather than two, and they are independent:
//
//   - modeling disabled — everything is the Disabled* pair; /modeling answers
//     "modeling is disabled" instead of the command family being absent.
//   - enabled without QemuRoot — the pipeline runs, plans, extracts and infers,
//     but Emit's landing and Verify's build have no target, so Applier stays
//     DisabledApplier. This is the useful configuration for reading a datasheet
//     on a machine that has no QEMU checkout.
//   - enabled with QemuRoot — the applier is real, and every write it makes goes
//     through a security.Executor whose write tool is rooted at that tree.
package build

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/tools/security"
)

// ModelingInput keeps the seams a test needs to control. Completer and Executor
// are the two dependencies that cannot be built from configuration alone: the
// first is a model, the second is the process-wide audited side-effect path, and
// modeling must reuse both rather than construct its own.
type ModelingInput struct {
	Config   config.Config
	Logger   *slog.Logger
	Executor ToolExecutor // the only side-effect exit; nil is an error when enabled
	// Completer is the model the stages call. It is reused from the knowledge
	// layer's ProviderCompleter rather than built here, so a deployment has one
	// answer to "which model does background work use".
	Completer modeling.Completer
	NewID     func() string
	Now       func() time.Time
}

// ToolExecutor is security.Executor as this file needs it. It is declared next to
// the consumer so a test can supply a recording double without constructing a
// policy, an approver and an audit sink.
type ToolExecutor interface {
	Execute(ctx context.Context, in security.Invocation) (security.Result, error)
}

// ModelingComponents is the whole modeling layer as one value.
//
// The Application only ever receives Runner and Applier — the two narrow
// interfaces. Projects and Artifacts are returned for the integration tests and
// for a future maintenance command; no request-path code may take them, because
// a command that could call ProjectStore.Save would be able to change project
// state without going through a stage.
type ModelingComponents struct {
	Projects  modeling.ProjectStore
	Artifacts modeling.ArtifactStore
	Runner    modeling.Runner
	Applier   modeling.Applier
	// WorkspaceID is the scope modeling projects are stored under. It is derived
	// by the same WorkspaceID helper the knowledge layer uses, because two
	// capabilities in one directory must agree on the id or a project created by
	// one becomes invisible to the other.
	WorkspaceID string
}

// The two subdirectories of Modeling.Dir. They are split because the stores have
// different lifetimes: a project's JSON is rewritten on every stage run, while an
// artifact is content-addressed and never rewritten at all. Keeping them apart
// means a future "prune old artifacts" cannot walk over project state.
func projectsRoot(dir string) string  { return filepath.Join(dir, "projects") }
func artifactsRoot(dir string) string { return filepath.Join(dir, "artifacts") }

// BuildModeling assembles the layer. It returns a usable value even on error, so
// a caller that chooses to log and continue is not left holding nil.
func BuildModeling(in ModelingInput) (ModelingComponents, error) {
	// 1: safe defaults. Every one of these answers every call with ErrDisabled or
	// an empty list, which is what makes the early returns below harmless.
	components := ModelingComponents{
		Projects:  modeling.DisabledProjects{},
		Artifacts: modeling.DisabledArtifacts{},
		Runner:    modeling.DisabledRunner{},
		Applier:   modeling.DisabledApplier{},
	}
	if in.Logger == nil {
		return components, errors.New("modeling logger is nil")
	}
	if in.NewID == nil {
		// Not uuid.NewString: the project store's ids are also its file names and
		// are validated against a fixed pattern, so the generator has to be the
		// modeling package's own.
		in.NewID = modeling.NewProjectID
	}
	if in.Now == nil {
		in.Now = time.Now
	}

	// 2: the workspace id is computed even when modeling is disabled. It is cheap,
	// it cannot fail differently later, and a disabled build that still reports a
	// consistent id keeps the command layer's scope construction uniform.
	workspaceID, err := WorkspaceID(in.Config.Paths.Workspace)
	if err != nil {
		return components, err
	}
	components.WorkspaceID = workspaceID

	cfg := in.Config.Modeling
	if !cfg.Enabled {
		return components, nil
	}

	// 3: the dependencies configuration cannot supply. Both are hard errors rather
	// than silent downgrades: an operator who set QEMU_AGENT_MODELING_ENABLED=true
	// asked for a working pipeline, and a pipeline with no model would fail on the
	// first advance with a much less obvious message.
	if in.Executor == nil {
		return components, errors.New("modeling requires a tool executor")
	}
	if in.Completer == nil {
		return components, errors.New("modeling requires a model")
	}

	// 4: the two stores. Their directories are created here, once, so that no
	// request-path code performs a MkdirAll and no stage has to care whether this
	// is the first run.
	projects, err := modeling.OpenFileProjectStore(modeling.StoreOptions{
		Root:        projectsRoot(cfg.Dir),
		MaxProjects: cfg.MaxProjects,
		NewID:       in.NewID,
		Now:         in.Now,
		Logger:      in.Logger,
	})
	if err != nil {
		return components, fmt.Errorf("open modeling project store: %w", err)
	}
	artifacts, err := modeling.OpenFileArtifactStore(modeling.ArtifactOptions{
		Root:             artifactsRoot(cfg.Dir),
		MaxArtifactBytes: cfg.MaxArtifactBytes,
		MaxProjectBytes:  cfg.MaxProjectBytes,
		NewBatchID:       modeling.NewBatchID,
		Now:              in.Now,
	})
	if err != nil {
		return components, fmt.Errorf("open modeling artifact store: %w", err)
	}
	components.Projects = projects
	components.Artifacts = artifacts

	// 5: the stage registry, built once and immutable afterwards. What a project
	// can do is therefore a property of the binary rather than of state somebody
	// edited. Emit is constructed with AutoApply from configuration — false by
	// default — and Verify with the operator's already-configured build directory,
	// because this pipeline never runs `configure` itself.
	runner, err := modeling.NewPipeline(modeling.PipelineOptions{
		Projects:  projects,
		Artifacts: artifacts,
		Stages: []modeling.StageRunner{
			modeling.NewPlanStage(),
			modeling.NewExtractStage(),
			modeling.NewInferStage(),
			modeling.NewEmitStage(modeling.NewCRenderer(), cfg.AutoApply),
			modeling.NewVerifyStage(cfg.BuildDir),
		},
		Tools:     newModelingTools(in.Executor, in.NewID),
		Completer: in.Completer,
		Workspace: workspaceID,
		QemuRoot:  cfg.QemuRoot,
		Timeout:   cfg.StageTimeout,
		Now:       in.Now,
		Logger:    in.Logger,
	})
	if err != nil {
		return components, fmt.Errorf("build modeling pipeline: %w", err)
	}
	components.Runner = runner

	// 6: the applier, and only if there is somewhere to apply to. An empty QemuRoot
	// leaves DisabledApplier in place, so `/modeling apply` reports that this build
	// has no apply target instead of failing halfway through a write.
	if strings.TrimSpace(cfg.QemuRoot) == "" {
		return components, nil
	}
	applier, err := modeling.NewApplier(modeling.ApplierOptions{
		Projects:  projects,
		Artifacts: artifacts,
		Tools:     newModelingTools(in.Executor, in.NewID),
		QemuRoot:  cfg.QemuRoot,
		Now:       in.Now,
		Logger:    in.Logger,
	})
	if err != nil {
		return components, fmt.Errorf("build modeling applier: %w", err)
	}
	components.Applier = applier
	return components, nil
}
