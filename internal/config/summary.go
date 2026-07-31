package config

// summary return exposable unit.
type Summary struct {
	Provider               string
	Model                  string
	Workspace              string
	DataDir                string
	SessionDir             string
	Stream                 bool
	LogLevel               string
	LogFormat              string
	MaxInputBytes          int
	CLISessionKey          string
	ModelCount             int
	CLIEnabled             bool
	TelegramEnabled        bool
	TelegramAllowedUsers   int
	TelegramMaxConcurrency int
}

func (c Config) Summary() Summary {
	return Summary{
		Provider:               c.Agent.Provider,
		Model:                  c.Agent.Model,
		Workspace:              c.Paths.Workspace,
		DataDir:                c.Paths.DataDir,
		SessionDir:             c.Paths.SessionDir,
		Stream:                 c.Agent.Stream,
		LogLevel:               c.Log.Level,
		LogFormat:              c.Log.Format,
		MaxInputBytes:          c.Channel.MaxInputBytes,
		CLISessionKey:          c.Channel.CLISessionKey,
		ModelCount:             len(c.Models.Definitions),
		CLIEnabled:             c.Channel.CLIEnabled,
		TelegramEnabled:        c.Channel.Telegram.Enabled,
		TelegramAllowedUsers:   len(c.Channel.Telegram.AllowedUserIDs),
		TelegramMaxConcurrency: c.Channel.Telegram.MaxConcurrency,
	}
}
