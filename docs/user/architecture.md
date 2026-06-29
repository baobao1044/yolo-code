# yolo-code Architecture

yolo-code is designed with a 12-layer architecture using an Event Bus as the backbone. Each layer has clear responsibilities and depends only on lower layers.

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
- Lifecycle management: create, checkpoint, resume, cancel
- Undo stack for rollback
- Context-based cancellation

### L2 — Runtime FSM
- 12 states, 20 transitions
- Runs on **exactly 1 goroutine** (single-goroutine drive loop)
- State transitions emit `state.change` events

Main states:
```
IDLE → PLAN → THINK → EXEC → WAIT_TOOL → VERIFY → (HasMore?) → PLAN → ... → DONE
```

### L3 — Event Bus
- Backbone of the entire system
- 16 topic groups
- Fsync-before-fanout (durability)
- Per-subscriber FIFO
- At-least-once delivery + idempotent handlers

## Cognition (L4–L6)

### L4 — Context Engine
- 7 inputs (files, conversation, tool results, memory, preferences, etc.)
- Relevance scoring: recency + proximity + semantic + centrality + explicit
- Compression passes when context exceeds budget

### L5 — Prompt Compiler
- Pipeline: dedup → summarize → budget → order
- Output: XML + Markdown wire format
- Sent to the Cognitive Core

### L6 — Cognitive Core
- Communicates with LLM provider (OpenAI-compatible API)
- Multi-turn conversation: Think() → tool_calls → RecordToolResult() → Think() again
- Planner + Reflection + Reasoner
- Tool Policy + Verify Policy + Cost Controller

## Action (L7–L9)

### L7 — Execution Engine
- Tool Registry: 4 built-in tools (list_files, read_file, edit_file, bash)
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
- Fail → automatic rollback

## Memory (L10)

- 6 types: Working, Conversation, Exec, Repository, Knowledge, Preference
- Updates ONLY via events (event-driven)
- Pure-Go vector store
- Per-function chunking

## Coordination (L11)

- Multi-agent: Orchestrator, Planner, Coder, Reviewer, Tester, Researcher
- DAG scheduler for complex tasks
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

Higher layers may only import lower layers. Never the other way around.

```
L11 → L6, L7, L8, L9, L10
L6  → L3, L4, L5
L7  → L3
L8  → L7
L9  → L7
L10 → L3
L3  → (no layer dependencies)
L2  → L1, L3
L1  → L3
TUI → L3 (subscribe-only)
```

## Data Flow

```
1. User types task (stdin or TUI)
2. Session Manager creates task
3. Context Engine gathers context
4. Prompt Compiler composes prompt
5. Cognitive Core calls LLM (Think)
6. LLM returns tool_calls or final answer
7. If tool_calls:
   a. Dispatcher sends to Execution Engine
   b. Sandbox checks safety
   c. HITL approval (if needed)
   d. Tool executes → Observation
   e. RecordToolResult → added to history
   f. Verification (if auto)
   g. Loop back to step 5 (HasMore = true)
8. If final answer:
   a. Task completed
   b. Memory updated via events
```

## See also

- [Configuration](configuration.md) — configuring the layers
- [Tools Reference](tools.md) — details on the 4 tools
- [RAG & Memory](../rag/) — Context Engine and Memory System in depth
- Design docs `00-Mindmap.md` → `15-Implementation_Roadmap.md` — technical deep dive
