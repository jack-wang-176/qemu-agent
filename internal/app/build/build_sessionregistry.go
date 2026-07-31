package build

import (
	"fmt"

	"github.com/jack-wang-176/qemu-agent/internal/config"
	"github.com/jack-wang-176/qemu-agent/internal/session"
)

func BuildSessionRegistry(agentConfig config.AgentConfig, store session.Store, systemPrompt string) (*session.Registry, error) {
	factory, err := session.NewDefaultFactory(session.Defaults{
		Model:        agentConfig.Model,
		SystemPrompt: systemPrompt,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("build session factory: %w", err)
	}
	registry, err := session.NewRegistry(store, factory)
	if err != nil {
		return nil, fmt.Errorf("build session registry: %w", err)
	}
	return registry, nil
}
