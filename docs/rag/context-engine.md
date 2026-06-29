# Context Engine (L4 + L5)

Context Engine thu thập context từ nhiều nguồn, chấm điểm relevance, và Prompt Compiler compose thành prompt cuối cùng gửi đến LLM.

## L4 — Context Engine

### 7 Inputs

| # | Input | Nguồn | Mô tả |
|---|---|---|---|
| 1 | **Files** | Repo filesystem | Nội dung files liên quan |
| 2 | **Conversation** | Session history | Lịch sử hội thoại turns |
| 3 | **Tool results** | Execution Engine | Output từ tool calls |
| 4 | **Memory** | Memory System (L10) | Kinh nghiệm dài hạn |
| 5 | **Repository structure** | File tree | Cấu trúc thư mục |
| 6 | **Task** | User input | Task description |
| 7 | **Explicit** | `--open` flag | Files user chỉ định mở |

### Relevance Scoring

Mỗi input được chấm điểm theo 5 signals:

| Signal | Ý nghĩa | Ví dụ |
|---|---|---|
| **Recency** | Gần đây = quan trọng hơn | Tool result vừa rồi > result 5 turns trước |
| **Proximity** | Gần file đang sửa = liên quan | File cùng package > file package khác |
| **Semantic** | Similarity với task | File có "fibonacci" khi task nói "fibonacci" |
| **Centrality** | Hub trong dependency graph | `main.go` > helper file |
| **Explicit** | User chỉ định | `--open main.go` = luôn include |

### Compression

Khi tổng context vượt budget (token limit), Context Engine thực hiện compression passes:

1. **Dedup** — Loại bỏ nội dung trùng lặp
2. **Summarize** — Tóm tắt blocks dài (vd: file 500 dòng → tóm tắt 50 dòng)
3. **Budget** — Cắt theo token budget, ưu tiên high-score items
4. **Order** — Sắp xếp: system → task → context → conversation → tools

## L5 — Prompt Compiler

### Pipeline

```
7 Inputs → Collect → Score → Compress → Order → Wire Format → LLM
```

### Wire Format

Prompt Compiler output sử dụng **XML + Markdown** format:

```xml
<system>
Bạn là yolo-code, một AI coding agent...
</system>

<task>
Tạo 1 CLI tool tính fibonacci
</task>

<context>
<file path="cmd/yolo/main.go">
package main
...
</file>
<memory type="knowledge">
Project này dùng Go 1.26
</memory>
</context>

<conversation>
<turn role="assistant" tool="read_file">
<result>package main...</result>
</turn>
</conversation>
```

### Tool Definitions

Prompt Compiler cũng inject tool definitions vào request:

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

Mỗi Think() call trong agent loop:
1. **Lần đầu**: Compile từ system prompt + task + context
2. **Lần sau**: Sử dụng accumulated history + tool results mới
3. **Không re-compile**: Tránh duplicate messages

```
Turn 1: system + user(task) → LLM → tool_calls
Turn 2: [history] + tool(role=tool, result) → LLM → tool_calls
Turn 3: [history] + tool(role=tool, result) → LLM → final answer
```

## RAG Flow

```
1. User gửi task
2. Context Engine query Memory System với task embedding
3. Memory System trả về top-K relevant chunks
4. Context Engine score tất cả inputs (files + memory + conversation + ...)
5. Compress nếu vượt budget
6. Prompt Compiler compose wire format
7. Gửi đến LLM
8. LLM trả về tool_calls hoặc final answer
9. Nếu tool_calls → execute → RecordToolResult → loop lại bước 2
10. Nếu final answer → done → Memory cập nhật qua events
```

## Xem thêm

- [Vector Store](vector-store.md) — Pure-Go vector store và retrieval
- [Memory Lifecycle](memory-lifecycle.md) — Cách memory được tạo, đọc, và xoá
- [Architecture](../user/architecture.md) — Vị trí L4/L5 trong kiến trúc
