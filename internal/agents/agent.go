package agents

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/internal/events"
)

// Agent represents a running CLI process and streams its output to the UI.
type Agent struct {
	ID       string
	Name     string
	Prompt   string
	Workdir  string
	LogPath  string
	Model    string
	Display  string
	CLI      CLI
	events   chan<- events.Event
	restarts int

	cmd     *exec.Cmd
	logFile *os.File
	mu      sync.Mutex
}

// Start launches the agent process and begins streaming output.
func (a *Agent) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cmd != nil {
		return fmt.Errorf("agent %s already running", a.ID)
	}

	if err := os.MkdirAll(filepath.Dir(a.LogPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.Create(a.LogPath)
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	a.logFile = logFile

	args := a.CLI.BuildArgs(a.Prompt, a.Model)
	cmd := exec.CommandContext(ctx, a.CLI.Command(), args...)
	cmd.Dir = a.Workdir

	_, _ = fmt.Fprintf(a.logFile, "[%s] %s starting\n", time.Now().Format(time.RFC3339), a.Name)
	_, _ = fmt.Fprintf(a.logFile, "[%s] workdir: %s\n", time.Now().Format(time.RFC3339), a.Workdir)
	_, _ = fmt.Fprintf(a.logFile, "[%s] command: %s %s\n\n", time.Now().Format(time.RFC3339), a.CLI.Command(), strings.Join(args, " "))

	if a.CLI.UseStdin() {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("stdin pipe: %w", err)
		}
		go func() {
			_, _ = io.WriteString(stdin, a.Prompt)
			_ = stdin.Close()
		}()
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	a.cmd = cmd
	display := a.Display
	if display == "" {
		display = a.Model
	}
	a.emit(events.AgentAdded{
		ID:       a.ID,
		Name:     a.Name,
		Kind:     a.CLI.Name(),
		Model:    display,
		LogPath:  a.LogPath,
		Worktree: a.Workdir,
	})

	go a.stream(stdout)
	go a.stream(stderr)
	go a.wait(ctx)

	return nil
}

// Stop terminates the process.
func (a *Agent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cmd == nil || a.cmd.Process == nil {
		return
	}
	_ = a.cmd.Process.Kill()
}

func (a *Agent) stream(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = a.logFile.WriteString(line + "\n")
		a.emit(events.AgentLine{ID: a.ID, Line: line})
	}
}

func (a *Agent) wait(ctx context.Context) {
	err := a.cmd.Wait()
	exit := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exit = exitErr.ExitCode()
		} else {
			exit = 1
		}
	}

	select {
	case <-ctx.Done():
		// Context cancellation: still notify but no restart.
	default:
		a.emit(events.AgentStopped{ID: a.ID, ExitCode: exit})
	}

	a.mu.Lock()
	cmd := a.cmd
	a.cmd = nil
	logFile := a.logFile
	a.logFile = nil
	a.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	if logFile != nil {
		_ = logFile.Close()
	}
}

func (a *Agent) emit(ev events.Event) {
	if a.events == nil {
		return
	}
	select {
	case a.events <- ev:
	default:
		// Drop if channel is full to keep agents flowing.
	}
}
