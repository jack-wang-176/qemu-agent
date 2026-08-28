package config

// modeling.go closes the I8 device-modeling pipeline configuration: the value
// group, its environment loader and its validation.
//
// Modeling differs from Skills and Memory in one dangerous way. Those two only
// ever touch directories the agent owns under DataDir. Modeling additionally
// reads and writes a QEMU source tree (QemuRoot), which is *not* the private
// sandbox of Paths.Workspace. That is why QemuRoot is optional, why an empty
// QemuRoot simply makes the Emit/Verify stages unavailable instead of failing
// startup, and why AutoApply defaults to false: emitting device code never
// lands on disk without an explicit, approved apply.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ModelingConfig is the only source of paths and limits for the modeling
// pipeline. It contains values only; the stores and the Pipeline are created by
// internal/app/build.
type ModelingConfig struct {
	// Enabled gates the whole capability. Default false: I8 is a new feature
	// and must not change the behaviour of an existing deployment.
	Enabled bool
	// Dir is the absolute root for project state and artifacts.
	Dir string
	// QemuRoot is the QEMU source tree Emit/Verify work against.
	// Empty is legal and means those two stages are unavailable.
	QemuRoot string
	// BuildDir is the *already configured* QEMU build directory Verify runs
	// ninja and qtest in. It is a separate value rather than a path derived from
	// QemuRoot because configuring QEMU needs flags only the operator knows
	// (target list, cross compilers), and a pipeline that ran `configure`
	// itself would build something nobody asked for. Empty means Verify is
	// unavailable, exactly like an empty QemuRoot disables Emit's apply.
	BuildDir string
	// MaxProjects caps how many projects one workspace may accumulate.
	MaxProjects int
	// MaxArtifactBytes caps a single artifact; MaxProjectBytes caps the sum of
	// all artifacts of one project.
	MaxArtifactBytes int64
	MaxProjectBytes  int64
	// StageTimeout is the wall-clock ceiling for one stage run.
	StageTimeout time.Duration
	// Model is the model used by the modeling stages. Empty reuses Agent.Model.
	Model string
	// AutoApply lets Emit write into QemuRoot without a separate command.
	// Default false, and it requires QemuRoot to be set.
	AutoApply bool
}

// loadModelingConfig reads the Modeling group. Like the Skill and Memory
// loaders it never touches the filesystem: existence checks belong to Build.
func loadModelingConfig(lookup LookupEnv, dataDir string) (ModelingConfig, error) {
	enabled, err := envBool(lookup, "QEMU_AGENT_MODELING_ENABLED", DefaultModelingEnabled)
	if err != nil {
		return ModelingConfig{}, err
	}
	dir, err := resolveDir(lookup, "QEMU_AGENT_MODELING_DIR", filepath.Join(dataDir, "modeling"))
	if err != nil {
		return ModelingConfig{}, err
	}
	// QemuRoot has no fallback: an unset variable must stay empty so that
	// Validate can tell "no apply target" from "a wrong apply target".
	qemuRoot := envString(lookup, "QEMU_AGENT_MODELING_QEMU_ROOT", "")
	if qemuRoot != "" {
		absolute, err := filepath.Abs(qemuRoot)
		if err != nil {
			return ModelingConfig{}, fmt.Errorf("resolve QEMU_AGENT_MODELING_QEMU_ROOT: %w", err)
		}
		qemuRoot = filepath.Clean(absolute)
	}
	// BuildDir follows the same "no fallback" rule: guessing <QemuRoot>/build
	// would make Verify run ninja in a directory the operator never nominated.
	buildDir := envString(lookup, "QEMU_AGENT_MODELING_BUILD_DIR", "")
	if buildDir != "" {
		absolute, err := filepath.Abs(buildDir)
		if err != nil {
			return ModelingConfig{}, fmt.Errorf("resolve QEMU_AGENT_MODELING_BUILD_DIR: %w", err)
		}
		buildDir = filepath.Clean(absolute)
	}
	maxProjects, err := envInt(lookup, "QEMU_AGENT_MODELING_MAX_PROJECTS", DefaultModelingMaxProjects)
	if err != nil {
		return ModelingConfig{}, err
	}
	maxArtifactBytes, err := envInt(lookup, "QEMU_AGENT_MODELING_MAX_ARTIFACT_BYTES", DefaultModelingArtifactBytes)
	if err != nil {
		return ModelingConfig{}, err
	}
	maxProjectBytes, err := envInt(lookup, "QEMU_AGENT_MODELING_MAX_PROJECT_BYTES", DefaultModelingProjectBytes)
	if err != nil {
		return ModelingConfig{}, err
	}
	stageTimeout, err := envDuration(lookup, "QEMU_AGENT_MODELING_STAGE_TIMEOUT", DefaultModelingStageTimeout)
	if err != nil {
		return ModelingConfig{}, err
	}
	autoApply, err := envBool(lookup, "QEMU_AGENT_MODELING_AUTO_APPLY", DefaultModelingAutoApply)
	if err != nil {
		return ModelingConfig{}, err
	}
	return ModelingConfig{
		Enabled:          enabled,
		Dir:              dir,
		QemuRoot:         qemuRoot,
		BuildDir:         buildDir,
		MaxProjects:      maxProjects,
		MaxArtifactBytes: int64(maxArtifactBytes),
		MaxProjectBytes:  int64(maxProjectBytes),
		StageTimeout:     stageTimeout,
		Model:            envString(lookup, "QEMU_AGENT_MODELING_MODEL", ""),
		AutoApply:        autoApply,
	}, nil
}

