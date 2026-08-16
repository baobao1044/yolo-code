# Retrieval Index

yolo-code uses a pure-Go **lexical retrieval index** — hashed term-frequency similarity over per-function chunks. It runs in-process with zero external dependencies and nothing leaves the machine.

> **What this is not.** There is no embedding model in yolo-code, local or hosted, and no approximate-nearest-neighbour index. Retrieval matches **literal shared tokens**, in the tf-idf family — a query only finds chunks that contain its words. A synonym-only paraphrase scores zero (there is a test asserting exactly that: `TestDefaultEmbedderIsLexicalNotSemantic` in `internal/memory/semantic_test.go`). The `Embedder` interface is a real substitution seam: dropping a hosted or local embedding model into `memory.Deps.Embedder` upgrades this to semantic retrieval with no other change, because chunking, cosine, top-k, and eviction are all embedder-agnostic.

> **Implementation status (S13, 2026-08-12):** the store (`LexicalStore`) is a linear scan over every chunk, scored by cosine. The default vectorizer is a deterministic FNV-1a hashing term-frequency vectorizer (dim 384). Cold-start indexing (`IndexRepo`) walks the repo at session open and `BulkInsert`s chunks. `Retrieve` is wired into the Context Engine via `Memory.Retrieve` → `<rag>` prompt group. `Delete`/`Size`/`Evict` (LRU)/`SetThreshold` are implemented. See `internal/memory/semantic.go`, `internal/memory/embed.go`, `internal/memory/chunk.go`, `internal/memory/index.go`.

## Overview

```
┌───────────────┐     ┌───────────────┐     ┌────────────────┐
│  Code / Docs  │────►│   Chunking    │────►│  Vectorize     │
│  (raw text)   │     │  per-function │     │  hashed term-f │
└───────────────┘     └───────────────┘     └───────┬────────┘
                                                    │
                                           ┌────────▼───────┐
                                           │ Lexical Store  │
                                           │  (pure Go)     │
                                           └───────┬────────┘
                                                   │
┌───────────────┐     ┌───────────────┐    ┌───────▼────────┐
│  Top-K Chunks │◄────│   Rank + Cut  │◄───│ Cosine, linear │
│  (results)    │     │  score > θ    │    │ scan of chunks │
└───────────────┘     └───────────────┘    └────────────────┘
```

## Chunking Strategy

### Per-function chunking

Go code is chunked at function/method boundaries via `go/parser`, with the preceding doc comment attached. Non-Go files — and Go files that fail to parse — fall back to 40-line windows with 8 lines of overlap, so a file with no grammar is still indexed.

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

- **Whole units**: Each chunk is a complete function, so a hit is something you can read on its own
- **Better retrieval**: A task naming `Fibonacci` matches exactly the chunk containing that function — the identifier is a literal token in both
- **No split mid-code**: Never cuts in the middle of a line

Oversized functions are split at the soft (1500 char) and hard (4000 char) caps into multiple chunks sharing the signature header.

### Metadata per chunk

```go
type Chunk struct {
    Path string // repo-relative path
    Kind string // "function" | "block" (the window fallback)
    Name string // symbol name
    Text string // the chunk body, returned on a hit
}
```

Path, name, and kind travel with a hit in the returned `Part.Attr`, so the Context Engine can attribute where a RAG chunk came from. Line spans and a language tag are not tracked.

## Vectorizing

### The default vectorizer

The shipped `Embedder` is a **hashing term-frequency vectorizer**, not a model. Each chunk is tokenized on whitespace and punctuation, each token is hashed with FNV-1a into one of `dim` buckets, and that bucket is incremented. The result is a hashed bag-of-words vector; cosine over two of them measures how many literal tokens the texts share.

| Property | Value |
|---|---|
| Implementation | FNV-1a token hashing (`NewHashEmbedder`) |
| Dimension | 384 (fixed default; configurable) |
| What it captures | Literal token overlap — the tf-idf family |
| What it does not capture | Synonymy, paraphrase, any notion of meaning |
| Cost | Free; no network, no model weights, no deps |

