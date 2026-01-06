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

type AgentProgress struct {
	ID        string
	Completed int
	Total     int
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

// AgentStatus carries git/log snapshot updates for an agent.
type AgentStatus struct {
	ID       string
	Snapshot StatusSnapshot
}

// StatusSnapshot is a lightweight git/log view used by the UI.
type StatusSnapshot struct {
	Branch        string             `json:"branch"`
	Staged        []StatusFileChange `json:"staged"`
	Unstaged      []StatusFileChange `json:"unstaged"`
	Untracked     []string           `json:"untracked"`
	RecentCommits []string           `json:"recentCommits"`
	Error         string             `json:"error,omitempty"`
	LastPass      *StatusLogEvent    `json:"lastPass,omitempty"`
	LastFail      *StatusLogEvent    `json:"lastFail,omitempty"`
	UpdatedAt     time.Time          `json:"updatedAt"`
}

// StatusFileChange mirrors git numstat output.
type StatusFileChange struct {
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	File    string `json:"file"`
}

// StatusLogEvent captures simple pass/fail signals from logs.
type StatusLogEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
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
func (AgentProgress) isEvent()   {}
func (AgentLine) isEvent()       {}
func (StatusMessage) isEvent()   {}
func (PhaseChanged) isEvent()    {}
func (RoundChanged) isEvent()    {}
func (RemainingTime) isEvent()   {}
func (TodoLoaded) isEvent()      {}
func (CompletedWorker) isEvent() {}
func (AgentStatus) isEvent()     {}
