package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/internal/agents"
	"github.com/asynkron/Asynkron.SwarmGo/internal/config"
	"github.com/asynkron/Asynkron.SwarmGo/internal/control"
	"github.com/asynkron/Asynkron.SwarmGo/internal/events"
	"github.com/asynkron/Asynkron.SwarmGo/internal/prompts"
	"github.com/asynkron/Asynkron.SwarmGo/internal/session"
	"github.com/asynkron/Asynkron.SwarmGo/internal/status"
	"github.com/asynkron/Asynkron.SwarmGo/internal/supervisor"
	"github.com/asynkron/Asynkron.SwarmGo/internal/worktree"
)

// Orchestrator coordinates workers, supervisor, and UI events.
type Orchestrator struct {
	session *session.Session
	opts    config.Options
	resume  bool
	events  chan<- events.Event
	control <-chan control.Command

	mu      sync.Mutex
	agents  []*agents.Agent
	started bool

	codedSupervisor *supervisor.CodedSupervisor
	appLog          *appLogger
	workerSpecs     map[string]workerSpec
	supervisorSpec  *supervisorSpec
	userSpec        *userCommandSpec
	agentRestarts   map[string]int
	collectors      map[string]context.CancelFunc
}

// New constructs a new Orchestrator.
func New(sess *session.Session, opts config.Options, resume bool, events chan<- events.Event, control <-chan control.Command) *Orchestrator {
	return &Orchestrator{
		session:       sess,
		opts:          opts,
		resume:        resume,
		events:        events,
		control:       control,
		workerSpecs:   make(map[string]workerSpec),
		agentRestarts: make(map[string]int),
		collectors:    make(map[string]context.CancelFunc),
	}
}

