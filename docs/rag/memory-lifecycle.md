# Memory Lifecycle

Memory System (L10) quản lý 6 loại memory, được cập nhật **HOÀN TOÀN qua events** — không bao giờ ghi trực tiếp.

## Quy tắc vàng

> **Memory chỉ cập nhật qua events. Không có code nào gọi Memory.Write() trực tiếp.**

```
Tool Execution → Event → Memory Update
LLM Response   → Event → Memory Update
User Input     → Event → Memory Update
```

## 6 Memory Types

### 1. Working Memory

| Thuộc tính | Giá trị |
|---|---|
| **Nội dung** | Task hiện tại, active context |
| **Lifecycle** | Bắt đầu → kết thúc task |
| **Cập nhật** | Mỗi state transition |
| **Size** | Nhỏ (task + current state) |

```
Event: task.created → Working Memory = { task: "tạo fibonacci CLI" }
Event: state.change → Working Memory.state = "exec"
Event: task.completed → Working Memory.clear()
```

### 2. Conversation Memory

| Thuộc tính | Giá trị |
|---|---|
| **Nội dung** | Lịch sử hội thoại (user messages + LLM responses) |
| **Lifecycle** | Toàn bộ session |
| **Cập nhật** | Mỗi turn |
| **Size** | Lớn (accumulate theo turns) |

```
Event: think.complete → Conversation Memory.append({ role: "assistant", content: "..." })
Event: tool.result → Conversation Memory.append({ role: "tool", name: "bash", result: "..." })
```

### 3. Exec Memory

| Thuộc tính | Giá trị |
|---|---|
| **Nội dung** | Kết quả tool execution |
| **Lifecycle** | Nửa vĩnh viễn (có retention policy) |
| **Cập nhật** | Mỗi tool call |
| **Size** | Lớn nhất (file contents, command outputs) |

```
Event: observation → Exec Memory.append({ tool: "bash", cmd: "go test", stdout: "PASS", exit: 0 })
Event: observation → Exec Memory.append({ tool: "read_file", file: "main.go", content: "..." })
```

### 4. Repository Memory

| Thuộc tính | Giá trị |
|---|---|
| **Nội dung** | Code structure, file tree, function signatures |
| **Lifecycle** | Vĩnh viễn (cho đến khi code thay đổi) |
| **Cập nhật** | Khi file thay đổi |
| **Size** | Vừa (metadata, không full content) |

```
Event: file.changed → Repository Memory.update({ file: "main.go", functions: ["main", "fib"], imports: [...] })
Event: file.deleted → Repository Memory.remove("main.go")
```

### 5. Knowledge Memory

| Thuộc tính | Giá trị |
|---|---|
| **Nội dung** | Kinh nghiệm tích lũy: patterns, gotchas, best practices |
| **Lifecycle** | Vĩnh viễn (cross-session) |
| **Cập nhật** | Khi agent học được điều mới |
| **Size** | Nhỏ (insights, không raw data) |

```
Event: task.completed → Knowledge Memory.append({ insight: "Project này dùng Go 1.26, không dùng generics" })
Event: verify.fail → Knowledge Memory.append({ insight: "Lệnh go test cần CGO cho race detector" })
```

### 6. Preference Memory

| Thuộc tính | Giá trị |
|---|---|
| **Nội dung** | User preferences: style, conventions |
| **Lifecycle** | Vĩnh viễn (cross-session) |
| **Cập nhật** | Khi user chỉ định |
| **Size** | Nhỏ |

```
Event: user.preference → Preference Memory.set({ style: "conventional commits", language: "vietnamese" })
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
1. Context Engine nhận task
2. Query Working Memory → active context
3. Query Conversation Memory → history
4. Query Exec Memory → tool results gần đây
5. Query Repository Memory → file tree, signatures
6. Query Vector Store (Knowledge + Repository) → semantic search
7. Query Preference Memory → user style
8. Score tất cả inputs
9. Compress nếu vượt budget
10. Compile thành prompt
```

### Retrieval priority

| Priority | Source | Reason |
|---|---|---|
| 1 (cao nhất) | Working Memory | Task hiện tại là quan trọng nhất |
| 2 | Conversation Memory | Context hội thoại |
| 3 | Exec Memory (recent) | Kết quả tool vừa chạy |
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
- **Least-recently-used** → Evict Knowledge/Preference entries khi store đầy

### Vector Store cleanup

```go
// Evict entries khi store vượt capacity
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

## Xem thêm

- [Context Engine](context-engine.md) — Cách memory feeds vào prompt
- [Vector Store](vector-store.md) — Technical details về vector store
- [Architecture](../user/architecture.md) — Vị trí Memory System trong kiến trúc
