package config

import "strings"

// Overrides contains values explicitly supplied by a higher-precedence source.
// A nil pointer means the source did not specify that value.
type Overrides struct {
	Provider *string
	Model    *string
	MaxTurns *int
	Stream   *bool
}

func (c Config) WithOverrides(overrides Overrides) (Config, error) {
	result := c
	if overrides.Provider != nil {
		result.Agent.Provider = strings.ToLower(strings.TrimSpace(*overrides.Provider))
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
	result.Log.Level = strings.ToLower(strings.TrimSpace(result.Log.Level))
	result.Log.Format = strings.ToLower(strings.TrimSpace(result.Log.Format))
	result.Agent.Provider = strings.ToLower(strings.TrimSpace(result.Agent.Provider))
	if err := result.Validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}