// Run executes a single swarm round. It blocks until the context is canceled or the round completes.
func (o *Orchestrator) Run(ctx context.Context) error {
	if o.started {
		return fmt.Errorf("orchestrator already running")
	}
	o.started = true
	defer func() {
		if o.appLog != nil {
			o.appLog.Close()
		}
		if o.codedSupervisor != nil {
			o.codedSupervisor.Close()
		}
		o.stopAllCollectors()
	}()

	if log, err := newAppLogger(o.session.AppLogPath(), o.events); err == nil {
		o.appLog = log
		o.emit(events.AgentAdded{
			ID:       "app",
			Name:     "App",
			Kind:     "Log",
			Model:    "",
			LogPath:  o.session.AppLogPath(),
			Worktree: o.session.Path,
			Running:  true,
		})
		o.logf("orchestrator starting (resume=%v)", o.resume)
	} else {
		o.emit(events.StatusMessage{Message: fmt.Sprintf("app log unavailable: %v", err)})
	}

	o.emit(events.StatusMessage{Message: fmt.Sprintf("Session: %s", o.session.ID)})
	o.emit(events.StatusMessage{Message: fmt.Sprintf("Repository: %s", o.opts.Repo)})
	if o.opts.AgentMode {
		o.emit(events.StatusMessage{Message: fmt.Sprintf("Agent mode: %s", strings.Title(string(o.opts.AgentType)))})
	} else {
		o.emit(events.StatusMessage{Message: fmt.Sprintf("Workers: Claude %d, Codex %d, Copilot %d, Gemini %d", o.opts.ClaudeWorkers, o.opts.CodexWorkers, o.opts.CopilotWorkers, o.opts.GeminiWorkers)})
	}
	if o.resume {
		o.logf("resuming session %s", o.session.ID)
	} else {
		o.logf("new session %s", o.session.ID)
	}

	// Prime todo content
	o.loadTodo()

	if o.opts.AgentMode {
		return o.runAgentMode(ctx)
	}

	restartCount := 0
	if o.resume {
		restartCount = 1
	}

	worktrees := o.buildWorktreePaths()
	workerTypes := o.buildWorkerTypes()

	if o.resume {
		o.emit(events.PhaseChanged{Phase: "Resuming session..."})
		if err := o.ensureWorktrees(worktrees); err != nil {
			o.logf("worktree check failed: %v", err)
			return err
		}
	} else {
		o.emit(events.PhaseChanged{Phase: "Preparing test script..."})
		prepPath := o.session.PrepWorktreePath()
		baseRef := "HEAD"
		if err := worktree.CreateFromRef(ctx, o.opts.Repo, []string{prepPath}, baseRef); err != nil {
			return err
		}
		ref, err := o.runPrep(ctx, prepPath)
		if err != nil {
			return err
		}
		baseRef = ref

		o.emit(events.PhaseChanged{Phase: "Creating worktrees..."})
		if err := worktree.CreateFromRef(ctx, o.opts.Repo, worktrees, baseRef); err != nil {
			return err
		}
	}

	// Start agents
	if o.resume {
		o.emit(events.PhaseChanged{Phase: "Resuming workers..."})
		o.logf("resuming workers")
	} else {
		o.emit(events.PhaseChanged{Phase: "Starting workers..."})
		o.logf("starting workers")
	}
	ghAvailable := checkGhAvailable()
	isGitHubRepo := checkGitHubRepo(o.opts.Repo)

	workers, workerLogs, workerTypes, err := o.startWorkers(ctx, worktrees, workerTypes, ghAvailable, isGitHubRepo, restartCount)
	if err != nil {
		o.stopAll()
		o.logf("worker start failed: %v", err)
		return err
	}
	o.logf("workers started/resumed: %d active", len(workers))

	if o.resume {
		o.emit(events.PhaseChanged{Phase: "Resuming supervisor..."})
		o.logf("resuming supervisor")
	} else {
		o.emit(events.PhaseChanged{Phase: "Starting supervisor..."})
		o.logf("starting supervisor")
	}
	supervisor, err := o.startSupervisor(ctx, worktrees, workerLogs, workerTypes, ghAvailable, isGitHubRepo, restartCount)
	if err != nil {
		o.stopAll()
		o.logf("supervisor start failed: %v", err)
		return err
	}

	userCLI := agents.NewCLI(o.opts.Supervisor)
	userLog := o.session.UserCommandLogPath()
	_, userDisplay := userCLI.Model(len(worktrees) + 2)
	if sm, ok := userCLI.(agents.SupervisorModeler); ok {
		_, userDisplay = sm.SupervisorModel()
	}
	o.userSpec = &userCommandSpec{
		worktrees:    worktrees,
		repoPath:     o.opts.Repo,
		cli:          userCLI,
		logPath:      userLog,
		ghAvailable:  ghAvailable,
		isGitHubRepo: isGitHubRepo,
	}
	o.agentRestarts["user-command"] = 0
	o.emit(events.AgentAdded{
		ID:       "user-command",
		Name:     "User Command",
		Kind:     userCLI.Name(),
		Model:    userDisplay,
		LogPath:  userLog,
		Worktree: o.opts.Repo,
		Running:  false,
	})
	o.emit(events.StatusMessage{Message: "User command agent is stopped; press Enter to inject a prompt or Space to start/stop."})
	o.emit(events.PhaseChanged{Phase: "Workers running..."})

	// Tick remaining time
	deadline := time.Now().Add(o.opts.Duration())
	timeout := time.NewTimer(o.opts.Duration())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer timeout.Stop()

loop:
	for {
		select {
		case cmd := <-o.control:
			if err := o.handleControl(ctx, cmd); err != nil {
				o.emit(events.StatusMessage{Message: fmt.Sprintf("control error: %v", err)})
			}
		case <-ctx.Done():
			o.emit(events.StatusMessage{Message: "Cancellation requested, stopping agents..."})
			o.stopAll()
			return ctx.Err()
		case <-timeout.C:
			o.emit(events.StatusMessage{Message: "Time limit reached, stopping workers..."})
			o.emit(events.PhaseChanged{Phase: "Stopping workers..."})
			for _, w := range workers {
				w.Stop()
			}
			// Wait a short grace period for supervisor to finish.
			go func() {
				time.Sleep(30 * time.Second)
				supervisor.Stop()
			}()
			break loop
		case <-ticker.C:
			remaining := time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
			o.emit(events.RemainingTime{Duration: remaining})
		}
	}

	o.emit(events.RemainingTime{Duration: 0})
	o.emit(events.PhaseChanged{Phase: "Round finished"})
	o.emit(events.StatusMessage{Message: "Round finished"})
	return nil
}

