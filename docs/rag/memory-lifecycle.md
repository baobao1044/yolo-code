# Memory Lifecycle

The Memory System (L10) manages 6 types of memory, updated **entirely via events** — never through direct writes.

## Golden Rule

> **Memory is only updated via events. No code calls Memory.Write() directly.**

```
Tool Execution → Event → Memory Update
LLM Response   → Event → Memory Update
User Input      → Event → Memory Update
```

## 6 Memory Types

### 1. Working Memory

| Property | Value |
|---|---|
| **Content** | Current task, active context |
| **Lifecycle** | Start → end of task |
| **Update** | Every state transition |
| **Size** | Small (task + current state) |

```
Event: task.created → Working Memory = { task: "create fibonacci CLI" }
Event: state.change → Working Memory.state = "exec"
Event: task.completed → Working Memory.clear()
```

### 2. Conversation Memory

| Property | Value |
|---|---|
| **Content** | Conversation history (user messages + LLM responses) |
| **Lifecycle** | Entire session |
| **Update** | Every turn |
| **Size** | Large (accumulates across turns) |

```
Event: think.complete → Conversation Memory.append({ role: "assistant", content: "..." })
Event: tool.result → Conversation Memory.append({ role: "tool", name: "bash", result: "..." })
```

### 3. Exec Memory

| Property | Value |
|---|---|
| **Content** | Tool execution results |
| **Lifecycle** | Semi-permanent (has retention policy) |
| **Update** | Every tool call |
| **Size** | Largest (file contents, command outputs) |

```
Event: observation → Exec Memory.append({ tool: "bash", cmd: "go test", stdout: "PASS", exit: 0 })
Event: observation → Exec Memory.append({ tool: "read_file", file: "main.go", content: "..." })
```

### 4. Repository Memory

| Property | Value |
|---|---|
| **Content** | Code structure, file tree, function signatures |
| **Lifecycle** | Permanent (until code changes) |
| **Update** | When files change |
| **Size** | Medium (metadata, not full content) |

```
Event: file.changed → Repository Memory.update({ file: "main.go", functions: ["main", "fib"], imports: [...] })
Event: file.deleted → Repository Memory.remove("main.go")
```

### 5. Knowledge Memory

| Property | Value |
|---|---|
| **Content** | Accumulated experience: patterns, gotchas, best practices |
| **Lifecycle** | Permanent (cross-session) |
| **Update** | When the agent learns something new |
| **Size** | Small (insights, not raw data) |

```
Event: task.completed → Knowledge Memory.append({ insight: "This project uses Go 1.26, no generics" })
Event: verify.fail → Knowledge Memory.append({ insight: "Race detector requires CGO" })
```

### 6. Preference Memory

| Property | Value |
|---|---|
| **Content** | User preferences: style, conventions |
| **Lifecycle** | Permanent (cross-session) |
| **Update** | When user specifies |
| **Size** | Small |

```
Event: user.preference → Preference Memory.set({ style: "conventional commits", language: "english" })
```

## Write Paths (Event → Memory)

```
┌─────────────────┐
│  Event Bus (L3) │
└────┬───┬───┬────┘
     │   │   │
     │   │   └──────────────────────┐
     │   │                          │
     ▼   ▼                          ▼
  Working  Conversation      Exec Memory
  Memory   Memory                │
                                ▼
                          Repository Memory
                                │
                          ┌─────▼──────┐
                          │  Vector    │
                          │  Store     │
                          │  (embed +  │
                          │   index)   │
                          └─────┬──────┘
                                │
                    ┌───────────┼───────────┐
                    ▼           ▼           ▼
              Knowledge    Preference   Repository
              Memory       Memory       Memory
```

### Event → Memory mapping

| Event | Memory Type | Action |
|---|---|---|
| `task.created` | Working | Set task |
| `task.completed` | Working | Clear |
| `task.failed` | Knowledge | Record insight |
| `state.change` | Working | Update state |
| `think.complete` | Conversation | Append assistant message |
| `observation` | Exec | Append tool result |
| `observation` | Conversation | Append tool message |
| `file.changed` | Repository | Update file metadata |
| `file.deleted` | Repository | Remove file |
| `verify.pass` | Knowledge | Record success pattern |
| `verify.fail` | Knowledge | Record failure insight |
| `user.preference` | Preference | Set preference |

## Read Paths (Memory → Context)

```
1. Context Engine receives task
2. Query Working Memory → active context
3. Query Conversation Memory → history
4. Query Exec Memory → recent tool results
5. Query Repository Memory → file tree, signatures
6. Query Vector Store (Knowledge + Repository) → semantic search
7. Query Preference Memory → user style
8. Score all inputs
9. Compress if exceeding budget
10. Compile into prompt
```

### Retrieval priority

| Priority | Source | Reason |
|---|---|---|
| 1 (highest) | Working Memory | Current task is most important |
| 2 | Conversation Memory | Conversation context |
| 3 | Exec Memory (recent) | Recently run tool results |
| 4 | Repository Memory | Code structure |
| 5 | Vector Store (semantic) | Knowledge + similar code |
| 6 | Preference Memory | Style preferences |

## Retention & Cleanup

### Per-type retention

| Type | Policy | Duration |
|---|---|---|
| Working | Clear on task complete | 1 task |
| Conversation | Keep full session | 1 session |
| Exec | Rolling window | Last 50 results |
| Repository | Update on change | Until file changes |
| Knowledge | Permanent + evict least-used | Cross-session |
| Preference | Permanent | Cross-session |

### Cleanup triggers

- **Task complete** → Clear Working Memory
- **Session end** → Clear Working + Conversation + Exec
- **File changed** → Re-index Repository Memory
- **Budget exceeded** → Evict lowest-score Knowledge entries
- **Least-recently-used** → Evict Knowledge/Preference entries when store is full

### Vector Store cleanup

```go
// Evict entries when store exceeds capacity
func (s *VectorStore) Evict(capacity int) {
    if len(s.chunks) <= capacity {
        return
    }
    // Sort by last_accessed, evict oldest
    sorted := sortByAccess(s.chunks)
    for _, chunk := range sorted[:len(sorted)-capacity] {
        s.Delete(chunk.ID)
    }
}
```

## See also

- [Context Engine](context-engine.md) — How memory feeds into the prompt
- [Vector Store](vector-store.md) — Vector store technical details
- [Architecture](../user/architecture.md) — Memory System position in the architecture
