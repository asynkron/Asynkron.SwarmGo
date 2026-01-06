package viewer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/internal/events"
)

// Viewer coordinates watching external Claude agent log files.
type Viewer struct {
	repoPath string
	tasksDir string
	events   chan<- events.Event
	watchers map[string]*Watcher
	mu       sync.Mutex
}

// New creates a new Viewer for the given repository path.
func New(repoPath string, eventCh chan<- events.Event) *Viewer {
	return &Viewer{
		repoPath: repoPath,
		tasksDir: deriveTasksDir(repoPath),
		events:   eventCh,
		watchers: make(map[string]*Watcher),
	}
}

// TasksDir returns the derived tasks directory path.
func (v *Viewer) TasksDir() string {
	return v.tasksDir
}

// Run starts the viewer and blocks until the context is cancelled.
func (v *Viewer) Run(ctx context.Context) error {
	v.emit(events.StatusMessage{Message: fmt.Sprintf("Viewer starting, watching: %s", v.tasksDir)})

	// Check if the tasks directory exists
	if _, err := os.Stat(v.tasksDir); os.IsNotExist(err) {
		v.emit(events.StatusMessage{Message: fmt.Sprintf("Tasks directory not found: %s", v.tasksDir)})
		v.emit(events.StatusMessage{Message: "Waiting for Claude agents to start..."})
	} else {
		v.emit(events.StatusMessage{Message: "Tasks directory exists, scanning..."})
	}

	// Initial scan
	v.scan(ctx)

	// Periodic rescan every 2 seconds
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			v.stopAll()
			return ctx.Err()
		case <-ticker.C:
			v.scan(ctx)
		}
	}
}

// scan checks for new/removed agent log files.
func (v *Viewer) scan(ctx context.Context) {
	entries, err := os.ReadDir(v.tasksDir)
	if err != nil {
		// Directory doesn't exist yet, that's fine
		return
	}

	// Track which files we found
	found := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".output") {
			continue
		}

		// Extract agent ID from filename (e.g., "aaf0b8d.output" -> "aaf0b8d")
		id := strings.TrimSuffix(name, ".output")
		found[id] = true

		v.mu.Lock()
		_, exists := v.watchers[id]
		v.mu.Unlock()

		if !exists {
			logPath := filepath.Join(v.tasksDir, name)
			v.emit(events.StatusMessage{Message: fmt.Sprintf("Found agent: %s", id)})
			watcher := NewWatcher(id, logPath, v.events)
			v.mu.Lock()
			v.watchers[id] = watcher
			v.mu.Unlock()
			watcher.Start(ctx)
		}
	}

	// Remove watchers for files that no longer exist
	v.mu.Lock()
	for id, watcher := range v.watchers {
		if !found[id] {
			watcher.Stop()
			delete(v.watchers, id)
		}
	}
	v.mu.Unlock()
}

// stopAll stops all watchers.
func (v *Viewer) stopAll() {
	v.mu.Lock()
	defer v.mu.Unlock()

	for _, watcher := range v.watchers {
		watcher.Stop()
	}
	v.watchers = make(map[string]*Watcher)
}

func (v *Viewer) emit(ev events.Event) {
	if v.events == nil {
		return
	}
	defer func() { _ = recover() }()
	select {
	case v.events <- ev:
	default:
	}
}

// deriveTasksDir converts a repo path to the Claude tasks directory.
// Example: /Users/foo/bar.baz -> /tmp/claude/-Users-foo-bar-baz/tasks/
func deriveTasksDir(repoPath string) string {
	// Claude sanitizes paths by replacing / and . with -
	sanitized := strings.ReplaceAll(repoPath, "/", "-")
	sanitized = strings.ReplaceAll(sanitized, ".", "-")
	// Claude uses /tmp/claude/ directly, not os.TempDir() which may differ on macOS
	return filepath.Join("/tmp", "claude", sanitized, "tasks")
}
