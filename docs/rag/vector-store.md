# Vector Store

yolo-code sử dụng pure-Go vector store cho semantic search — không cần external services (Pinecone, Weaviate, v.v.).

## Tổng quan

```
┌───────────────┐     ┌───────────────┐     ┌───────────────┐
│  Code / Docs  │────►│   Chunking    │────►│   Embedding    │
│  (raw text)   │     │  per-function │     │  (local model) │
└───────────────┘     └───────────────┘     └───────┬───────┘
                                                   │
                                           ┌───────▼───────┐
                                           │  Vector Store  │
                                           │  (pure Go)     │
                                           └───────┬───────┘
                                                   │
┌───────────────┐     ┌───────────────┐     ┌───────▼───────┐
│  Top-K Chunks │◄────│   Rank + Cut  │◄────│  Cosine Search │
│  (results)    │     │  score > θ    │     │  (query vec)   │
└───────────────┘     └───────────────┘     └───────────────┘
```

## Chunking Strategy

### Per-function chunking

Code được chunk theo function/method boundaries — không phải fixed-size windows.

```go
// Chunk 1
func Fibonacci(n int) int {
    if n <= 1 {
        return n
    }
    return Fibonacci(n-1) + Fibonacci(n-2)
}

// Chunk 2
func FibonacciIter(n int) int {
    a, b := 0, 1
    for i := 0; i < n; i++ {
        a, b = b, a+b
    }
    return a
}
```

### Lợi ích

- **Semantic coherence**: Mỗi chunk là 1 unit logic hoàn chỉnh
- **Better retrieval**: Query "fibonacci function" match chính xác chunk chứa function
- **No split mid-code**: Không cắt giữa dòng code

### Metadata mỗi chunk

```json
{
  "id": "chunk_001",
  "file": "internal/cognitive/core.go",
  "function": "Think",
  "line_start": 45,
  "line_end": 82,
  "type": "function",
  "language": "go"
}
```

## Embedding

### Local embedding model

yolo-code dùng embedding model chạy locally — không cần API call cho embedding.

| Thuộc tính | Giá trị |
|---|---|
| Model | Local ONNX or Go-native model |
| Dimension | 384–768 (tùy model) |
| Speed | ~1ms per chunk trên CPU |
| Cost | Free (local inference) |

### Embedding flow

```
1. Chunk code thành per-function pieces
2. Mỗi chunk → embedding model → vector
3. Vector + metadata → store trong Vector Store
```

## Vector Store

### Storage

Pure-Go in-memory vector store (HNSW hoặc brute-force cho small corpora):

```go
type VectorStore struct {
    chunks map[string]*Chunk    // id → chunk
    vecs   map[string][]float32  // id → vector
    index  *hnsw.Graph           // HNSW index (nếu lớn)
}
```

### Operations

| Operation | Mô tả | Complexity |
|---|---|---|
| `Insert(id, vec, meta)` | Thêm chunk | O(log n) HNSW |
| `Search(query_vec, k)` | Tìm top-K similar | O(log n) HNSW |
| `Delete(id)` | Xoá chunk | O(log n) HNSW |
| `Size() int` | Số chunks | O(1) |

### Cosine similarity

```go
func cosine(a, b []float32) float32 {
    var dot, normA, normB float32
    for i := range a {
        dot += a[i] * b[i]
        normA += a[i] * a[i]
        normB += b[i] * b[i]
    }
    return dot / (sqrt(normA) * sqrt(normB) + 1e-8)
}
```

## Retrieval Flow

```
1. User task → embedding model → query vector
2. VectorStore.Search(query_vec, topK=10)
3. Lọc: similarity score > threshold (θ)
4. Rank kết quả theo score (giảm dần)
5. Trả về top chunks cho Context Engine
6. Context Engine integrate vào prompt
```

### Parameters

| Parameter | Mặc định | Mô tả |
|---|---|---|
| `topK` | 10 | Số chunks trả về |
| `threshold` | 0.7 | Minimum cosine similarity |
| `rerank` | true | Re-rank bằng cross-encoder (nếu có) |

## Indexing

### Khi nào index

- **Cold start**: Index toàn bộ repo khi session bắt đầu
- **Incremental**: Re-index files khi `edit_file` hoặc `bash` thay đổi code
- **Event-driven**: File change events trigger re-index

### Index flow

```
1. Walk repo root (list_files)
2. Skip: .git/, node_modules/, vendor/, __pycache__/, .cache/, dist/
3. Mỗi file → parse → chunk per function
4. Mỗi chunk → embed → store
5. HNSW index rebuild (hoặc incremental insert)
```

## Xem thêm

- [Context Engine](context-engine.md) — Cách retrieval tích hợp vào prompt
- [Memory Lifecycle](memory-lifecycle.md) — Cách memory được cập nhật
- [Architecture](../user/architecture.md) — Vị trí Vector Store trong kiến trúc
