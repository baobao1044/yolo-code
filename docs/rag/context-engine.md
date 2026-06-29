# Context Engine (L4 + L5)

The Context Engine gathers context from multiple sources, scores relevance, and the Prompt Compiler composes the final prompt sent to the LLM.

## L4 — Context Engine

### 7 Inputs

| # | Input | Source | Description |
|---|---|---|---|
| 1 | **Files** | Repo filesystem | Contents of relevant files |
| 2 | **Conversation** | Session history | Conversation turn history |
| 3 | **Tool results** | Execution Engine | Output from tool calls |
| 4 | **Memory** | Memory System (L10) | Long-term experience |
| 5 | **Repository structure** | File tree | Directory structure |
| 6 | **Task** | User input | Task description |
| 7 | **Explicit** | `--open` flag | Files the user specified to open |

### Relevance Scoring

Each input is scored by 5 signals:

| Signal | Meaning | Example |
|---|---|---|
| **Recency** | More recent = more important | Latest tool result > result from 5 turns ago |
| **Proximity** | Near the file being edited = relevant | File in same package > file in different package |
| **Semantic** | Similarity to the task | File with "fibonacci" when task says "fibonacci" |
| **Centrality** | Hub in dependency graph | `main.go` > helper file |
| **Explicit** | User-specified | `--open main.go` = always include |

### Compression

When total context exceeds budget (token limit), the Context Engine performs compression passes:

1. **Dedup** — Remove duplicate content
2. **Summarize** — Summarize long blocks (e.g. 500-line file → 50-line summary)
3. **Budget** — Cut by token budget, prioritizing high-score items
4. **Order** — Arrange: system → task → context → conversation → tools

## L5 — Prompt Compiler

### Pipeline

```
7 Inputs → Collect → Score → Compress → Order → Wire Format → LLM
```

### Wire Format

Prompt Compiler output uses **XML + Markdown** format:

```xml
<system>
You are yolo-code, an AI coding agent...
</system>

<task>
Create a CLI tool that computes fibonacci
</task>

<context>
<file path="cmd/yolo/main.go">
package main
...
</file>
<memory type="knowledge">
This project uses Go 1.26
</memory>
</context>

<conversation>
<turn role="assistant" tool="read_file">
<result>package main...</result>
</turn>
</conversation>
```

### Tool Definitions

The Prompt Compiler also injects tool definitions into the request:

```json
{
  "tools": [
    { "type": "function", "function": { "name": "list_files", ... } },
    { "type": "function", "function": { "name": "read_file", ... } },
    { "type": "function", "function": { "name": "edit_file", ... } },
    { "type": "function", "function": { "name": "bash", ... } }
  ]
}
```

### Multi-turn Accumulation

Each Think() call in the agent loop:
1. **First time**: Compile from system prompt + task + context
2. **Subsequent turns**: Use accumulated history + new tool results
3. **No re-compile**: Avoid duplicate messages

```
Turn 1: system + user(task) → LLM → tool_calls
Turn 2: [history] + tool(role=tool, result) → LLM → tool_calls
Turn 3: [history] + tool(role=tool, result) → LLM → final answer
```

## RAG Flow

```
1. User submits task
2. Context Engine queries Memory System with task embedding
3. Memory System returns top-K relevant chunks
4. Context Engine scores all inputs (files + memory + conversation + ...)
5. Compress if exceeding budget
6. Prompt Compiler composes wire format
7. Send to LLM
8. LLM returns tool_calls or final answer
9. If tool_calls → execute → RecordToolResult → loop back to step 2
10. If final answer → done → Memory updated via events
```

## See also

- [Vector Store](vector-store.md) — Pure-Go vector store and retrieval
- [Memory Lifecycle](memory-lifecycle.md) — How memory is created, read, and deleted
- [Architecture](../user/architecture.md) — L4/L5 position in the architecture
