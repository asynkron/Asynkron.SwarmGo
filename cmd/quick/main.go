package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/agentrunner"
	"github.com/charmbracelet/glamour"
)

func main() {
	var (
		repo      string
		agentType string
		model     string
	)
	flag.StringVar(&repo, "repo", "", "working directory (defaults to git root or current dir)")
	flag.StringVar(&agentType, "agent", "claude", "agent to use (codex|claude|copilot|gemini)")
	flag.StringVar(&model, "model", "", "model override (optional)")
	yolo := flag.Bool("yolo", false, "allow non-read-only actions (drops guardrails in prompt)")
	flag.Parse()

	userInput := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if userInput == "" {
		fmt.Println("usage: quick [--repo PATH] [--agent codex|claude|copilot|gemini] [--model NAME] <request>")
		os.Exit(1)
	}

	repoPath := repo
	if repoPath == "" {
		if root, err := gitRoot(); err == nil {
			repoPath = root
		} else {
			cwd, _ := os.Getwd()
			repoPath = cwd
		}
	}

	cli, err := chooseCLI(agentType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid agent: %v\n", err)
		os.Exit(1)
	}

	if model == "" && strings.EqualFold(agentType, "claude") {
		model = "haiku"
	}

	prompt := buildPrompt(repoPath, userInput, *yolo)

	logPath, cleanup := tempLogPath()
	defer cleanup()
	events := make(chan agentrunner.Event, 256)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	agent := &agentrunner.Agent{
		ID:              "quick-agent",
		Name:            "Quick",
		Prompt:          prompt,
		Workdir:         repoPath,
		LogPath:         logPath,
		Model:           model,
		CLI:             cli,
		Events:          events,
		DisablePreamble: true,
	}

	if err := agent.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "start agent: %v\n", err)
		os.Exit(1)
	}

	renderer := newRenderer()
	fmt.Printf("Running quick task with %s in %s\n\n", cli.Name(), repoPath)

	stopping := false
	var stopTimer <-chan time.Time
	var lastSay string
	var spinIdx int
	var spinActive bool
	statusWidth := 0
	spinTicker := time.NewTicker(40 * time.Millisecond)
	defer spinTicker.Stop()
done:
	for {
		select {
		case <-ctx.Done():
			break done
		case ev := <-events:
			switch e := ev.(type) {
			case agentrunner.AgentAdded:
				fmt.Printf("Agent started (%s %s)\n", e.Kind, e.Model)
			case agentrunner.AgentLine:
				if isPreambleLine(e.Line) {
					continue
				}
				switch e.Kind {
				case agentrunner.MessageDo:
					if blocked := checkDangerous(e.Line); blocked != "" {
						fmt.Printf("⚠️  blocked destructive command: %s (line: %s)\n", blocked, strings.TrimSpace(e.Line))
						agent.Stop()
						break done
					}
					spinIdx++
					spin := spinnerFrame(spinIdx)
					status := spin
					visible := len(stripANSI(status))
					if visible > statusWidth {
						statusWidth = visible
					}
					fmt.Printf("\r%-*s\r", statusWidth, status)
					spinActive = true
				case agentrunner.MessageSee:
					// hide see output
				default:
					if spinActive {
						fmt.Printf("\r%-*s\r", statusWidth, "")
						spinActive = false
					}
					text := strings.TrimSpace(e.Line)
					if text == "" {
						continue
					}
					text = collapseBlankLines(text)
					if text == lastSay {
						continue
					}
					out, err := renderer.Render(text)
					if err != nil {
						fmt.Println(text)
						continue
					}
					lastSay = text
					fmt.Print(strings.TrimRight(out, "\n") + "\n")
				}
			case agentrunner.AgentStopped:
				if spinActive {
					fmt.Printf("\r%-*s\r", statusWidth, "")
					spinActive = false
				}
				fmt.Printf("\nAgent exited (code %d)\n", e.ExitCode)
				stopping = true
				stopTimer = time.After(300 * time.Millisecond)
			}
		case <-spinTicker.C:
			if spinActive {
				spinIdx++
				spin := spinnerFrame(spinIdx)
				status := spin
				visible := len(stripANSI(status))
				if visible > statusWidth {
					statusWidth = visible
				}
				fmt.Printf("\r%-*s\r", statusWidth, status)
			}
		case <-stopTimer:
			if stopping {
				break done
			}
		}
	}
}

func newRenderer() *glamour.TermRenderer {
	r, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(0), // disable reflow to preserve source layout
	)
	return r
}

func chooseCLI(name string) (agentrunner.CLI, error) {
	switch strings.ToLower(name) {
	case "codex":
		return agentrunner.CodexCLI(), nil
	case "claude":
		return agentrunner.ClaudeCLI(), nil
	case "copilot":
		return agentrunner.CopilotCLI(), nil
	case "gemini":
		return agentrunner.GeminiCLI(), nil
	default:
		return nil, fmt.Errorf("unknown agent %q", name)
	}
}

