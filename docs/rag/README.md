# RAG & Memory System

Hệ thống RAG (Retrieval-Augmented Generation) và Memory của yolo-code — Layer 10 trong kiến trúc 12 layers.

## Tổng quan

yolo-code sử dụng RAG để cung cấp context liên quan cho LLM, kết hợp với memory system lưu trữ kinh nghiệm dài hạn. Cả hai được cập nhật **HOÀN TOÀN qua events** — không bao giờ ghi trực tiếp.

## Nội dung

- [Context Engine](context-engine.md) — L4 + L5: thu thập, chấm điểm, và compile context
- [Vector Store](vector-store.md) — Pure-Go vector store, chunking, embedding, retrieval
- [Memory Lifecycle](memory-lifecycle.md) — 6 memory types, event-driven updates, retention

## Kiến trúc tổng quan

```
┌─────────────────────────────────────────┐
│           Prompt gửi đến LLM           │
└──────────────┬──────────────────────────┘
               │
    ┌──────────▼──────────┐
    │   Prompt Compiler   │  L5
    │ dedup → sum → budget │
    └──────────┬──────────┘
               │
    ┌──────────▼──────────┐
    │   Context Engine    │  L4
    │  7 inputs → score   │
    │  → compress → rank   │
    └──────────┬──────────┘
               │
    ┌──────────▼──────────┐
    │   Memory System     │  L10
    │  6 types × vector   │
    │  store → retrieve    │
    └─────────────────────┘
```

## 6 Memory Types

| Type | Nội dung | Ví dụ |
|---|---|---|
| **Working** | Task hiện tại, context ngắn hạn | "User muốn tạo fibonacci CLI" |
| **Conversation** | Lịch sử hội thoại | Turn 1: task, Turn 2: tool results |
| **Exec** | Kết quả tool execution | `go test` output, file contents |
| **Repository** | Code structure, file tree | `main.go: func main() {...}` |
| **Knowledge** | Kinh nghiệm tích lũy | "Project này dùng Go 1.26" |
| **Preference** | User preferences | "Luôn dùng conventional commits" |

## Xem thêm

- [Architecture](../user/architecture.md) — Vị trí RAG/Memory trong kiến trúc tổng thể
- Design doc `06-Context_Engine.md` — L4 + L5 technical spec
- Design doc `11-Memory_System.md` — L10 technical spec
