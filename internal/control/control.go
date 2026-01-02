package control

// Command represents a UI-initiated action sent to the orchestrator.
type Command interface{ isCommand() }

// RestartAgent requests that an agent be restarted with an optional injected message.
type RestartAgent struct {
	AgentID string
	Message string
}

func (RestartAgent) isCommand() {}

// StopAgent requests that an agent be stopped.
type StopAgent struct{ AgentID string }

func (StopAgent) isCommand() {}

// StartAgent requests that an agent be started (or restarted) without an injected message.
type StartAgent struct{ AgentID string }

func (StartAgent) isCommand() {}

// StartUserCommand starts the one-off user agent with a specific prompt.
type StartUserCommand struct {
	Message string
}

func (StartUserCommand) isCommand() {}