func (o *Orchestrator) runAgentMode(ctx context.Context) error {
	restartCount := 0
	if o.resume {
		restartCount = 1
	}

	if o.resume {
		o.emit(events.PhaseChanged{Phase: "Resuming agent..."})
		o.logf("resuming single agent")
	} else {
		o.emit(events.PhaseChanged{Phase: "Starting agent..."})
		o.logf("starting single agent")
	}

	ghAvailable := checkGhAvailable()
	isGitHubRepo := checkGitHubRepo(o.opts.Repo)
	logPath := o.session.WorkerLogPath(1)
	cli := agents.NewCLI(o.opts.AgentType)
	_, display := cli.Model(0)

	o.emit(events.AgentAdded{
		ID:       "worker-1",
		Name:     "Agent",
		Kind:     cli.Name(),
		Model:    display,
		LogPath:  logPath,
		Worktree: o.opts.Repo,
		Running:  true,
	})

	worker := agents.NewWorker(0, o.opts.Repo, o.opts.Todo, cli, logPath, false, "", restartCount, ghAvailable, isGitHubRepo, o.events)
	if err := worker.Start(ctx); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	go o.trackCompletion(1, worker)
	o.workerSpecs["worker-1"] = workerSpec{
		index:        0,
		worktree:     o.opts.Repo,
		todoFile:     o.opts.Todo,
		cli:          cli,
		logPath:      logPath,
		autopilot:    false,
		branchName:   "",
		ghAvailable:  ghAvailable,
		isGitHubRepo: isGitHubRepo,
	}
	o.agentRestarts["worker-1"] = restartCount
	o.track(worker)
	o.startCollector(ctx, "worker-1", o.opts.Repo, logPath, cli)
	if restartCount > 0 {
		o.emit(events.StatusMessage{Message: fmt.Sprintf("Resumed agent (%s)", cli.Name())})
	} else {
		o.emit(events.StatusMessage{Message: fmt.Sprintf("Started agent (%s)", cli.Name())})
	}
	o.emit(events.PhaseChanged{Phase: "Agent running..."})

	deadline := time.Now().Add(o.opts.Duration())
	timeout := time.NewTimer(o.opts.Duration())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer timeout.Stop()

	for {
		select {
		case cmd := <-o.control:
			if err := o.handleControl(ctx, cmd); err != nil {
				o.emit(events.StatusMessage{Message: fmt.Sprintf("control error: %v", err)})
			}
		case <-ctx.Done():
			o.emit(events.StatusMessage{Message: "Cancellation requested, stopping agent..."})
			o.stopAll()
			return ctx.Err()
		case <-timeout.C:
			o.emit(events.StatusMessage{Message: "Time limit reached, stopping agent..."})
			o.emit(events.PhaseChanged{Phase: "Stopping agent..."})
			worker.Stop()
			o.emit(events.RemainingTime{Duration: 0})
			o.emit(events.PhaseChanged{Phase: "Agent finished"})
			return nil
		case <-ticker.C:
			remaining := time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
			o.emit(events.RemainingTime{Duration: remaining})
		}
	}
}

