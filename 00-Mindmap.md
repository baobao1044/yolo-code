# 00 — Mindmap

> Bản đồ tư duy toàn cảnh kiến trúc **yolo-code** — agent viết code đa agent
> trên terminal bằng Go. Mỗi nhánh là một layer (L1–L12) hoặc một mặt cắt
> ngang (TUI / Nguyên tắc / Docs). Số trong ngoặc trỏ tới file tài liệu tương
> ứng.

```mermaid
mindmap
  root((yolo-code))
    Foundation
      L3 Event Bus[05]
        Bus + Envelope
        16 topic groups
        durability fsync-before-fanout
        per-subscriber FIFO
        at-least-once + idempotent
      L1 Session Manager[03]
        Session / Task lifecycle
        checkpoints
        undo stack
        resume
        cancel vs pause
      L2 Runtime FSM[04]
        12 states
        20 transitions
        single runtime goroutine
        context-based cancel
        state.change events
    Cognition
      L4 Context Engine[06]
        7 inputs
        relevance scoring
          recency proximity semantic centrality explicit
        compression passes
      L5 Prompt Compiler[06]
        dedup → summarize → budget → order
        XML + Markdown wire format
      L6 Cognitive Core[07]
        Planner
        Reflection non-acting
        Reasoner
        Tool Policy
        Verify Policy
        Cost Controller
          degradation ladder
    Action
      L7 Execution Engine[08]
        Tool Registry static Go + MCP
        Dispatcher + worker goroutines
        Sandbox
          path confinement
          command allowlist
          network default-deny
          resource limits
        HITL approval flow
        Observation normalizer
        process-group kill
      L9 Patch Engine[10]
        Hybrid SEARCH/REPLACE primary
        unified diff fallback
        conflict detection
        AST validation
        git checkpoint + rollback
      L8 Verification Engine[09]
        AST → Format → Lint → TypeCheck → Build → Tests → PolicyCheck
        verdicts pass / warn / fail
        fail → rollback
    Memory
      L10 Memory System[11]
        6 types
          Working Conversation Exec Repository Knowledge Preference
        update ONLY via events
        pure-Go vector store
        per-function chunking
    Coordination
      L11 Multi-Agent[12]
        Orchestrator
        Planner
        Coder
        Reviewer
        Tester
        Researcher
        DAG scheduler
        rework cap
        merge + re-verify
        shared cost budget
    Cross-cutting
      L12 Infrastructure[13]
        OpenTelemetry traces
          span-per-event
          task root span
          otel.export drain
        Metrics unsampled
        slog structured logs
        Sentry opt-in fail-silent
        Secrets redaction
          3 boundaries exec / log / sentry
        Permissions
          yolo auto ask read-only
        Rate Limit token-bucket
        Cost Ledger
    Interface
      TUI[14]
        bubbletea + lipgloss + bubbles
        subscribe-only
        no logic
        busWatcher bridge
        never-block rule
        event→render mapping
        headless mode
    Nguyên tắc
      P1 Speed
      P2 Safety
      P3 Determinism
      P4 Transparency
      P5 Bounded Cost
```

## Chú thích

| Ký hiệu | Ý nghĩa |
|---|---|
| `root((yolo-code))` | Toàn bộ hệ thống |
| `Foundation` | Xương sống: bus, session, FSM — mọi thứ dựa vào đây |
| `Cognition` | Lớp "suy nghĩ": context, prompt, planner/reflection/reasoner |
| `Action` | Lớp "hành động": chạy tool, vá file, kiểm chứng |
| `Memory` | Trí nhớ dài hạn, chỉ cập nhật qua events |
| `Coordination` | Đa agent cho task phức tạp |
| `Cross-cutting` | Quan sát + an toàn + chi phí, bọc toàn agent, không đụng logic |
| `Interface` | TUI subscribe-only (và headless dùng chung contract) |
| `Nguyên tắc` | P1–P5, thứ tự ưu tiên khi xung đột |

## Thứ tự phụ thuộc (dependency)

```mermaid
flowchart LR
    L3 --> L1 --> L2 --> L4 --> L5 --> L6
    L6 --> L7 --> L9 --> L8
    L8 --> L10
    L6 --> L12
    L8 --> L12
    L10 --> L11
    L12 --> TUI
    TUI --> L11
    L11 --> Release
```

> Quy tắc: mỗi layer chỉ phụ thuộc layer thấp hơn + Event Bus, **không bao giờ** phụ thuộc TUI.

## Index file

| # | File | Layer |
|---|---|---|
| 00 | `00-Mindmap.md` | Bản đồ tổng |
| 01 | `01-Project_Vision.md` | Tầm nhìn + S1–S10 |
| 02 | `02-System_Architecture.md` | Tổng quan 12 lớp |
| 03 | `03-Session_Manager.md` | L1 Session |
| 04 | `04-Runtime_State_Machine.md` | L2 Runtime FSM |
| 05 | `05-Event_Bus.md` | L3 Event Bus |
| 06 | `06-Context_Engine.md` | L4 + L5 |
| 07 | `07-Cognitive_Core.md` | L6 Cognitive Core |
| 08 | `08-Execution_Engine.md` | L7 Execution |
| 09 | `09-Verification_Engine.md` | L8 Verification |
| 10 | `10-Patch_Engine.md` | L9 Patch |
| 11 | `11-Memory_System.md` | L10 Memory |
| 12 | `12-Coordination_Layer.md` | L11 Multi-Agent |
| 13 | `13-Infrastructure.md` | L12 Infra |
| 14 | `14-TUI_Architecture.md` | TUI |
| 15 | `15-Implementation_Roadmap.md` | 11 sprint + CI |

*End of File 00 — Mindmap.*
