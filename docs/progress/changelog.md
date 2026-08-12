# Progress Changelog

Important changes to yolo-code, updated over time.

## 2026-08-12

### S13 Superpowers — RAG & Memory wired into the agent path
- Wired the memory Store into all three composition-root sites (headless `defaultHeadlessDeps`, coord `buildRuntimeDeps`, TUI via `defaultHeadlessDeps`): `memory.Open` with a sandbox-confined `memory.FS`, `contextMemoryAdapter` behind the Context Engine's `Memory` port, and cold-start `IndexRepo`. Production no longer runs with `noopMemory`.
- Context Engine: added `KindRAG` + `RAG` group, `Memory.Retrieve` port method, gather RAG after Preferences, rank keeps cosine score for RAG parts, budget slot (8% off Files), compression assigns RAG.
- Prompt Compiler: new `<rag>` wire tag (after `<files>`, before conversation), budget trimming for RAG.
- Cold-start indexing (`internal/memory/index.go`): `IndexRepo` walks the repo (deterministic order), skips `.git`/`vendor`/`node_modules`/`__pycache__`/`dist`/`.cache`/`build`/dotfiles/oversized files, chunks each source file, `BulkInsert` in one locked pass. Reindex on `patch.applied` reads the new content via the wired FS.
- Memory lifecycle complete:
  - Working memory: `task`/`state` fields, `SetTask`/`SetState`/`Clear` (§11.3.1).
  - Exec history: rolling window (last 50), monotonic per-task seq counter.
  - SemanticStore: `Delete(id)`, `Size()`, `Evict(capacity)` (LRU by lastAccess), `SetThreshold(θ)`, `lastAccess` bump on Retrieve.
  - Knowledge insight store (`internal/memory/knowledge.go`): distinct from code-chunk RAG, fed by `verify.fail`/`verify.pass`/`task.completed`, dedup, cross-session JSON persistence.
  - Listener widened to 9 topics (`task.started`/`state.change`/`verification.failed`/`verification.stage`/`user.preference` added); slow I/O (Persist/Reindex) offloaded to background goroutines tracked by `Store.bg` so `Close` waits.
  - `Store.Open` eager-loads Knowledge + Preferences (cross-session recall).
- New `user.preference` event (`internal/event/events.go`) routes agent-originated preferences through the listener (§11.5.2).
- Determinism preserved (S5): `memory.update` telemetry is excluded from the headless transcript projection (the listener goroutine's interleaving is non-deterministic); transcript seq is normalized to the event's position. Golden transcript hash unchanged. The TUI still sees `memory.update` via its own subscriber.
- Fixed pre-existing test races surfaced by `-race` (`recordingSub` happens-before in `TestHeuristicPlannerDrivesOrchestrator`, `allCh` drain in `TestRuntimeAgentRunnerSpawnsCoder`).
- Tests added: `knowledge_test.go`, `index_test.go`, `lifecycle_test.go`, RAG extensions in `semantic_test.go`, `engine_test.go` (gather RAG), `pipeline_test.go` (`<rag>` tag), listener extensions in `listener_test.go`.
- Notes: each coord per-todo runtime gets its own memory store (temp dir); Knowledge isn't shared across roles — a documented S12 limitation, not S13 scope. Hash embedder kept (air-gapped); a real OpenAI/Ollama embedder plugs behind the `Embedder` interface in a later sprint. HNSW deferred (brute-force cosine sufficient for small corpora).

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
