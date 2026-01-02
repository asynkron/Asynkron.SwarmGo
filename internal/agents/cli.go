package agents

import "github.com/asynkron/Asynkron.SwarmGo/internal/config"

// CLI abstracts how to invoke each agent executable.
type CLI interface {
	Name() string
	Command() string
	UseStdin() bool
	BuildArgs(prompt string, model string) []string
	Model(index int) (apiModel string, display string)
}

// NewCLI returns an implementation for the given agent type.
func NewCLI(agent config.AgentType) CLI {
	switch agent {
	case config.AgentClaude:
		return claudeCLI{}
	case config.AgentCodex:
		return codexCLI{}
	case config.AgentCopilot:
		return copilotCLI{}
	case config.AgentGemini:
		return geminiCLI{}
	default:
		return codexCLI{}
	}
}

type codexCLI struct{}

func (codexCLI) Name() string    { return "Codex" }
func (codexCLI) Command() string { return "codex" }
func (codexCLI) UseStdin() bool  { return false }
func (codexCLI) Model(i int) (string, string) {
	models := []string{"gpt-5.2-codex", "gpt-5.1-codex-max", "gpt-5.2"}
	short := []string{"5.2-cdx", "5.1-max", "5.2"}
	idx := i % len(models)
	return models[idx], short[idx]
}
func (c codexCLI) BuildArgs(prompt string, model string) []string {
	args := []string{"exec", prompt, "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

type claudeCLI struct{}

func (claudeCLI) Name() string               { return "Claude" }
func (claudeCLI) Command() string            { return "claude" }
func (claudeCLI) UseStdin() bool             { return true }
func (claudeCLI) Model(int) (string, string) { return "opus", "opus" }
func (claudeCLI) BuildArgs(prompt string, model string) []string {
	args := []string{"-p", "--dangerously-skip-permissions", "--tools", "default", "--output-format", "stream-json", "--verbose"}
	if model != "" {
		args = append(args, "--model", model)
	}
	// Bubble Tea provides its own prompt injection; Claude reads from stdin.
	return args
}

type copilotCLI struct{}

func (copilotCLI) Name() string               { return "Copilot" }
func (copilotCLI) Command() string            { return "copilot" }
func (copilotCLI) UseStdin() bool             { return false }
func (copilotCLI) Model(int) (string, string) { return "gpt-5", "gpt-5" }
func (copilotCLI) BuildArgs(prompt string, model string) []string {
	if model == "" {
		model = "gpt-5"
	}
	return []string{"-p", prompt, "--allow-all-tools", "--allow-all-paths", "--stream", "on", "--model", model}
}

type geminiCLI struct{}

func (geminiCLI) Name() string               { return "Gemini" }
func (geminiCLI) Command() string            { return "gemini" }
func (geminiCLI) UseStdin() bool             { return false }
func (geminiCLI) Model(int) (string, string) { return "gemini-2.0-flash-exp", "flash" }
func (geminiCLI) BuildArgs(prompt string, model string) []string {
	if model == "" {
		model = "gemini-2.0-flash-exp"
	}
	return []string{prompt, "--yolo", "--output-format", "stream-json", "--model", model}
}
