package build

import (
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/config"
)

func TestBuildModelingServiceUsesLegacyRuntimeByDefault(t *testing.T) {
	cfg := modelingConfig(t, func(cfg *config.Config) { cfg.Modeling.Enabled = true })
	legacy, err := BuildModeling(ModelingInput{
		Config: cfg, Logger: testLogger(t), Executor: &stubExecutor{}, Completer: stubCompleter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	components, err := BuildModelingService(ModelingServiceInput{Legacy: legacy})
	if err != nil {
		t.Fatal(err)
	}
	if components.Service == nil || components.Engine == nil || components.Query == nil {
		t.Fatalf("incomplete modeling service components: %#v", components)
	}
}

func TestBuildModelingServiceRejectsIncompleteInput(t *testing.T) {
	if _, err := BuildModelingService(ModelingServiceInput{}); err == nil {
		t.Fatal("BuildModelingService accepted incomplete input")
	}
}