func (o *Orchestrator) startWorkers(ctx context.Context, worktrees []string, workerTypes []config.AgentType, ghAvailable bool, isGitHubRepo bool, restartCount int) ([]*agents.Agent, []string, []config.AgentType, error) {
	var workers []*agents.Agent
	var logs []string

	timestamp := time.Now().Format("20060102-150405")
	o.logf("startWorkers: resume=%v restartCount=%d worktrees=%d workerTypes=%d", o.resume, restartCount, len(worktrees), len(workerTypes))

	// If workerTypes is shorter than worktrees (e.g., stale session options), extend it using the last known type.
	if len(workerTypes) > 0 && len(workerTypes) < len(worktrees) {
		last := workerTypes[len(workerTypes)-1]
		for len(workerTypes) < len(worktrees) {
			workerTypes = append(workerTypes, last)
		}
		o.logf("extended workerTypes to match worktrees (filled with %s)", last)
	}
	if len(workerTypes) == 0 {
		workerTypes = append(workerTypes, config.AgentCodex)
		o.logf("workerTypes empty; defaulting first worker to %s", config.AgentCodex)
	}

	for i := range worktrees {
		if i >= len(workerTypes) {
			err := fmt.Errorf("missing worker type for index %d (have %d)", i, len(workerTypes))
			o.logf("%v", err)
			o.emit(events.StatusMessage{Message: err.Error()})
			return nil, nil, nil, fmt.Errorf("missing worker type for index %d", i)
		}
		workerNum := i + 1
		logPath := o.session.WorkerLogPath(workerNum)
		prevComplete := o.session.IsWorkerCompleted(workerNum)
		agentType := workerTypes[i]
		cli := agents.NewCLI(agentType)
		_, display := cli.Model(i)
		branchName := ""
		if o.opts.Autopilot {
			branchName = fmt.Sprintf("autopilot/worker%d-%s", i+1, timestamp)
			if restartCount > 0 {
				// Avoid telling the worker to create a new branch on resume; stick with whatever exists.
				branchName = ""
			}
		}

		o.logf("starting worker %d (%s) worktree=%s log=%s", workerNum, cli.Name(), worktrees[i], logPath)
		o.emit(events.AgentAdded{
			ID:       fmt.Sprintf("worker-%d", workerNum),
			Name:     fmt.Sprintf("Worker %d", workerNum),
			Kind:     cli.Name(),
			Model:    display,
			LogPath:  logPath,
			Worktree: worktrees[i],
			Running:  true,
		})
		worker := agents.NewWorker(i, worktrees[i], o.opts.Todo, cli, logPath, o.opts.Autopilot, branchName, restartCount, ghAvailable, isGitHubRepo, o.events)
		if err := worker.Start(ctx); err != nil {
			return nil, nil, nil, fmt.Errorf("start worker %d: %w", workerNum, err)
		}
		go o.trackCompletion(workerNum, worker)
		o.workerSpecs[fmt.Sprintf("worker-%d", workerNum)] = workerSpec{
			index:        i,
			worktree:     worktrees[i],
			todoFile:     o.opts.Todo,
			cli:          cli,
			logPath:      logPath,
			autopilot:    o.opts.Autopilot,
			branchName:   branchName,
			ghAvailable:  ghAvailable,
			isGitHubRepo: isGitHubRepo,
		}
		o.agentRestarts[fmt.Sprintf("worker-%d", workerNum)] = restartCount

		workers = append(workers, worker)
		logs = append(logs, logPath)
		o.track(worker)
		o.startCollector(ctx, fmt.Sprintf("worker-%d", workerNum), worktrees[i], logPath, cli)
		if restartCount > 0 {
			o.emit(events.StatusMessage{Message: fmt.Sprintf("Resumed %s (%s) -> %s", worker.Name, cli.Name(), worktrees[i])})
			o.logf("resumed %s (%s) -> %s (log: %s; previously complete=%v)", worker.Name, cli.Name(), worktrees[i], logPath, prevComplete)
		} else {
			o.emit(events.StatusMessage{Message: fmt.Sprintf("Started %s (%s) -> %s", worker.Name, cli.Name(), worktrees[i])})
			o.logf("started %s (%s) -> %s (log: %s)", worker.Name, cli.Name(), worktrees[i], logPath)
		}
	}

	o.logf("startWorkers completed: started=%d logs=%d", len(workers), len(logs))
	return workers, logs, workerTypes, nil
}

