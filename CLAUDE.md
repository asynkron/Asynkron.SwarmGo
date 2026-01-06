# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
# Build
go build ./...

# Test
go test ./...

# Run main swarm orchestrator
go run ./cmd/swarm --todo task.md

# Run quick single-agent CLI (read-only by default)
go run ./cmd/quick "your question here"
go run ./cmd/quick --agent claude --model haiku "inspect a file"
go run ./cmd/quick --yolo "write mode enabled"

# Detect installed agents
go run ./cmd/swarm --detect
```

## Architecture Overview

SwarmGo is a terminal UI application that orchestrates multiple AI coding agents (Claude, Codex, Copilot, Gemini) on separate git worktrees. Built with the Charm stack (Bubble Tea + Lip Gloss).

### Key Components

- **cmd/swarm/** - Main orchestrator CLI entry point; handles flags, session creation, orchestrator setup
- **cmd/quick/** - Quick single-agent CLI for one-off queries with safety guards
- **agentrunner/** - Agent CLI abstraction layer; defines how to invoke each agent type (claude, codex, copilot, gemini) and parse their output
- **internal/orchestrator/** - Core coordination logic; manages workers, supervisor, status collection, agent lifecycle
- **internal/ui/** - Bubble Tea TUI model; renders agents panel (left), logs panel (right), status, countdown
- **internal/agents/** - Agent wrappers for execution and log tailing
- **internal/supervisor/** - CodedSupervisor monitors worker git state and logs
- **internal/prompts/** - Dynamic prompt generation for workers, supervisor, prep agent, user commands
- **internal/status/** - Lightweight collector polling git state and log signals (~5s intervals)
- **internal/session/** - Session persistence in `/tmp/swarmgo/<SESSION_ID>/`
- **internal/config/** - Options struct and validation; agent type definitions
- **internal/events/** - Event types for orchestrator→UI communication
- **internal/control/** - Command types for UI→orchestrator communication

### Event-Driven Architecture

- **events.Event interface**: Polymorphic events from orchestrator to UI (AgentAdded, AgentRemoved, AgentLine, StatusMessage, PhaseChanged, etc.)
- **control.Command types**: UI commands to orchestrator (RestartAgent, StopAgent, StartAgent, StartUserCommand)
- Events flow through buffered channels (512 capacity)

### Agent Abstraction

`agentrunner.CLI` interface defines each agent type:
- `Cmd()` returns the CLI command and args
- `ParseOutput()` categorizes agent output into Say/Do/See messages
- Model assignment uses index-based rotation across available models

### Session & Worktree Management

- Sessions stored in system temp: `/tmp/swarmgo/<SESSION_ID>/`
- Per-worker worktrees: `wt1`, `wt2`, ... plus `prep` for prep agent
- Resumable via `--resume <SESSION_ID>`

### TUI Controls

- `↑/↓` select agent
- `PgUp/PgDn` scroll logs
- `Space` toggle agent stop/start
- `q` quit
