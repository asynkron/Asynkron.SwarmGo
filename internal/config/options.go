package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Options contains runtime configuration parsed from CLI flags.
type Options struct {
	ClaudeWorkers  int
	CodexWorkers   int
	CopilotWorkers int
	GeminiWorkers  int

	Repo       string
	Todo       string
	Minutes    int
	MaxRounds  int
	Arena      bool
	Autopilot  bool
	Supervisor AgentType
	PrepAgent  AgentType
	AgentMode  bool
	AgentType  AgentType

	Resume     string
	Detect     bool
	SkipDetect bool
}

// AgentType matches supported CLI agent executables.
type AgentType string

const (
	AgentClaude  AgentType = "claude"
	AgentCodex   AgentType = "codex"
	AgentCopilot AgentType = "copilot"
	AgentGemini  AgentType = "gemini"
)

// Validate normalizes and validates the options. It also resolves the repo path.
func (o *Options) Validate() error {
	if o.Detect {
		// Skip all other validation; detection runs without needing repo/todo.
		return nil
	}

	if o.ClaudeWorkers < 0 || o.CodexWorkers < 0 || o.CopilotWorkers < 0 || o.GeminiWorkers < 0 {
		return errors.New("worker counts cannot be negative")
	}

	if !o.Arena && o.Minutes < 1 {
		return errors.New("minutes must be at least 1")
	}

	if o.MaxRounds < 1 {
		return errors.New("max rounds must be at least 1")
	}

	if o.AgentMode {
		o.ClaudeWorkers, o.CodexWorkers, o.CopilotWorkers, o.GeminiWorkers = 0, 0, 0, 0
		if o.AgentType == "" {
			o.AgentType = AgentCodex
		}
		// Single-agent mode runs directly in the repo; disable autopilot/branch creation.
		o.Autopilot = false
	} else if o.ClaudeWorkers+o.CodexWorkers+o.CopilotWorkers+o.GeminiWorkers == 0 {
		// Default to two Claude workers when nothing is specified.
		o.ClaudeWorkers = 2
	}

	if o.PrepAgent == "" {
		o.PrepAgent = AgentClaude
	}

	if o.Repo == "" {
		root, err := findGitRoot()
		if err != nil {
			return err
		}
		o.Repo = root
	} else {
		abs, err := filepath.Abs(o.Repo)
		if err != nil {
			return fmt.Errorf("invalid repo path: %w", err)
		}
		o.Repo = abs
	}

	info, err := os.Stat(o.Repo)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("repository path does not exist: %s", o.Repo)
	}

	gitDir := filepath.Join(o.Repo, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("not a git repository: %s", o.Repo)
	}

	if o.Todo == "" {
		o.Todo = "todo.md"
	}
	todoPath := filepath.Join(o.Repo, o.Todo)
	if _, err := os.Stat(todoPath); err != nil {
		return fmt.Errorf("todo file not found: %s", todoPath)
	}

	return nil
}

// TotalWorkers returns the sum of all configured worker counts.
func (o Options) TotalWorkers() int {
	if o.AgentMode {
		return 1
	}
	return o.ClaudeWorkers + o.CodexWorkers + o.CopilotWorkers + o.GeminiWorkers
}

// Duration returns the configured time limit for a round.
func (o Options) Duration() time.Duration {
	return time.Duration(o.Minutes) * time.Minute
}

func findGitRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	current := dir
	for {
		gitDir := filepath.Join(current, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("not in a git repository; use --repo to specify a path")
		}
		current = parent
	}
}