func (o *Orchestrator) startSupervisor(ctx context.Context, worktrees, workerLogs []string, workerTypes []config.AgentType, ghAvailable bool, isGitHubRepo bool, restartCount int) (*agents.Agent, error) {
	// Start coded supervisor collector in the background for aggregated signals.
	if o.codedSupervisor == nil {
		o.codedSupervisor = supervisor.NewCodedSupervisor(o.session.CodedSupervisorPath(), worktrees, workerLogs, workerTypes, o.session.Created, 5*time.Second)
		o.codedSupervisor.Start()
	}

	cli := agents.NewCLI(o.opts.Supervisor)
	o.logf("starting supervisor (%s)", cli.Name())
	_, display := cli.Model(len(worktrees) + 1)
	if sm, ok := cli.(agents.SupervisorModeler); ok {
		_, display = sm.SupervisorModel()
	}
	o.emit(events.AgentAdded{
		ID:       "supervisor",
		Name:     "Supervisor",
		Kind:     cli.Name(),
		Model:    display,
		LogPath:  o.session.SupervisorLogPath(),
		Worktree: o.opts.Repo,
		Running:  true,
	})
	supervisor := agents.NewSupervisor(worktrees, workerLogs, o.opts.Repo, o.session.CodedSupervisorPath(), cli, o.session.SupervisorLogPath(), o.opts.Autopilot, restartCount, ghAvailable, isGitHubRepo, o.events)
	if err := supervisor.Start(ctx); err != nil {
		return nil, err
	}

	o.track(supervisor)
	o.startCollector(ctx, "supervisor", o.opts.Repo, o.session.SupervisorLogPath(), cli)
	o.emit(events.StatusMessage{Message: fmt.Sprintf("Started supervisor (%s)", cli.Name())})
	o.logf("supervisor started")
	o.supervisorSpec = &supervisorSpec{
		worktrees:    worktrees,
		workerLogs:   workerLogs,
		workerTypes:  workerTypes,
		repoPath:     o.opts.Repo,
		codedPath:    o.session.CodedSupervisorPath(),
		cli:          cli,
		logPath:      o.session.SupervisorLogPath(),
		autopilot:    o.opts.Autopilot,
		ghAvailable:  ghAvailable,
		isGitHubRepo: isGitHubRepo,
		restartCount: restartCount,
	}
	o.agentRestarts["supervisor"] = restartCount
	return supervisor, nil
}

func (o *Orchestrator) stopAll() {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, a := range o.agents {
		a.Stop()
	}
}

func (o *Orchestrator) stopAllCollectors() {
	for id := range o.collectors {
		o.stopCollector(id)
	}
}

func (o *Orchestrator) stopCollector(id string) {
	if cancel, ok := o.collectors[id]; ok {
		cancel()
		delete(o.collectors, id)
	}
}

func (o *Orchestrator) startCollector(ctx context.Context, id, worktree, logPath string, cli agents.CLI) {
	o.stopCollector(id)
	cctx, cancel := context.WithCancel(ctx)
	o.collectors[id] = cancel
	coll := status.NewCollector(worktree, logPath, cli, o.session.Created, 5*time.Second)
	go coll.Start(cctx, func(s status.Snapshot) {
		o.emit(events.AgentStatus{ID: id, Snapshot: convertStatusSnapshot(s)})
	})
}

func convertStatusSnapshot(s status.Snapshot) events.StatusSnapshot {
	out := events.StatusSnapshot{
		Branch:        s.Branch,
		Untracked:     append([]string(nil), s.Untracked...),
		RecentCommits: append([]string(nil), s.RecentCommits...),
		Error:         s.Error,
		UpdatedAt:     s.UpdatedAt,
	}
	for _, fc := range s.Staged {
		out.Staged = append(out.Staged, events.StatusFileChange{Added: fc.Added, Deleted: fc.Deleted, File: fc.File})
	}
	for _, fc := range s.Unstaged {
		out.Unstaged = append(out.Unstaged, events.StatusFileChange{Added: fc.Added, Deleted: fc.Deleted, File: fc.File})
	}
	if s.LastPass != nil {
		out.LastPass = &events.StatusLogEvent{Timestamp: s.LastPass.Timestamp, Kind: s.LastPass.Kind, Message: s.LastPass.Message}
	}
	if s.LastFail != nil {
		out.LastFail = &events.StatusLogEvent{Timestamp: s.LastFail.Timestamp, Kind: s.LastFail.Kind, Message: s.LastFail.Message}
	}
	return out
}

func (o *Orchestrator) logf(format string, args ...any) {
	if o.appLog != nil {
		o.appLog.Logf(format, args...)
	}
}

func (o *Orchestrator) track(a *agents.Agent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.agents = append(o.agents, a)
}

