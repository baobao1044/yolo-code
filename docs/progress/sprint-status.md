# Sprint Status

Tiến trình chi tiết theo từng sprint, cập nhật theo `15-Implementation_Roodmap.md`.

## Legend

- ✅ **Completed** — Tất cả deliverables hoàn thành
- 🔵 **In Progress** — Đang phát triển
- 🟡 **Partial** — Một phần hoàn thành
- ⬜ **Planned** — Chưa bắt đầu

## Foundation

| Sprint | Tên | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S1 | Cold Start | ✅ | Session lifecycle, basic CLI, go build | Nền tảng |
| S2 | Token-to-Screen | ✅ | TUI skeleton (bubbletea), event rendering | TUI cơ bản |
| S3 | Event Backbone | ✅ | Event Bus (16 topics), fsync-before-fanout, FIFO | Backbone xong |

## Cognition

| Sprint | Tên | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S4 | Context Assembly | ✅ | Context Engine (7 inputs), relevance scoring | 5 signals |
| S5 | Prompt Pipeline | ✅ | Prompt Compiler, dedup→summarize→budget→order | Golden tests |
| S6 | First Thought | ✅ | Cognitive Core, OpenAI-compatible provider, Think() | LLM kết nối |

## Action

| Sprint | Tên | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S7 | Tool Stack | ✅ | 4 tools (list_files, read_file, edit_file, bash), Dispatcher | Tool registry |
| S8 | Patch & Rollback | ✅ | Patch Engine, SEARCH/REPLACE, git checkpoint | Rollback an toàn |
| S9 | Trust but Verify | ✅ | Verification Engine (AST→Format→Lint→Build→Test), fail→rollback | 7-stage pipeline |

## Interface + Hardening

| Sprint | Tên | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S10 | TUI Polish | ✅ | Full TUI: board, cost meter, diff viewer, status bar | Interactive mode |
| S11 | Sandbox Hardening | ✅ | HITL approval, risk classification, wrapper peeling, red-team suite | An toàn |

## Integration + Superpowers

| Sprint | Tên | Status | Key Deliverables | Notes |
|---|---|---|---|---|
| S12 | Multi-Agent Integration | 🔵 | Coordination layer, DAG scheduler, orchestrator/coder/reviewer | Đang tích hợp |
| S13 | Superpowers | 🟡 | RAG, vector store, memory lifecycle, knowledge accumulation | Một phần |

## Chi tiết Sprint

### S1 — Cold Start
- Session Manager: create, checkpoint, resume, cancel
- Basic CLI: `yolo` binary, stdin input
- Build pipeline: `go build ./...`, `go test ./...`

### S2 — Token-to-Screen
- TUI skeleton: bubbletea Program, basic rendering
- Event→render mapping: state changes hiển thị trên board
- Performance budget: S1 cold-start < 100ms, S2 token-to-screen < 50ms

### S3 — Event Backbone
- Event Bus: 16 topic groups, Envelope struct
- Durability: fsync-before-fanout
- Per-subscriber FIFO, at-least-once + idempotent

### S4 — Context Assembly
- Context Engine: 7 input sources
- Relevance scoring: recency, proximity, semantic, centrality, explicit
- Compression passes khi vượt budget

### S5 — Prompt Pipeline
- Prompt Compiler: dedup → summarize → budget → order
- Wire format: XML + Markdown
- Golden tests: deterministic output cho cùng input

### S6 — First Thought
- Cognitive Core: `Think()`, `HasMore()`, `RecordToolResult()`
- OpenAI-compatible provider với SSE streaming
- Multi-turn agent loop: Think → tool_call → Execute → RecordToolResult → Think lại

### S7 — Tool Stack
- 4 tools: `list_files`, `read_file`, `edit_file`, `bash`
- Tool Registry + Dispatcher
- Sandbox: path confinement, command classification
- Native function calling API (OpenAI tools[])

### S8 — Patch & Rollback
- Patch Engine: SEARCH/REPLACE primary + unified diff fallback
- Git checkpoint trước mỗi edit
- Rollback mechanism khi verify fail

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
- Auto-approve config cho headless mode

### S12 — Multi-Agent Integration
- Coordination Layer: Orchestrator, Planner, Coder, Reviewer, Tester, Researcher
- DAG scheduler cho sub-task parallelism
- Rework cap + merge + re-verify
- Shared cost budget

### S13 — Superpowers
- Pure-Go vector store
- Per-function chunking + embedding
- RAG retrieval flow
- 6 memory types với event-driven lifecycle
- Knowledge accumulation cross-session

## Xem thêm

- `15-Implementation_Roadmap.md` — Full roadmap technical spec
- [Changelog](changelog.md) — Lịch sử thay đổi
- [Development Workflow](../workflow/development.md) — Sprint cadence và testing
