package agents

import (
	"fmt"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/internal/events"
	"github.com/asynkron/Asynkron.SwarmGo/internal/prompts"
)

// NewWorker builds a configured Agent representing a worker.
func NewWorker(index int, worktree string, todoFile string, cli CLI, logPath string, autopilot bool, branchName string, restartCount int, ghAvailable bool, isGitHubRepo bool, events chan<- events.Event) *Agent {
	// Pick display model based on worker index for a bit of variety.
	apiModel, displayModel := cli.Model(index)
	prompt := prompts.WorkerPrompt(todoFile, fmt.Sprintf("Worker %d", index+1), autopilot, branchName, logPath, restartCount, ghAvailable, isGitHubRepo)

	return &Agent{
		ID:      fmt.Sprintf("worker-%d", index+1),
		Name:    fmt.Sprintf("Worker %d", index+1),
		Prompt:  prompt,
		Workdir: worktree,
		LogPath: logPath,
		Model:   apiModel,
		CLI:     cli,
		Display: displayModel,
		events:  events,
	}
}

// NewSupervisor builds the supervisor agent.
func NewSupervisor(worktrees []string, workerLogs []string, repoPath string, codedPath string, cli CLI, logPath string, autopilot bool, restartCount int, ghAvailable bool, isGitHubRepo bool, events chan<- events.Event) *Agent {
	prompt := prompts.SupervisorPrompt(worktrees, workerLogs, repoPath, codedPath, autopilot, restartCount, ghAvailable, isGitHubRepo)
	apiModel, displayModel := cli.Model(int(time.Now().UnixNano()))
	if sm, ok := cli.(SupervisorModeler); ok {
		apiModel, displayModel = sm.SupervisorModel()
	}
	return &Agent{
		ID:              "supervisor",
		Name:            "Supervisor",
		Prompt:          prompt,
		Workdir:         repoPath,
		LogPath:         logPath,
		Model:           apiModel,
		Display:         displayModel,
		CLI:             cli,
		events:          events,
		isSupervisor:    true,
		workerWorktrees: worktrees,
		workerLogPaths:  workerLogs,
	}
}

// NewUserCommand builds the one-off user-controlled agent.
func NewUserCommand(worktrees []string, repoPath string, cli CLI, logPath string, message string, events chan<- events.Event) *Agent {
	prompt := prompts.UserCommandPrompt(worktrees, repoPath, message)
	apiModel, displayModel := cli.Model(len(worktrees) + 99)
	if sm, ok := cli.(SupervisorModeler); ok {
		_, displayModel = sm.SupervisorModel()
	}
	return &Agent{
		ID:      "user-command",
		Name:    "User Command",
		Prompt:  prompt,
		Workdir: repoPath,
		LogPath: logPath,
		Model:   apiModel,
		Display: displayModel,
		CLI:     cli,
		events:  events,
	}
}

// NewPrep builds the prep agent that generates test.sh before workers start.
func NewPrep(worktree string, todoPath string, cli CLI, logPath string, events chan<- events.Event) *Agent {
	apiModel, displayModel := cli.Model(0)
	prompt := prompts.PrepTestsPrompt(todoPath)
	return &Agent{
		ID:      "prep",
		Name:    "Prep",
		Prompt:  prompt,
		Workdir: worktree,
		LogPath: logPath,
		Model:   apiModel,
		Display: displayModel,
		CLI:     cli,
		events:  events,
	}
}