func (o *Orchestrator) handleControl(ctx context.Context, cmd control.Command) error {
	switch c := cmd.(type) {
	case control.RestartAgent:
		return o.restartAgent(ctx, c.AgentID, c.Message)
	case control.StopAgent:
		return o.stopAgent(c.AgentID)
	case control.StartAgent:
		return o.restartAgent(ctx, c.AgentID, "")
	case control.StartUserCommand:
		return o.restartAgent(ctx, "user-command", c.Message)
	default:
		return fmt.Errorf("unknown control command %T", cmd)
	}
}

func (o *Orchestrator) restartAgent(ctx context.Context, id string, message string) error {
	o.logf("control: restarting %s with injected message length=%d", id, len(message))
	o.stopCollector(id)
	o.mu.Lock()
	var target *agents.Agent
	var remaining []*agents.Agent
	for _, a := range o.agents {
		if a.ID == id {
			target = a
			continue
		}
		remaining = append(remaining, a)
	}
	o.agents = remaining
	o.mu.Unlock()

	if target != nil {
		target.Stop()
	}

	if spec, ok := o.workerSpecs[id]; ok {
		return o.restartWorker(ctx, id, spec, message)
	}
	if o.supervisorSpec != nil && id == "supervisor" {
		return o.restartSupervisor(ctx, id, message)
	}
	if o.userSpec != nil && id == "user-command" {
		return o.restartUserCommand(ctx, id, message)
	}
	if target == nil {
		return fmt.Errorf("agent %s not found", id)
	}
	return fmt.Errorf("no restart spec for %s", id)
}

func (o *Orchestrator) stopAgent(id string) error {
	o.logf("control: stopping %s", id)
	o.stopCollector(id)
	o.mu.Lock()
	var target *agents.Agent
	for _, a := range o.agents {
		if a.ID == id {
			target = a
			break
		}
	}
	o.mu.Unlock()
	if target == nil {
		return fmt.Errorf("agent %s not found", id)
	}
	target.Stop()
	o.emit(events.StatusMessage{Message: fmt.Sprintf("Stopped %s", id)})
	return nil
}

func (o *Orchestrator) buildWorktreePaths() []string {
	paths := make([]string, 0, o.opts.TotalWorkers())
	for i := 0; i < o.opts.TotalWorkers(); i++ {
		paths = append(paths, o.session.WorktreePath(i+1))
	}
	return paths
}

func (o *Orchestrator) ensureWorktrees(paths []string) error {
	for i, p := range paths {
		workerNum := i + 1
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("worktree missing for worker %d: %s (%v)", workerNum, p, err)
		}
	}
	return nil
}

func (o *Orchestrator) trackCompletion(workerNumber int, agent *agents.Agent) {
	ch := agent.Done()
	if ch == nil {
		return
	}
	<-ch
	if agent.ExitCode() == 0 {
		if err := o.session.MarkWorkerCompleted(workerNumber); err != nil {
			o.emit(events.StatusMessage{Message: fmt.Sprintf("save session: %v", err)})
		}
	}
}

func (o *Orchestrator) buildWorkerTypes() []config.AgentType {
	var types []config.AgentType
	for i := 0; i < o.opts.ClaudeWorkers; i++ {
		types = append(types, config.AgentClaude)
	}
	for i := 0; i < o.opts.CodexWorkers; i++ {
		types = append(types, config.AgentCodex)
	}
	for i := 0; i < o.opts.CopilotWorkers; i++ {
		types = append(types, config.AgentCopilot)
	}
	for i := 0; i < o.opts.GeminiWorkers; i++ {
		types = append(types, config.AgentGemini)
	}
	return types
}

func (o *Orchestrator) loadTodo() {
	todoPath := filepath.Join(o.opts.Repo, o.opts.Todo)
	content, err := os.ReadFile(todoPath)
	if err != nil {
		o.logf("todo load failed: %v", err)
		return
	}
	o.emit(events.TodoLoaded{Content: string(content), Path: todoPath})
}

