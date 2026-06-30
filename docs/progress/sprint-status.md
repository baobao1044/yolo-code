# Sprint Status

Detailed progress by sprint, updated per `15-Implementation_Roadmap.md`.

## Legend

- ✅ **Completed** — All deliverables done
- 🔵 **In Progress** — Under development
- 🟡 **Partial** — Partially completed
- ⬜ **Planned** — Not started

## Foundation

| Sprint | Name | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S1 | Cold Start | ✅ | Session lifecycle, basic CLI, go build | Foundation |
| S2 | Token-to-Screen | ✅ | TUI skeleton (bubbletea), event rendering | Basic TUI |
| S3 | Event Backbone | ✅ | Event Bus (16 topics), fsync-before-fanout, FIFO | Backbone done |

## Cognition

| Sprint | Name | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S4 | Context Assembly | ✅ | Context Engine (7 inputs), relevance scoring | 5 signals |
| S5 | Prompt Pipeline | ✅ | Prompt Compiler, dedup→summarize→budget→order | Golden tests |
| S6 | First Thought | ✅ | Cognitive Core, OpenAI-compatible provider, Think() | LLM connected |

## Action

| Sprint | Name | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S7 | Tool Stack | ✅ | 4 tools (list_files, read_file, edit_file, bash), Dispatcher | Tool registry |
| S8 | Patch & Rollback | ✅ | Patch Engine, SEARCH/REPLACE, git checkpoint | Safe rollback |
| S9 | Trust but Verify | ✅ | Verification Engine (AST→Format→Lint→Build→Test), fail→rollback | 7-stage pipeline |

## Interface + Hardening

| Sprint | Name | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S10 | TUI Polish | ✅ | Full TUI: board, cost meter, diff viewer, status bar | Interactive mode |
| S11 | Sandbox Hardening | ✅ | HITL approval, risk classification, wrapper peeling, red-team suite | Safety |

## Integration + Superpowers

| Sprint | Name | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S12 | Multi-Agent Integration | 🔵 | Coordination layer, DAG scheduler, orchestrator/coder/reviewer | Integrating |
| S13 | Superpowers | 🟡 | RAG, vector store, memory lifecycle, knowledge accumulation | Partial |
| S14 | Scope Loop & Dynamic Workflow | ✅ | `internal/scope` (controller, W2/W3, MCTS) + `internal/workflow` (bugfix/feature/refactor), runtime ports, adapters, multi-candidate reflection | Landed |

## Sprint Details

### S1 — Cold Start
- Session Manager: create, checkpoint, resume, cancel
- Basic CLI: `yolo` binary, stdin input
- Build pipeline: `go build ./...`, `go test ./...`

### S2 — Token-to-Screen
- TUI skeleton: bubbletea Program, basic rendering
- Event→render mapping: state changes displayed on board
- Performance budget: S1 cold-start < 100ms, S2 token-to-screen < 50ms

### S3 — Event Backbone
- Event Bus: 16 topic groups, Envelope struct
- Durability: fsync-before-fanout
- Per-subscriber FIFO, at-least-once + idempotent

### S4 — Context Assembly
- Context Engine: 7 input sources
- Relevance scoring: recency, proximity, semantic, centrality, explicit
- Compression passes when exceeding budget

### S5 — Prompt Pipeline
- Prompt Compiler: dedup → summarize → budget → order
- Wire format: XML + Markdown
- Golden tests: deterministic output for same input

### S6 — First Thought
- Cognitive Core: `Think()`, `HasMore()`, `RecordToolResult()`
- OpenAI-compatible provider with SSE streaming
- Multi-turn agent loop: Think → tool_call → Execute → RecordToolResult → Think again

### S7 — Tool Stack
- 4 tools: `list_files`, `read_file`, `edit_file`, `bash`
- Tool Registry + Dispatcher
- Sandbox: path confinement, command classification
- Native function calling API (OpenAI tools[])

### S8 — Patch & Rollback
- Patch Engine: SEARCH/REPLACE primary + unified diff fallback
- Git checkpoint before each edit
- Rollback mechanism on verify failure

### S9 — Trust but Verify
- 7-stage verification: AST → Format → Lint → TypeCheck → Build → Test → PolicyCheck
- Verdicts: pass / warn / fail
- Fail → auto rollback

### S10 — TUI Polish
- Full TUI: board, cost meter, diff viewer, status bar
- Interactive input prompt
- Headless mode (JSON events)

### S11 — Sandbox Hardening
- HITL approval gate: risk classification (low/medium/high/critical)
- Wrapper peeling: sudo, env, time
- Red-team test suite: path escapes, shell escapes, network commands
- Auto-approve config for headless mode

### S12 — Multi-Agent Integration
- Coordination Layer: Orchestrator, Planner, Coder, Reviewer, Tester, Researcher
- DAG scheduler for sub-task parallelism
- Rework cap + merge + re-verify
- Shared cost budget

### S13 — Superpowers
- Pure-Go vector store
- Per-function chunking + embedding
- RAG retrieval flow
- 6 memory types with event-driven lifecycle
- Knowledge accumulation cross-session

### S14 — Scope Loop & Dynamic Workflow
- `internal/scope`: scope-level state machine (Task/Repo/File/Function/Edit/Verify), W2 tool-gating, W3 expand/contract on verify feedback, anti-loop Memory, budget-bounded Scope MCTS
- `internal/workflow`: per-task workflow selection (bugfix/feature/refactor), heuristic Classifier, conditional branching (multi-hypothesis, repair loop, scope contract, model degrade)
- Runtime ports `ScopeController` + `WorkflowEngine`, noop stubs, drive-loop integration (VERIFY scope consult, PLAN workflow consult, EXECUTE scope-gated tool broadening)
- Multi-candidate reflection (`ReflectMulti`, `RerankCandidates`) + `ReflectionMemory` + cost-degrade ladder (`MultiCandidateAllowed`)
- Production blockers fixed: module path, env vars, `.env` auto-load, CLI flags, grep tool schema, missing `EXECUTE → PLAN` FSM edge

## See also

- `15-Implementation_Roadmap.md` — Full roadmap technical spec
- [Changelog](changelog.md) — Change history
- [Development Workflow](../workflow/development.md) — Sprint cadence and testing
