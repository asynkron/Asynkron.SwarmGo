package status

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/internal/agents"
)

// Snapshot represents a lightweight git/log view for a single agent.
type Snapshot struct {
	Branch        string       `json:"branch"`
	Staged        []FileChange `json:"staged"`
	Unstaged      []FileChange `json:"unstaged"`
	Untracked     []string     `json:"untracked"`
	RecentCommits []string     `json:"recentCommits"`
	Error         string       `json:"error,omitempty"`
	LastPass      *LogEvent    `json:"lastPass,omitempty"`
	LastFail      *LogEvent    `json:"lastFail,omitempty"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}

// FileChange mirrors git numstat output.
type FileChange struct {
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	File    string `json:"file"`
}

// LogEvent captures simple pass/fail signals from logs.
type LogEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
}

// Collector polls git state (and optionally the agent log) for a single worktree.
type Collector struct {
	worktree  string
	logPath   string
	cli       agents.CLI
	interval  time.Duration
	startTime time.Time

	mu        sync.Mutex
	last      Snapshot
	logOffset int64
}

var (
	passRegex = regexp.MustCompile(`(?i)\b(pass(ed)?|success|succeeded|ok|all tests passed|tests passed)\b`)
	failRegex = regexp.MustCompile(`(?i)\b(fail(ed)?|error|exception|traceback|stacktrace|panic|assert|test[s]? failed)\b`)
)

// NewCollector builds a collector for a single repo/worktree.
func NewCollector(worktree string, logPath string, cli agents.CLI, startTime time.Time, interval time.Duration) *Collector {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Collector{
		worktree:  worktree,
		logPath:   logPath,
		cli:       cli,
		interval:  interval,
		startTime: startTime,
		last:      Snapshot{UpdatedAt: time.Now()},
	}
}

// Start begins polling until ctx is canceled. Each poll emits the latest snapshot.
func (c *Collector) Start(ctx context.Context, emit func(Snapshot)) {
	// Do an immediate poll so UI gets data quickly.
	c.pollOnce(ctx, emit)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.pollOnce(ctx, emit)
		}
	}
}

func (c *Collector) pollOnce(ctx context.Context, emit func(Snapshot)) {
	gitSnap := c.collectGit(ctx)
	logSnap := c.collectLogs()

	c.mu.Lock()
	defer c.mu.Unlock()

	if logSnap.LastPass != nil {
		c.last.LastPass = logSnap.LastPass
	}
	if logSnap.LastFail != nil {
		c.last.LastFail = logSnap.LastFail
	}
	if gitSnap.Branch != "" || gitSnap.Error != "" {
		c.last.Branch = gitSnap.Branch
		c.last.Staged = gitSnap.Staged
		c.last.Unstaged = gitSnap.Unstaged
		c.last.Untracked = gitSnap.Untracked
		c.last.RecentCommits = gitSnap.RecentCommits
		c.last.Error = gitSnap.Error
	}
	c.last.UpdatedAt = time.Now()
	emit(c.last)
}

type gitSnapshot struct {
	Branch        string
	Staged        []FileChange
	Unstaged      []FileChange
	Untracked     []string
	RecentCommits []string
	Error         string
}

type logSnapshot struct {
	LastPass *LogEvent
	LastFail *LogEvent
}

func (c *Collector) collectGit(ctx context.Context) gitSnapshot {
	localCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	snap := gitSnapshot{}

	if out, err := runGit(localCtx, c.worktree, "rev-parse --abbrev-ref HEAD"); err == nil {
		snap.Branch = strings.TrimSpace(out)
	} else {
		snap.Error = err.Error()
		return snap
	}

	if staged, err := runGit(localCtx, c.worktree, "diff --cached --numstat"); err == nil {
		snap.Staged = parseNumstat(staged)
	}
	if unstaged, err := runGit(localCtx, c.worktree, "diff --numstat"); err == nil {
		snap.Unstaged = parseNumstat(unstaged)
	}
	if untracked, err := runGit(localCtx, c.worktree, "ls-files --others --exclude-standard"); err == nil {
		snap.Untracked = splitLines(untracked)
	}
	logCmd := "log --oneline -5"
	if !c.startTime.IsZero() {
		logCmd = fmt.Sprintf("log --since=\"%s\" --oneline --max-count=20", c.startTime.Format(time.RFC3339))
	}
	if commits, err := runGit(localCtx, c.worktree, logCmd); err == nil {
		snap.RecentCommits = splitLines(commits)
	}
	return snap
}

func (c *Collector) collectLogs() logSnapshot {
	if c.logPath == "" || c.cli == nil {
		return logSnapshot{}
	}

	data, newOffset, err := c.readNewLogData()
	if err != nil || len(data) == 0 {
		return logSnapshot{}
	}
	lines := strings.Split(data, "\n")
	c.logOffset = newOffset

	var out logSnapshot
	now := time.Now()
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		msgs := c.cli.Parse(line)
		if msgs == nil {
			continue
		}
		for _, msg := range msgs {
			text := trimLine(msg.Text)
			switch {
			case passRegex.MatchString(text):
				out.LastPass = &LogEvent{Timestamp: now, Kind: "pass", Message: text}
			case failRegex.MatchString(text):
				out.LastFail = &LogEvent{Timestamp: now, Kind: "fail", Message: text}
			}
		}
	}
	return out
}

func (c *Collector) readNewLogData() (string, int64, error) {
	f, err := os.Open(c.logPath)
	if err != nil {
		return "", c.logOffset, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", c.logOffset, err
	}
	offset := c.logOffset
	if offset > info.Size() {
		offset = info.Size()
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", offset, err
	}

	data, err := io.ReadAll(bufio.NewReader(f))
	if err != nil {
		return "", offset, err
	}
	return string(data), offset + int64(len(data)), nil
}

func runGit(ctx context.Context, dir string, args string) (string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty git args")
	}
	cmd := exec.CommandContext(ctx, "git", fields...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v", args, err)
	}
	return string(out), nil
}

func parseNumstat(input string) []FileChange {
	lines := splitLines(input)
	changes := make([]FileChange, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		changes = append(changes, FileChange{
			Added:   parseCount(parts[0]),
			Deleted: parseCount(parts[1]),
			File:    parts[2],
		})
	}
	return changes
}

func parseCount(val string) int {
	i, _ := strconv.Atoi(val)
	return i
}

func splitLines(input string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func trimLine(line string) string {
	const max = 500
	if len(line) <= max {
		return line
	}
	return line[:max]
}