func (o *Orchestrator) runPrep(ctx context.Context, prepPath string) (string, error) {
	cli := agents.NewCLI(o.opts.PrepAgent)
	logPath := o.session.PrepLogPath()

	prep := agents.NewPrep(prepPath, filepath.Join(prepPath, o.opts.Todo), cli, logPath, o.events)

	if err := prep.Start(ctx); err != nil {
		return "", fmt.Errorf("start prep agent: %w", err)
	}
	select {
	case <-ctx.Done():
		prep.Stop()
		return "", ctx.Err()
	case <-prep.Done():
	}

	if exit := prep.ExitCode(); exit != 0 {
		return "", fmt.Errorf("prep agent failed with exit code %d (see %s)", exit, logPath)
	}

	ref, err := o.snapshotPrep(ctx, prepPath)
	if err != nil {
		return "", err
	}

	o.emit(events.StatusMessage{Message: fmt.Sprintf("Prep complete; using %s for worker worktrees", ref)})
	return ref, nil
}

func (o *Orchestrator) snapshotPrep(ctx context.Context, prepPath string) (string, error) {
	status, err := gitOutput(ctx, prepPath, "status", "--porcelain")
	if err != nil {
		o.logf("prep git status failed: %v", err)
		return "", err
	}

	if strings.TrimSpace(status) != "" {
		if err := runGit(ctx, prepPath, "add", "-A"); err != nil {
			o.logf("prep git add failed: %v", err)
			return "", err
		}
		if err := runGit(ctx, prepPath, "commit", "-m", "swarm prep: targeted tests"); err != nil {
			o.logf("prep git commit failed: %v", err)
			return "", err
		}
	}

	ref, err := gitOutput(ctx, prepPath, "rev-parse", "HEAD")
	if err != nil {
		o.logf("prep rev-parse failed: %v", err)
		return "", err
	}
	return strings.TrimSpace(ref), nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutput(ctx, dir, args...)
	return err
}

func checkGhAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

func checkGitHubRepo(repoPath string) bool {
	if repoPath == "" {
		return false
	}
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	remote := strings.ToLower(strings.TrimSpace(string(out)))
	return strings.Contains(remote, "github.com")
}

func (o *Orchestrator) emit(ev events.Event) {
	if o.events == nil {
		return
	}
	switch ev.(type) {
	case events.AgentAdded:
		// Agent listings are critical to the UI; block briefly rather than drop.
		o.events <- ev
	default:
		select {
		case o.events <- ev:
		default:
			o.logf("dropping event %T: channel full", ev)
		}
	}
}

type appLogger struct {
	file   *os.File
	events chan<- events.Event
}

func newAppLogger(path string, events chan<- events.Event) (*appLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &appLogger{file: f, events: events}, nil
}

func (l *appLogger) Logf(format string, args ...any) {
	if l == nil || l.file == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	timestamped := fmt.Sprintf("%s %s", time.Now().Format("15:04:05"), line)
	_, _ = fmt.Fprintln(l.file, timestamped)
	l.emit(timestamped)
}

func (l *appLogger) emit(line string) {
	defer func() { _ = recover() }()
	select {
	case l.events <- events.AgentLine{ID: "app", Kind: events.MessageSay, Line: line}:
	default:
	}
}

func (l *appLogger) Close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}

type workerSpec struct {
	index        int
	worktree     string
	todoFile     string
	cli          agents.CLI
	logPath      string
	autopilot    bool
	branchName   string
	ghAvailable  bool
	isGitHubRepo bool
}

type supervisorSpec struct {
	worktrees    []string
	workerLogs   []string
	workerTypes  []config.AgentType
	repoPath     string
	codedPath    string
	cli          agents.CLI
	logPath      string
	autopilot    bool
	ghAvailable  bool
	isGitHubRepo bool
	restartCount int
}

type userCommandSpec struct {
	worktrees    []string
	repoPath     string
	cli          agents.CLI
	logPath      string
	ghAvailable  bool
	isGitHubRepo bool
}

