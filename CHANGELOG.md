# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- OpenAI-compatible provider (`OpenAICompatProvider`) with SSE streaming
- 4 built-in tools: `list_files`, `read_file`, `edit_file`, `bash`
- Multi-turn agent loop: Think → Tool Call → Execute → Verify → Think again
- HITL (Human-in-the-Loop) approval gate with risk classification
- Safe sandbox: path confinement, wrapper peeling, shell escape detection, network default-deny
- Interactive TUI mode (bubbletea + lipgloss)
- Headless mode (JSON events for CI/scripts)
- Event Bus backbone with 16 topic groups
- Single-goroutine Runtime FSM (12 states, 20 transitions)
- Context Engine with relevance scoring (recency, proximity, semantic, centrality, explicit)
- Prompt Compiler: dedup → summarize → budget → order
- Pure-Go vector store for memory system
- Multi-agent coordination layer (DAG scheduler)
- OpenTelemetry traces + structured logging (slog)
- Cross-compile matrix: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
- CI pipeline: lint → build → test → race → golden → snapshot → docs
- GoReleaser release dry-run pipeline

### Changed

- Tool `read` renamed to `read_file`, arg `path` → `file`
- Tool `bash` arg `cmd` → `command`
- Headless mode: medium/high risk tools need AutoApprove config to avoid deadlock
- Conversation history accumulation: `Think()` retains history across turns

### Fixed

- `parseSSE()` did not accumulate partial tool_calls → fixed with `partials map[int]*partialCall`
- `HasMore()` returned `false` after tool call → fixed to return `!lastTurn.Final`
- Duplicate prompt messages each turn → fixed to init history only once
- Headless deadlock when HITL gate waits for approval → added AutoApprove config
