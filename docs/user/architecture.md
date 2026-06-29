# Kiến trúc yolo-code

yolo-code được thiết kế theo kiến trúc 12 layers với Event Bus làm backbone. Mỗi layer có trách nhiệm rõ ràng và chỉ phụ thuộc layer thấp hơn.

## 12 Layers

```
┌─────────────────────────────────────────┐
│  Interface                              │
│  ├─ TUI (L14) — bubbletea              │
│  └─ Headless — JSON events             │
├─────────────────────────────────────────┤
│  Coordination                           │
│  └─ Multi-Agent (L11) — DAG scheduler  │
├─────────────────────────────────────────┤
│  Cross-cutting                          │
│  └─ Infrastructure (L12) — otel, slog  │
├─────────────────────────────────────────┤
│  Memory                                │
│  └─ Memory System (L10) — vector store │
├─────────────────────────────────────────┤
│  Action                                │
│  ├─ Execution (L7) — tools, sandbox   │
│  ├─ Patch (L9) — SEARCH/REPLACE       │
│  └─ Verification (L8) — AST, lint, test│
├─────────────────────────────────────────┤
│  Cognition                             │
│  ├─ Cognitive Core (L6) — planner     │
│  ├─ Prompt Compiler (L5) — compose    │
│  └─ Context Engine (L4) — relevance   │
├─────────────────────────────────────────┤
│  Foundation                            │
│  ├─ Event Bus (L3) — pub/sub          │
│  ├─ Runtime FSM (L2) — 12 states      │
│  └─ Session (L1) — lifecycle          │
└─────────────────────────────────────────┘
```

## Foundation (L1–L3)

### L1 — Session Manager
- Quản lý lifecycle: tạo, checkpoint, resume, cancel
- Undo stack cho rollback
- Context-based cancellation

### L2 — Runtime FSM
- 12 states, 20 transitions
- Chạy trên **1 goroutine duy nhất** (single-goroutine drive loop)
- State transitions emit `state.change` events

States chính:
```
IDLE → PLAN → THINK → EXEC → WAIT_TOOL → VERIFY → (HasMore?) → PLAN → ... → DONE
```

### L3 — Event Bus
- Backbone của toàn bộ hệ thống
- 16 topic groups
- Fsync-before-fanout (durability)
- Per-subscriber FIFO
- At-least-once delivery + idempotent handlers

## Cognition (L4–L6)

### L4 — Context Engine
- 7 inputs (files, conversation, tool results, memory, preferences, v.v.)
- Relevance scoring: recency + proximity + semantic + centrality + explicit
- Compression passes khi context vượt budget

### L5 — Prompt Compiler
- Pipeline: dedup → summarize → budget → order
- Output: XML + Markdown wire format
- Gửi đến Cognitive Core

### L6 — Cognitive Core
- Giao tiếp với LLM provider (OpenAI-compatible API)
- Multi-turn conversation: Think() → tool_calls → RecordToolResult() → Think() lại
- Planner + Reflection + Reasoner
- Tool Policy + Verify Policy + Cost Controller

## Action (L7–L9)

### L7 — Execution Engine
- Tool Registry: 4 tools tích hợp (list_files, read_file, edit_file, bash)
- Dispatcher + worker goroutines
- Sandbox: path confinement, command allowlist, network default-deny
- HITL approval flow (risk-based)
- Observation normalizer

### L9 — Patch Engine
- Hybrid SEARCH/REPLACE primary + unified diff fallback
- Conflict detection
- AST validation
- Git checkpoint + rollback

### L8 — Verification Engine
- Pipeline: AST → Format → Lint → TypeCheck → Build → Tests → PolicyCheck
- Verdicts: pass / warn / fail
- Fail → rollback tự động

## Memory (L10)

- 6 types: Working, Conversation, Exec, Repository, Knowledge, Preference
- Updates CHỈ qua events (event-driven)
- Pure-Go vector store
- Per-function chunking

## Coordination (L11)

- Multi-agent: Orchestrator, Planner, Coder, Reviewer, Tester, Researcher
- DAG scheduler cho task phức tạp
- Rework cap + merge + re-verify
- Shared cost budget

## Infrastructure (L12)

- OpenTelemetry traces (span-per-event)
- Structured logging (slog)
- Sentry opt-in (fail-silent)
- Secrets redaction (3 boundaries: exec, log, sentry)
- Rate limiting (token-bucket)
- Cost ledger

## Import Matrix

Layer cao hơn chỉ được import layer thấp hơn. Không bao giờ ngược.

```
L11 → L6, L7, L8, L9, L10
L6  → L3, L4, L5
L7  → L3
L8  → L7
L9  → L7
L10 → L3
L3  → (không phụ thuộc layer khác)
L2  → L1, L3
L1  → L3
TUI → L3 (subscribe-only)
```

## Data Flow

```
1. User gõ task (stdin hoặc TUI)
2. Session Manager tạo task
3. Context Engine thu thập context
4. Prompt Compiler compose prompt
5. Cognitive Core gọi LLM (Think)
6. LLM trả về tool_calls hoặc final answer
7. Nếu tool_calls:
   a. Dispatcher gửi đến Execution Engine
   b. Sandbox kiểm tra safety
   c. HITL approval (nếu cần)
   d. Tool thực thi → Observation
   e. RecordToolResult → thêm vào history
   f. Verification (nếu auto)
   g. Loop lại bước 5 (HasMore = true)
8. Nếu final answer:
   a. Task completed
   b. Memory updated via events
```

## Xem thêm

- [Configuration](configuration.md) — cấu hình các layers
- [Tools Reference](tools.md) — chi tiết 4 tools
- [RAG & Memory](../rag/) — Context Engine và Memory System sâu hơn
- Design docs `00-Mindmap.md` → `15-Implementation_Roadmap.md` — technical deep dive