It is deterministic (the same text always yields the same vector, so headless transcripts stay reproducible) and it discriminates between distinct term profiles, which is what makes it useful for pulling in the functions a task literally names.

### Substituting a real embedding model

`Embedder` is an interface — `Embed(ctx, texts) ([][]float32, error)`. Inject one via `memory.Deps.Embedder` and every downstream stage (chunking, cosine, top-k, threshold, LRU eviction) works unchanged. That is the upgrade path from lexical to semantic retrieval; it has not been taken.

### Indexing flow

```
1. Chunk code into per-function pieces
2. Each chunk → hashing vectorizer → term-frequency vector
3. Vector + metadata → store in the lexical store
```

## Lexical Store

### Storage

Pure-Go in-memory store, backed by a slice of chunks scanned linearly on every query:

```go
type LexicalStore struct {
    chunks    []chunkVec // text + metadata + term-frequency vector
    threshold float64    // minimum cosine similarity to return (θ)
}
```

Constructors: `NewLexicalStore`, `NewLexicalStoreWith(emb)`, `NewLexicalStoreWithFS(emb, fs)`. `Store.Semantic()` still returns it — the accessor keeps the spec's name (File 11 §11.6) and has live call sites; the type it returns is a `*LexicalStore`.

### Operations

| Operation | Description | Complexity |
|---|---|---|
| `BulkInsert(ctx, chunks)` | Add chunks | O(n) in chunks added |
| `Retrieve(ctx, query, budget)` | Top-K by cosine | O(n) — linear scan |
| `Reindex(ctx, path, content)` | Atomically replace a path's chunks | O(n) |
| `Delete(id)` | Remove chunk | O(n) |
| `Evict(capacity)` | Drop least-recently-retrieved | O(n log n) |
| `Size() int` | Number of chunks | O(1) |

A linear scan is the right shape at this corpus size — a repo's worth of function-sized chunks scores in well under a frame. An ANN index (HNSW or otherwise) is a future concern, not a shipped one.

### Cosine similarity

```go
func cosine(a, b []float32) float64 {
    var dot, na, nb float64
    for i := range a {
        dot += float64(a[i]) * float64(b[i])
        na += float64(a[i]) * float64(a[i])
        nb += float64(b[i]) * float64(b[i])
    }
    if na == 0 || nb == 0 {
        return 0 // a zero vector has no direction; no division by zero
    }
    return dot / (sqrt(na) * sqrt(nb))
}
```

Because the hashing vectorizer only produces non-negative counts, scores land in `[0, 1]`.

## Retrieval Flow

```
1. User task → hashing vectorizer → query vector
2. Score every chunk by cosine (linear scan), sort descending
3. Filter: similarity > threshold (θ)
4. Keep up to `budget` chunks; bump their lastAccess (LRU)
5. Return chunks to the Context Engine as KindRAG parts
6. Context Engine integrates them into the prompt under <rag>
```

### Parameters

| Parameter | Default | Description |
|---|---|---|
| `budget` (top-K) | 10 | Maximum chunks returned; the Context Engine passes this |
| `threshold` (θ) | 0 | Minimum cosine similarity; at 0 only non-positive scores are dropped. Set with `SetThreshold` |

There is no re-ranking stage — the cosine order is the final order.

## Indexing

### When to index

- **Cold start**: `IndexRepo` walks and indexes the whole repo when a session opens
- **Incremental**: the memory listener re-indexes the changed paths on `patch.applied`

### Index flow

```
1. Walk repo root (filepath.WalkDir, lexical order → deterministic)
2. Skip: .git/, vendor/, node_modules/, __pycache__/, dist/, .cache/,
         build/, .idea/, .vscode/, dotfiles, and files over 1 MiB
3. Go files → go/parser → one chunk per function (comment included);
   everything else → 40-line windows with 8-line overlap
4. Each chunk → vectorize → BulkInsert
```

## See also

- [Context Engine](context-engine.md) — How retrieval integrates into the prompt
- [Memory Lifecycle](memory-lifecycle.md) — How memory is updated
- [Architecture](../user/architecture.md) — Where the retrieval index sits in the architecture