func (o *Orchestrator) restartWorker(ctx context.Context, id string, spec workerSpec, message string) error {
	restartCount := o.agentRestarts[id] + 1
	prompt := prompts.WorkerPrompt(spec.todoFile, fmt.Sprintf("Worker %d", spec.index+1), spec.autopilot, spec.branchName, spec.logPath, restartCount, spec.ghAvailable, spec.isGitHubRepo)
	if strings.TrimSpace(message) != "" {
		prompt = fmt.Sprintf("SYSTEM RESUME NOTE: %s\n\n%s", message, prompt)
	}

	worker := agents.NewWorker(spec.index, spec.worktree, spec.todoFile, spec.cli, spec.logPath, spec.autopilot, spec.branchName, restartCount, spec.ghAvailable, spec.isGitHubRepo, o.events)
	worker.Prompt = prompt
	if err := worker.Start(ctx); err != nil {
		return fmt.Errorf("restart %s: %w", id, err)
	}
	_, display := spec.cli.Model(spec.index)
	o.emit(events.AgentAdded{
		ID:       id,
		Name:     fmt.Sprintf("Worker %d", spec.index+1),
		Kind:     spec.cli.Name(),
		Model:    display,
		LogPath:  spec.logPath,
		Worktree: spec.worktree,
		Running:  true,
	})

	o.mu.Lock()
	o.agents = append(o.agents, worker)
	o.agentRestarts[id] = restartCount
	o.mu.Unlock()
	o.startCollector(ctx, id, spec.worktree, spec.logPath, spec.cli)
	o.logf("restarted %s (restartCount=%d)", id, restartCount)
	o.emit(events.StatusMessage{Message: fmt.Sprintf("Restarted %s with injected note", id)})
	return nil
}

func (o *Orchestrator) restartSupervisor(ctx context.Context, id string, message string) error {
	if o.supervisorSpec == nil {
		return fmt.Errorf("no supervisor spec to restart")
	}
	restartCount := o.agentRestarts[id] + 1
	spec := o.supervisorSpec
	prompt := prompts.SupervisorPrompt(spec.worktrees, spec.workerLogs, spec.repoPath, spec.codedPath, spec.autopilot, restartCount, spec.ghAvailable, spec.isGitHubRepo)
	if strings.TrimSpace(message) != "" {
		prompt = fmt.Sprintf("SYSTEM RESUME NOTE: %s\n\n%s", message, prompt)
	}

	sup := agents.NewSupervisor(spec.worktrees, spec.workerLogs, spec.repoPath, spec.codedPath, spec.cli, spec.logPath, spec.autopilot, restartCount, spec.ghAvailable, spec.isGitHubRepo, o.events)
	sup.Prompt = prompt
	if err := sup.Start(ctx); err != nil {
		return fmt.Errorf("restart %s: %w", id, err)
	}
	_, display := spec.cli.Model(len(spec.worktrees) + 1)
	if sm, ok := spec.cli.(agents.SupervisorModeler); ok {
		_, display = sm.SupervisorModel()
	}
	o.emit(events.AgentAdded{
		ID:       "supervisor",
		Name:     "Supervisor",
		Kind:     spec.cli.Name(),
		Model:    display,
		LogPath:  spec.logPath,
		Worktree: spec.repoPath,
		Running:  true,
	})

	o.mu.Lock()
	o.agents = append(o.agents, sup)
	o.agentRestarts[id] = restartCount
	o.mu.Unlock()
	o.startCollector(ctx, id, spec.repoPath, spec.logPath, spec.cli)
	o.logf("restarted supervisor (restartCount=%d)", restartCount)
	o.emit(events.StatusMessage{Message: "Restarted supervisor with injected note"})
	return nil
}

func (o *Orchestrator) restartUserCommand(ctx context.Context, id string, message string) error {
	if o.userSpec == nil {
		return fmt.Errorf("no user command spec to start")
	}
	startCount := o.agentRestarts[id] + 1
	agent := agents.NewUserCommand(o.userSpec.worktrees, o.userSpec.repoPath, o.userSpec.cli, o.userSpec.logPath, message, o.events)
	if err := agent.Start(ctx); err != nil {
		return fmt.Errorf("start %s: %w", id, err)
	}
	o.mu.Lock()
	o.agents = append(o.agents, agent)
	o.agentRestarts[id] = startCount
	o.mu.Unlock()
	o.startCollector(ctx, id, o.userSpec.repoPath, o.userSpec.logPath, o.userSpec.cli)
	o.logf("started user command (starts=%d messageLen=%d)", startCount, len(message))
	if strings.TrimSpace(message) == "" {
		o.emit(events.StatusMessage{Message: "Started user command agent"})
	} else {
		o.emit(events.StatusMessage{Message: "Started user command with injected note"})
	}
	return nil
}
