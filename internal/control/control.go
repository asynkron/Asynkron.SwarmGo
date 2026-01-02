package control

// Command represents a UI-initiated action sent to the orchestrator.
type Command interface{ isCommand() }

// RestartAgent requests that an agent be restarted with an optional injected message.
type RestartAgent struct {
	AgentID string
	Message string
}

func (RestartAgent) isCommand() {}
