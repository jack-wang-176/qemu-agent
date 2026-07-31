package config

type Summary struct {
	Provider   string
	Model      string
	Workspace  string
	DataDir    string
	SessionDir string
	Stream     bool
	LogLevel   string
	LogFormat  string
}

func (c Config) Summary() Summary {
	return Summary{
		Provider:   c.Agent.Provider,
		Model:      c.Agent.Model,
		Workspace:  c.Paths.Workspace,
		DataDir:    c.Paths.DataDir,
		SessionDir: c.Paths.SessionDir,
		Stream:     c.Agent.Stream,
		LogLevel:   c.Log.Level,
		LogFormat:  c.Log.Format,
	}
}
