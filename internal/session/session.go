package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/internal/config"
)

const sessionBaseDir = "swarmgo"

// Session represents a swarm run and contains derived paths.
type Session struct {
	ID       string         `json:"id"`
	Path     string         `json:"path"`
	Options  config.Options `json:"options"`
	Created  time.Time      `json:"created"`
	Complete []int          `json:"complete"`
	mu       sync.Mutex     `json:"-"`
}

// New creates a fresh session stored under the system temp directory.
func New(opts config.Options) (*Session, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(os.TempDir(), sessionBaseDir, id)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	s := &Session{
		ID:       id,
		Path:     path,
		Options:  opts,
		Created:  time.Now(),
		Complete: []int{},
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load restores a session from disk using its ID.
func Load(id string) (*Session, error) {
	path := filepath.Join(os.TempDir(), sessionBaseDir, id)
	cfg := filepath.Join(path, "session.json")
	data, err := os.ReadFile(cfg)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	// Ensure path is set even if JSON lacked it.
	if sess.Path == "" {
		sess.Path = path
	}
	return &sess, nil
}

// WorktreePath returns the path for a worker's git worktree.
func (s *Session) WorktreePath(worker int) string {
	return filepath.Join(s.Path, fmt.Sprintf("wt%d", worker))
}

// PrepWorktreePath returns the path for the one-off prep worktree.
func (s *Session) PrepWorktreePath() string {
	return filepath.Join(s.Path, "prep")
}

// WorkerLogPath returns the log file path for a worker.
func (s *Session) WorkerLogPath(worker int) string {
	return filepath.Join(s.Path, fmt.Sprintf("worker%d.log", worker))
}

// PrepLogPath returns the log file path for the prep agent.
func (s *Session) PrepLogPath() string {
	return filepath.Join(s.Path, "prep.log")
}

// SupervisorLogPath returns the log file path for the supervisor.
func (s *Session) SupervisorLogPath() string {
	return filepath.Join(s.Path, "supervisor.log")
}

// AppLogPath returns the app-level log file path.
func (s *Session) AppLogPath() string {
	return filepath.Join(s.Path, "app.log")
}

// CodedSupervisorPath returns the aggregated supervisor JSON path.
func (s *Session) CodedSupervisorPath() string {
	return filepath.Join(s.Path, "coded-supervisor.json")
}

// IsWorkerCompleted reports whether the worker finished successfully in this session.
func (s *Session) IsWorkerCompleted(worker int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isWorkerCompletedLocked(worker)
}

func (s *Session) isWorkerCompletedLocked(worker int) bool {
	for _, w := range s.Complete {
		if w == worker {
			return true
		}
	}
	return false
}

// MarkWorkerCompleted records a completed worker and persists the session file.
func (s *Session) MarkWorkerCompleted(worker int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isWorkerCompletedLocked(worker) {
		return nil
	}
	s.Complete = append(s.Complete, worker)
	sort.Ints(s.Complete)
	return s.save()
}

func (s *Session) save() error {
	if err := os.MkdirAll(s.Path, 0o755); err != nil {
		return fmt.Errorf("ensure session dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	cfg := filepath.Join(s.Path, "session.json")
	if err := os.WriteFile(cfg, data, 0o644); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

func generateID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}

	timestamp := time.Now().UTC().Format("20060102150405")
	return fmt.Sprintf("%s%s", timestamp, hex.EncodeToString(buf[:])), nil
}