// validateModeling checks the Modeling group. It is conditional for the same
// reason validateKnowledge is: a disabled capability only has to be internally
// consistent, so an operator can start the agent without creating any modeling
// directory.
func (c Config) validateModeling() error {
	if !c.Modeling.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Modeling.Dir) == "" {
		return errors.New("QEMU_AGENT_MODELING_DIR is empty")
	}
	if !filepath.IsAbs(c.Modeling.Dir) {
		return fmt.Errorf("modeling dir %q must be absolute", c.Modeling.Dir)
	}
	// The modeling root must not overlap any other capability's root: a purge
	// of one capability's data must never delete another's.
	for _, other := range []struct {
		name string
		dir  string
	}{
		{"session dir", c.Paths.SessionDir},
		{"skills dir", skillDirIfEnabled(c)},
		{"memory dir", memoryDirIfEnabled(c)},
	} {
		if err := requireDisjointDir("modeling dir", c.Modeling.Dir, other.name, other.dir); err != nil {
			return err
		}
	}
	if c.Modeling.QemuRoot != "" && !filepath.IsAbs(c.Modeling.QemuRoot) {
		return fmt.Errorf("modeling qemu root %q must be absolute", c.Modeling.QemuRoot)
	}
	// A build directory is only meaningful next to a source tree: verifying a
	// build of a tree the pipeline may not read is not a configuration anyone
	// wants, so it is rejected at startup rather than at the fifth stage.
	if c.Modeling.BuildDir != "" {
		if !filepath.IsAbs(c.Modeling.BuildDir) {
			return fmt.Errorf("modeling build dir %q must be absolute", c.Modeling.BuildDir)
		}
		if c.Modeling.QemuRoot == "" {
			return errors.New("QEMU_AGENT_MODELING_BUILD_DIR requires QEMU_AGENT_MODELING_QEMU_ROOT")
		}
	}
	if c.Modeling.AutoApply && c.Modeling.QemuRoot == "" {
		return errors.New("QEMU_AGENT_MODELING_AUTO_APPLY requires QEMU_AGENT_MODELING_QEMU_ROOT")
	}
	if c.Modeling.MaxProjects <= 0 {
		return errors.New("QEMU_AGENT_MODELING_MAX_PROJECTS must be > 0")
	}
	if c.Modeling.MaxArtifactBytes <= 0 || c.Modeling.MaxProjectBytes <= 0 {
		return errors.New("modeling byte limits must be > 0")
	}
	if c.Modeling.MaxArtifactBytes > c.Modeling.MaxProjectBytes {
		return errors.New("modeling artifact limit must be <= modeling project limit")
	}
	if c.Modeling.StageTimeout <= 0 {
		return errors.New("QEMU_AGENT_MODELING_STAGE_TIMEOUT must be > 0")
	}
	return nil
}

// ValidateProfessionalModelingV1 validates the stricter product contract used
// by the professional modeling workflow. The legacy command path may remain
// disabled or use AutoApply, so this check is intentionally separate from
// Config.Validate until the composition root is switched in Step 12.
func (c Config) ValidateProfessionalModelingV1() error {
	if err := c.validateModeling(); err != nil {
		return err
	}
	if !c.Modeling.Enabled {
		return errors.New("professional modeling v1 requires QEMU_AGENT_MODELING_ENABLED=true")
	}
	if c.Modeling.AutoApply {
		return errors.New("professional modeling v1 requires QEMU_AGENT_MODELING_AUTO_APPLY=false")
	}
	return nil
}

// skillDirIfEnabled and memoryDirIfEnabled return an empty string for a
// disabled capability, which requireDisjointDir treats as "nothing to compare".
// A disabled capability has no directory on disk, so an overlap with it cannot
// destroy data.
func skillDirIfEnabled(c Config) string {
	if !c.Skills.Enabled {
		return ""
	}
	return c.Skills.Dir
}

func memoryDirIfEnabled(c Config) string {
	if !c.Memory.Enabled {
		return ""
	}
	return c.Memory.Dir
}
