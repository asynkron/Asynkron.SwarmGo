package detector

import (
	"bytes"
	"os/exec"
	"strings"

	"github.com/asynkron/Asynkron.SwarmGo/internal/config"
)

// Status represents detection info for a single agent CLI.
type Status struct {
	Type       config.AgentType
	Executable string
	Installed  bool
	Version    string
	Error      string
}

// DetectAll checks every supported agent type with a lightweight PATH lookup and optional version probe.
func DetectAll() []Status {
	types := []config.AgentType{
		config.AgentClaude,
		config.AgentCodex,
		config.AgentCopilot,
		config.AgentGemini,
	}

	results := make([]Status, 0, len(types))
	for _, t := range types {
		results = append(results, detect(t))
	}
	return results
}

func detect(t config.AgentType) Status {
	exe := string(t)
	path, err := exec.LookPath(exe)
	if err != nil {
		return Status{Type: t, Executable: exe, Installed: false, Error: "not found in PATH"}
	}

	version, vErr := getVersion(exe)
	errMsg := ""
	if vErr != nil {
		errMsg = vErr.Error()
	}

	return Status{
		Type:       t,
		Executable: path,
		Installed:  true,
		Version:    version,
		Error:      errMsg,
	}
}

func getVersion(exe string) (string, error) {
	cmd := exec.Command(exe, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}

	lines := strings.Split(out.String(), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0]), nil
	}
	return strings.TrimSpace(out.String()), nil
}
