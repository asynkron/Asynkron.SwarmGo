package viewer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/asynkron/Asynkron.SwarmGo/internal/agents"
	"github.com/asynkron/Asynkron.SwarmGo/internal/events"
)

// todoItem represents a single todo from TodoWrite
type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

// todoPayload represents the TodoWrite JSON payload
type todoPayload struct {
	Todos []todoItem `json:"todos"`
}

// editPayload represents the Edit tool JSON payload
type editPayload struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// Watcher tails an external Claude agent log file and emits parsed events.
type Watcher struct {
	ID      string
	LogPath string
	events  chan<- events.Event

	mu         sync.Mutex
	tailCancel context.CancelFunc
	tailWG     sync.WaitGroup
	running    bool
}

// NewWatcher creates a watcher for the given agent log file.
func NewWatcher(id, logPath string, eventCh chan<- events.Event) *Watcher {
	return &Watcher{
		ID:      id,
		LogPath: logPath,
		events:  eventCh,
	}
}

// Start begins tailing the log file and emitting events.
func (w *Watcher) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()

	// Emit AgentAdded event
	w.emit(events.AgentAdded{
		ID:      w.ID,
		Name:    w.ID,
		Kind:    "Claude",
		Model:   "external",
		LogPath: w.LogPath,
		Running: true,
	})

	tailCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.tailCancel = cancel
	w.mu.Unlock()

	w.tailWG.Add(1)
	go w.tailFile(tailCtx)
}

// Stop terminates the file tailing.
func (w *Watcher) Stop() {
	w.mu.Lock()
	cancel := w.tailCancel
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	w.tailWG.Wait()

	w.mu.Lock()
	w.running = false
	w.mu.Unlock()

	// Emit AgentRemoved event
	w.emit(events.AgentRemoved{ID: w.ID})
}

func (w *Watcher) emit(ev events.Event) {
	if w.events == nil {
		return
	}
	defer func() { _ = recover() }()
	switch ev.(type) {
	case events.AgentAdded:
		// Agent presence is critical; block rather than drop.
		w.events <- ev
	default:
		select {
		case w.events <- ev:
		default:
			// Drop if channel is full to keep agents flowing.
		}
	}
}

// scanLastProgress scans backwards through the log to find the last TodoWrite
// and emits a progress event for the sidebar without reading the full log.
func (w *Watcher) scanLastProgress() {
	f, err := os.Open(w.LogPath)
	if err != nil {
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return
	}

	// Scan in chunks from the end
	const chunkSize = 32 * 1024
	fileSize := info.Size()
	searchMarker := "[Tool: TodoWrite]"

	// Search backwards through file
	for offset := fileSize; offset > 0; {
		// Calculate chunk start
		start := offset - chunkSize
		if start < 0 {
			start = 0
		}
		size := offset - start

		buf := make([]byte, size)
		_, err := f.ReadAt(buf, start)
		if err != nil && err != io.EOF {
			return
		}

		content := string(buf)
		// Find last occurrence of TodoWrite in this chunk
		if idx := strings.LastIndex(content, searchMarker); idx >= 0 {
			// Find the end of this line
			lineEnd := strings.Index(content[idx:], "\n")
			if lineEnd < 0 {
				lineEnd = len(content) - idx
			}
			line := content[idx : idx+lineEnd]

			// Extract JSON and parse
			if jsonStart := strings.Index(line, "{"); jsonStart > 0 {
				jsonStr := strings.TrimSpace(line[jsonStart:])
				if _, progress := formatTodoWrite(jsonStr); progress != nil {
					w.emit(events.AgentProgress{
						ID:        w.ID,
						Completed: progress.Completed,
						Total:     progress.Total,
					})
				}
			}
			return
		}

		offset = start
		// If we've searched enough (e.g., 256KB), give up
		if fileSize-offset > 256*1024 {
			return
		}
	}
}

// tailFile streams log content to the UI, reusing the pattern from Agent.
func (w *Watcher) tailFile(ctx context.Context) {
	defer w.tailWG.Done()

	const tailBytes = 64 * 1024

	// On first run, scan backwards to find last TodoWrite for progress bar
	w.scanLastProgress()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		f, err := os.Open(w.LogPath)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		reader := bufio.NewReader(f)
		if info, _ := f.Stat(); info != nil && info.Size() > tailBytes {
			_, _ = f.Seek(-tailBytes, io.SeekEnd)
			reader = bufio.NewReader(f)
			_, _ = reader.ReadString('\n') // drop partial line
		}

		// Buffer for consecutive Say messages
		var sayBuffer []string

		flushSayBuffer := func() {
			if len(sayBuffer) > 0 {
				w.emit(events.AgentLine{ID: w.ID, Kind: events.MessageSay, Line: strings.Join(sayBuffer, "\n")})
				sayBuffer = nil
			}
		}

		for {
			select {
			case <-ctx.Done():
				flushSayBuffer()
				_ = f.Close()
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if line != "" {
				trimmed := strings.TrimRight(line, "\r\n")
				clean := agents.CleanLine(trimmed)
				if strings.TrimSpace(clean) == "" {
					continue
				}
				// Parse task log format: [Tool: Name] {json} or plain text
				parsed := parseTaskLogLine(clean)
				if parsed.Kind == events.MessageSay {
					// Buffer Say messages to combine them
					sayBuffer = append(sayBuffer, parsed.Text)
				} else {
					// Flush buffered Say messages before emitting Do/See
					flushSayBuffer()
					w.emit(events.AgentLine{ID: w.ID, Kind: parsed.Kind, Line: parsed.Text})
				}
				// Emit progress event for sidebar if this was a TodoWrite
				if parsed.Progress != nil {
					w.emit(events.AgentProgress{
						ID:        w.ID,
						Completed: parsed.Progress.Completed,
						Total:     parsed.Progress.Total,
					})
				}
			}
			if err == nil {
				continue
			}
			if err == io.EOF {
				// Flush any buffered Say messages when we hit EOF
				flushSayBuffer()
				// Wait for more data in same file.
				time.Sleep(50 * time.Millisecond)
				continue
			}
			break
		}
		_ = f.Close()
		// Re-open on next loop iteration to mimic tail -F.
		time.Sleep(100 * time.Millisecond)
	}
}

