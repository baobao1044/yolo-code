# Vector Store

yolo-code uses a pure-Go vector store for semantic search — no external services required (Pinecone, Weaviate, etc.).

## Overview

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

Code is chunked by function/method boundaries — not fixed-size windows.

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

### Benefits

- **Semantic coherence**: Each chunk is a complete logical unit
- **Better retrieval**: Query "fibonacci function" matches exactly the chunk containing the function
- **No split mid-code**: Never cuts in the middle of a line

### Metadata per chunk

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

yolo-code uses a locally-running embedding model — no API calls needed for embedding.

| Property | Value |
|---|---|
| Model | Local ONNX or Go-native model |
| Dimension | 384–768 (depends on model) |
| Speed | ~1ms per chunk on CPU |
| Cost | Free (local inference) |

### Embedding flow

```
1. Chunk code into per-function pieces
2. Each chunk → embedding model → vector
3. Vector + metadata → store in Vector Store
```

## Vector Store

### Storage

Pure-Go in-memory vector store (HNSW or brute-force for small corpora):

```go
type VectorStore struct {
    chunks map[string]*Chunk    // id → chunk
    vecs   map[string][]float32  // id → vector
    index  *hnsw.Graph           // HNSW index (for large corpora)
}
```

### Operations

| Operation | Description | Complexity |
|---|---|---|
| `Insert(id, vec, meta)` | Add chunk | O(log n) HNSW |
| `Search(query_vec, k)` | Find top-K similar | O(log n) HNSW |
| `Delete(id)` | Remove chunk | O(log n) HNSW |
| `Size() int` | Number of chunks | O(1) |

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
3. Filter: similarity score > threshold (θ)
4. Rank results by score (descending)
5. Return top chunks to Context Engine
6. Context Engine integrates into prompt
```

### Parameters

| Parameter | Default | Description |
|---|---|---|
| `topK` | 10 | Number of chunks to return |
| `threshold` | 0.7 | Minimum cosine similarity |
| `rerank` | true | Re-rank with cross-encoder (if available) |

## Indexing

### When to index

- **Cold start**: Index entire repo when a session begins
- **Incremental**: Re-index files when `edit_file` or `bash` changes code
- **Event-driven**: File change events trigger re-index

### Index flow

```
1. Walk repo root (list_files)
2. Skip: .git/, node_modules/, vendor/, __pycache__/, .cache/, dist/
3. Each file → parse → chunk per function
4. Each chunk → embed → store
5. HNSW index rebuild (or incremental insert)
```

## See also

- [Context Engine](context-engine.md) — How retrieval integrates into the prompt
- [Memory Lifecycle](memory-lifecycle.md) — How memory is updated
- [Architecture](../user/architecture.md) — Vector Store position in the architecture