func buildPrompt(repo string, userInput string, yolo bool) string {
	if yolo {
		return fmt.Sprintf(`You are Quick. Interpret the user's request and perform the minimum commands needed to answer it.
Work ONLY inside: %s

Keep responses concise and focused on the request.
Format responses in Markdown.
When showing command output (e.g., ls/git), wrap it in triple-backtick text fences.

User request:
%s
`, repo, userInput)
	}

	return fmt.Sprintf(`You are Quick, a read-only assistant.
You must interpret the user's request and perform the minimum commands needed to answer it.
Work ONLY inside: %s

Hard guardrails:
- Do NOT modify files, delete, move, or rename anything.
- Do NOT run installers, package managers, git push/fetch/reset/clean, sudo, or network calls.
- Prefer read-only commands like ls, cat, find, git status/log.
- If a destructive action is requested, refuse and explain briefly.

Keep responses concise and focused on the request.
Format responses in Markdown.
When showing command output (e.g., ls/git), wrap it in triple-backtick text fences.

User request:
%s
`, repo, userInput)
}

func gitRoot() (string, error) {
	out, err := runCmd("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func runCmd(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	out, err := c.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func tempLogPath() (string, func()) {
	dir, _ := os.MkdirTemp("", "swarm-quick-*")
	path := dir + "/agent.log"
	cleanup := func() {
		_ = os.RemoveAll(dir)
	}
	return path, cleanup
}

var dangerPatterns = []string{
	"rm -rf", "rm -r", "rm -f", "sudo ", "apt-get", "yum ", "dnf ", "pacman", "brew install",
	"pip install", "npm install", "pnpm install", "yarn add", "curl ", "wget ", " | sh", " |bash", " | bash",
	"chmod ", "chown ", "systemctl", "service ", "mkfs", "mount ", "umount", "dd if=", "truncate ",
	"git push", "git reset --hard", "git clean -fd", "git checkout -f", "docker rm", "docker rmi", "docker system prune",
}

func checkDangerous(line string) string {
	l := strings.ToLower(line)
	for _, pat := range dangerPatterns {
		if strings.Contains(l, pat) {
			return pat
		}
	}
	return ""
}

func summarizeDo(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "git status"):
		return "🔍 git status"
	case strings.Contains(l, "git log"):
		return "🧭 git log"
	case strings.Contains(l, "git diff"):
		return "📄 git diff"
	case strings.Contains(l, "ls"):
		return "📂 listing files"
	case strings.Contains(l, "cat "):
		return "📖 reading file"
	case strings.Contains(l, "find "):
		return "🔎 searching files"
	case strings.Contains(l, "grep ") || strings.Contains(l, "rg "):
		return "🔎 searching content"
	case strings.Contains(l, "dotnet test") || strings.Contains(l, "go test") || strings.Contains(l, "npm test"):
		return "🧪 running tests"
	default:
		return "⚙️  " + strings.TrimSpace(line)
	}
}

func summarizeSee(line string) string {
	txt := strings.TrimSpace(line)
	if strings.HasPrefix(strings.ToLower(txt), "/bin/zsh -lc") {
		return ""
	}
	if len(txt) > 200 {
		txt = txt[:200] + "…"
	}
	return "↳ " + txt
}

func isNoiseDo(line string) bool {
	t := strings.TrimSpace(line)
	switch t {
	case "[exec]", "[thinking]":
		return true
	}
	return false
}

func looksLikeCommand(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "[exec]") || strings.HasPrefix(t, "[thinking]") || strings.HasPrefix(t, "$ ") {
		return true
	}
	cmds := []string{
		"git ", "ls", "cat ", "find ", "rg ", "grep ", "dotnet ", "go ", "npm ", "pnpm ", "yarn ",
		"python ", "node ", "bash ", "sh ", "make ", "cargo ", "pwd", "ls -", "cd ",
	}
	for _, c := range cmds {
		if strings.HasPrefix(t, c) {
			return true
		}
	}
	return false
}

var tsLine = regexp.MustCompile(`^\\[[0-9]{4}-[0-9]{2}-[0-9]{2}`)

func isPreambleLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return true
	}
	if tsLine.MatchString(t) {
		return true
	}
	switch {
	case strings.HasPrefix(t, "OpenAI "):
		return true
	case t == "--------":
		return true
	case strings.HasPrefix(strings.ToLower(t), "workdir:"):
		return true
	case strings.HasPrefix(strings.ToLower(t), "model:"):
		return true
	case strings.HasPrefix(strings.ToLower(t), "provider:"):
		return true
	case strings.HasPrefix(strings.ToLower(t), "approval:"):
		return true
	case strings.HasPrefix(strings.ToLower(t), "sandbox:"):
		return true
	case strings.HasPrefix(strings.ToLower(t), "reasoning"):
		return true
	case strings.HasPrefix(strings.ToLower(t), "session id"):
		return true
	case strings.EqualFold(t, "user"):
		return true
	case strings.HasPrefix(strings.ToLower(t), "mcp:"):
		return true
	}
	return false
}

func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blank++
		} else {
			blank = 0
		}
		if blank > 1 {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func isNoiseSee(line, lastCmd, lastSee string) bool {
	l := strings.TrimSpace(line)
	if l == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(l), "/bin/zsh -lc") {
		return true
	}
	// Collapse duplicate see lines from the same command.
	if lastCmd != "" && lastSee != "" && summarizeSee(l) == lastSee {
		return true
	}
	return false
}

func spinnerFrame(idx int) string {
	const barLen = 64
	const period = barLen * 2
	var b strings.Builder
	for i := 0; i < barLen; i++ {
		pos := (idx + i) % period
		if pos >= barLen {
			pos = period - pos
		}
		val := int(float64(pos) * 255.0 / float64(barLen))
		b.WriteString(fmt.Sprintf("\x1b[38;2;0;%d;0m", val))
		b.WriteString("█")
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}
