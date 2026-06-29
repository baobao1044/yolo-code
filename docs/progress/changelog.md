# Progress Changelog

Important changes to yolo-code, updated over time.

## 2026-06-28

### Documentation overhaul
- Created root README.md with badges, features, quickstart, architecture
- Created CONTRIBUTING.md, CHANGELOG.md, LICENSE, .env.example
- Expanded docs/user/: added architecture.md, configuration.md, tools.md, tui-guide.md
- Created docs/workflow/: ci-cd.md, development.md
- Created docs/rag/: context-engine.md, vector-store.md, memory-lifecycle.md
- Created docs/progress/: sprint-status.md, changelog.md

### Multi-turn agent loop
- Fixed `HasMore()` to return `!lastTurn.Final` → agent loop continues after tool execution
- Added `RecordToolResult(toolName, result)` → conversation history accumulation
- Fixed duplicate prompt messages: only init history on first Think()

## 2026-06-27

### Native tool calling API
- Added `tools[]` definitions in OpenAI chat request
- Model emits `delta.tool_calls` instead of inline tokens
- Rewrote `parseSSE()` with partial tool_calls accumulation (by index)
- Flush on `finish_reason: "tool_calls"` or `[DONE]`

### 4 Built-in tools
- `list_files` — list repo files (Low risk)
- `read_file` — read file contents (Low risk)
- `edit_file` — overwrite file (High risk)
- `bash` — run shell command (Medium–Critical risk)

## 2026-06-26

### OpenAI-compatible provider
- Created `OpenAICompatProvider` with SSE streaming
- Parses SSE `data: {json}\n\n` format with `[DONE]` terminator

### HITL approval gate
- Risk classification: low/medium/high/critical
- Interactive mode: TUI prompt for approval
- Headless mode: AutoApprove config (YOLO_AUTO_APPROVE_MEDIUM/HIGH)
- Critical risk: always rejected

## 2026-06-25

### Sandbox hardening
- Wrapper peeling: sudo → peel → classify underlying command
- Path escape detection: `../../etc/passwd` → `ErrPathEscapes`
- Shell escape classification: `eval`, `source`, `$(cmd)` → `RiskCritical`
- Network command classification: `curl`, `wget`, `ssh` → `RiskHigh`
- Red-team test suite: sandbox_redteam_test.go

## 2026-06-20

### Verification Engine
- 7-stage pipeline: AST → Format → Lint → TypeCheck → Build → Test → PolicyCheck
- Fail → auto rollback
- Verdicts: pass / warn / fail

## 2026-06-15

### Patch Engine
- SEARCH/REPLACE primary + unified diff fallback
- Conflict detection
- Git checkpoint before each edit

## 2026-06-10

### Event Bus
- 16 topic groups
- Fsync-before-fanout
- Per-subscriber FIFO
- At-least-once + idempotent delivery

## 2026-06-05

### Runtime FSM
- 12 states, 20 transitions
- Single-goroutine drive loop
- Context-based cancellation

## 2026-06-01

### Project kickoff
- Project initialization: Go 1.26, bubbletea TUI
- Session Manager: lifecycle, checkpoints, undo stack
- Basic CLI: `yolo` binary, `--headless` flag

---

> For detailed technical changelog, see [CHANGELOG.md](../../CHANGELOG.md) at root.
