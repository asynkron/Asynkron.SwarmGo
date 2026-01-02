# Asynkron.SwarmGo

Go rewrite of **Asynkron.Swarm** using the Charm stack (Bubble Tea + Lip Gloss) for the terminal UI. It orchestrates multiple AI coding agents on separate git worktrees and streams their logs side‑by‑side.

## Features
- Creates per‑worker git worktrees and launches Claude/Codex/Copilot/Gemini CLI agents with the original swarm prompts.
- Charm‑based TUI: left panel for agents, right panel for live logs; shows status, phase, and countdown.
- Supervisor agent monitors worker logs; autopilot mode adds PR/branch instructions to worker prompts.
- Lightweight agent detection (`--detect`) and required agent validation before running.

## Requirements
- Go 1.22+
- Git
- At least one supported AI CLI installed in `$PATH`: `claude`, `codex`, `copilot`, or `gemini`.

## Usage
```bash
# From this repo (ensure your repo has a todo.md)
go run ./cmd/swarm --todo task.md

# Explicit repo and worker mix
go run ./cmd/swarm --repo ~/projects/my-app --todo task.md --claude 2 --codex 1 --copilot 1 --supervisor claude

# Detect installed agents only
go run ./cmd/swarm --detect
```

### Common flags
- `--repo` path to git repo (defaults to current repo)
- `--todo` relative path to todo file (default: `todo.md`)
- `--claude|--codex|--copilot|--gemini` worker counts (defaults to 2 Claude if none set)
- `--supervisor` supervisor agent type (`claude|codex|copilot|gemini`)
- `--minutes` time limit for a round (default: 15)
- `--autopilot` include PR/branch instructions in worker prompts (default: true)
- `--arena` reserved for multi‑round mode (placeholder in this Go port)
- `--skip-detect` skip required-agent check

### TUI controls
- `↑/↓` select item
- `PgUp/PgDn` scroll log
- `q` quit

## Notes and differences from the .NET version
- Resume and full arena orchestration are not yet implemented.
- Agent detection is lightweight (PATH + `--version`); no prompt test is executed.
- Worktrees and session data live under your system temp directory (`/tmp/swarmgo/<session>`).
