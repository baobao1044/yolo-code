# RAG & Memory System

The RAG (Retrieval-Augmented Generation) and Memory system of yolo-code — Layer 10 in the 12-layer architecture.

## Overview

yolo-code uses RAG to provide relevant context to the LLM, combined with a memory system that stores long-term experience. Both are updated **entirely via events** — never through direct writes.

## Contents

- [Context Engine](context-engine.md) — L4 + L5: gather, score, and compile context
- [Vector Store](vector-store.md) — Pure-Go vector store, chunking, embedding, retrieval
- [Memory Lifecycle](memory-lifecycle.md) — 6 memory types, event-driven updates, retention

## Architecture Overview

```
┌─────────────────────────────────────────┐
│           Prompt sent to LLM           │
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

| Type | Content | Example |
|---|---|---|
| **Working** | Current task, short-term context | "User wants to create a fibonacci CLI" |
| **Conversation** | Conversation history | Turn 1: task, Turn 2: tool results |
| **Exec** | Tool execution results | `go test` output, file contents |
| **Repository** | Code structure, file tree | `main.go: func main() {...}` |
| **Knowledge** | Accumulated experience | "This project uses Go 1.26" |
| **Preference** | User preferences | "Always use conventional commits" |

## See also

- [Architecture](../user/architecture.md) — RAG/Memory position in the overall architecture
- Design doc `06-Context_Engine.md` — L4 + L5 technical spec
- Design doc `11-Memory_System.md` — L10 technical spec
