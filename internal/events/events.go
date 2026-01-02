package events

import "time"

// Event is any message sent to the UI.
type Event interface{ isEvent() }

type AgentAdded struct {
	ID       string
	Name     string
	Kind     string
	Model    string
	LogPath  string
	Worktree string
	Running  bool
}

type AgentRemoved struct{ ID string }

type AgentStopped struct {
	ID       string
	ExitCode int
}

type AgentLine struct {
	ID   string
	Kind AgentMessageKind
	Line string
}

type StatusMessage struct{ Message string }

type PhaseChanged struct{ Phase string }

type RoundChanged struct {
	Current int
	Total   int
}

type RemainingTime struct{ Duration time.Duration }

type TodoLoaded struct {
	Content string
	Path    string
}

type CompletedWorker struct {
	Worker  int
	LogPath string
}

// AgentMessageKind mirrors Say/Do/See categories from the original UI.
type AgentMessageKind int

const (
	MessageSay AgentMessageKind = iota
	MessageDo
	MessageSee
)

func (AgentAdded) isEvent()      {}
func (AgentRemoved) isEvent()    {}
func (AgentStopped) isEvent()    {}
func (AgentLine) isEvent()       {}
func (StatusMessage) isEvent()   {}
func (PhaseChanged) isEvent()    {}
func (RoundChanged) isEvent()    {}
func (RemainingTime) isEvent()   {}
func (TodoLoaded) isEvent()      {}
func (CompletedWorker) isEvent() {}
