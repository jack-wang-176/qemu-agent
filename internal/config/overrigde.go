package config

import (
	"strings"
)

/* override agent config*/
type Overrides struct {
	Provider *string
	Model    *string
	MaxTurns *int
	Stream   *bool
}

func (c Config) WithOverrides(overrides Overrides) (Config, error) {
	result := c
	if overrides.Provider != nil {
		result.Agent.Provider = strings.TrimSpace(*overrides.Provider)
	}
	if overrides.Model != nil {
		result.Agent.Model = strings.TrimSpace(*overrides.Model)
	}
	if overrides.MaxTurns != nil {
		result.Agent.MaxTurns = *overrides.MaxTurns
	}
	if overrides.Stream != nil {
		result.Agent.Stream = *overrides.Stream
	}
	if err := result.Validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}