// parsedResult holds the result of parsing a task log line
type parsedResult struct {
	Kind      events.AgentMessageKind
	Text      string
	Progress  *progressInfo // non-nil if this was a TodoWrite
}

type progressInfo struct {
	Completed int
	Total     int
}

// parseTaskLogLine parses Claude task log format.
// [Tool: Name] {json} -> Do
// [Result: ...] -> See
// Regular text -> Say
func parseTaskLogLine(line string) parsedResult {
	if strings.HasPrefix(line, "[Tool:") {
		// Extract tool name and show a shorter summary
		if idx := strings.Index(line, "]"); idx > 0 {
			toolPart := line[1:idx] // "Tool: Name"
			rest := strings.TrimSpace(line[idx+1:])

			// Special handling for TodoWrite - format as markdown list
			if strings.Contains(toolPart, "TodoWrite") {
				if formatted, progress := formatTodoWrite(rest); formatted != "" {
					return parsedResult{
						Kind:     events.MessageSay,
						Text:     formatted,
						Progress: progress,
					}
				}
			}

			// Special handling for Edit - format as diff
			if strings.Contains(toolPart, "Edit") {
				if formatted := formatEdit(rest); formatted != "" {
					return parsedResult{Kind: events.MessageSay, Text: formatted}
				}
			}

			return parsedResult{Kind: events.MessageDo, Text: toolPart + " " + summarizeJSON(rest)}
		}
		return parsedResult{Kind: events.MessageDo, Text: line}
	}
	if strings.HasPrefix(line, "[Result") {
		return parsedResult{Kind: events.MessageSee, Text: line}
	}
	return parsedResult{Kind: events.MessageSay, Text: line}
}

// formatTodoWrite parses TodoWrite JSON and formats as markdown list
// Returns the formatted string and progress info for the sidebar
func formatTodoWrite(jsonStr string) (string, *progressInfo) {
	var payload todoPayload
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return "", nil
	}
	if len(payload.Todos) == 0 {
		return "", nil
	}

	// Count completed tasks
	completed := 0
	for _, todo := range payload.Todos {
		if todo.Status == "completed" {
			completed++
		}
	}
	total := len(payload.Todos)

	var sb strings.Builder

	// Progress bar
	const barWidth = 20
	filled := (completed * barWidth) / total
	sb.WriteString("[")
	for i := 0; i < barWidth; i++ {
		if i < filled {
			sb.WriteString("█")
		} else {
			sb.WriteString("░")
		}
	}
	sb.WriteString("] ")
	sb.WriteString(fmt.Sprintf("%d/%d\n\n", completed, total))

	// Task list
	for _, todo := range payload.Todos {
		var icon string
		switch todo.Status {
		case "completed":
			icon = "✓"
		case "in_progress":
			icon = ">"
		default:
			icon = " "
		}
		sb.WriteString("[")
		sb.WriteString(icon)
		sb.WriteString("] ")
		sb.WriteString(todo.Content)
		sb.WriteString("\n")
	}
	return sb.String(), &progressInfo{Completed: completed, Total: total}
}

// formatEdit parses Edit JSON and formats as a diff block
func formatEdit(jsonStr string) string {
	var payload editPayload
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return ""
	}
	if payload.FilePath == "" {
		return ""
	}

	var sb strings.Builder

	// Show file path (shortened if possible)
	filePath := payload.FilePath
	if idx := strings.LastIndex(filePath, "/"); idx > 0 {
		// Show last two path components
		if idx2 := strings.LastIndex(filePath[:idx], "/"); idx2 > 0 {
			filePath = "..." + filePath[idx2:]
		}
	}
	sb.WriteString("**Edit:** `")
	sb.WriteString(filePath)
	sb.WriteString("`\n")

	// Format as diff block
	sb.WriteString("```diff\n")

	// Old lines with - prefix
	oldLines := strings.Split(payload.OldString, "\n")
	for _, line := range oldLines {
		sb.WriteString("- ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// New lines with + prefix
	newLines := strings.Split(payload.NewString, "\n")
	for _, line := range newLines {
		sb.WriteString("+ ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	sb.WriteString("```\n")
	return sb.String()
}

// summarizeJSON extracts key fields from JSON for a short summary.
func summarizeJSON(s string) string {
	// For common tool patterns, extract the key field
	if strings.Contains(s, "file_path") {
		if start := strings.Index(s, `"file_path":"`); start > 0 {
			start += len(`"file_path":"`)
			if end := strings.Index(s[start:], `"`); end > 0 {
				return s[start : start+end]
			}
		}
	}
	if strings.Contains(s, "pattern") {
		if start := strings.Index(s, `"pattern":"`); start > 0 {
			start += len(`"pattern":"`)
			if end := strings.Index(s[start:], `"`); end > 0 {
				return s[start : start+end]
			}
		}
	}
	if strings.Contains(s, "command") {
		if start := strings.Index(s, `"command":"`); start > 0 {
			start += len(`"command":"`)
			if end := strings.Index(s[start:], `"`); end > 0 {
				cmd := s[start : start+end]
				if len(cmd) > 60 {
					cmd = cmd[:60] + "..."
				}
				return cmd
			}
		}
	}
	// Fallback: truncate if too long
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
