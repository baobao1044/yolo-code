// Semantic memory (Vector RAG) — retrieve code by meaning, not by grep
// (File 11 §11.6). The store embeds chunks via an Embedder, retrieves the top-k
// by cosine similarity, and returns them budget-capped as Parts.
//
// stdlib-only: the MVP embedder is a deterministic local hash embedder (L10-003,
// embed.go) so the store is offline-testable; a real OpenAI/Ollama embedder
// plugs behind the Embedder interface (§11.7.4). The backing is in-memory for
// L10-003; L10-005 adds JSON persistence, L10-004 adds chunking + reindex on
// patch.applied.
//
// Concurrency: single-writer is the listener goroutine (Invariant I1); a mutex
// guards the chunks slice for safety if a multi-task scheduler shares the store.

package memory

import (
	"context"
	"sort"
	"sync"
)

// chunkVec is one embedded chunk (§11.6.2): the path/kind/name it came from,
// the text (returned on a hit), and its embedding vector (for cosine).
type chunkVec struct {
	id     int
	path   string
	kind   string // "function" | "block"
	name   string
	text   string
	vector []float32
}

// FS reads a file's content for reindexing (L10-004). The composition root
// wires the sandbox-confined reader; memory stays free of infra (the import
// matrix lets memory import only event + stdlib). Mirrors verify/patch's FS
// seam.
type FS interface {
	Read(ctx context.Context, path string) ([]byte, error)
}

// SemanticStore holds the embedded chunks and retrieves the top-k by cosine.
// fs is the reindex reader (set by NewSemanticStoreWithFS); nil means Reindex
// can't read a path's content (it's a no-op unless content is passed directly).
type SemanticStore struct {
	embed  Embedder
	fs     FS
	mu     sync.RWMutex
	chunks []chunkVec
	nextID int
}

// NewSemanticStore returns a store with no embedder (Retrieve returns nil —
// kept for L10-001's aggregate wiring). Use NewSemanticStoreWith for a real
// embedder.
func NewSemanticStore() *SemanticStore { return &SemanticStore{} }

// NewSemanticStoreWith returns a store backed by the given embedder.
func NewSemanticStoreWith(emb Embedder) *SemanticStore {
	return &SemanticStore{embed: emb}
}

// NewSemanticStoreWithFS returns a store backed by the given embedder + an FS
// reader so Reindex can read a path's content on patch.applied (L10-004).
func NewSemanticStoreWithFS(emb Embedder, fs FS) *SemanticStore {
	return &SemanticStore{embed: emb, fs: fs}
}

// addChunk embeds + appends a chunk. Test-visible (the L10-003 exit-bar test
// seeds the store); production reindexing goes through Reindex (L10-004).
func (s *SemanticStore) addChunk(_ context.Context, c chunkVec) {
	if s.embed == nil {
		s.embed = NewHashEmbedder(256)
	}
	vecs, _ := s.embed.Embed(context.Background(), []string{c.text})
	if len(vecs) > 0 {
		c.vector = vecs[0]
	}
	s.mu.Lock()
	s.nextID++
	c.id = s.nextID
	s.chunks = append(s.chunks, c)
	s.mu.Unlock()
}

// Retrieve returns the top-k chunks for a query, budget-capped (§11.6.2). It
// embeds the query, scores every chunk by cosine, sorts descending, and
// returns up to `budget` Parts (one per hit, carrying path/name/kind in Attr
// so the Context Engine can attribute the RAG hit). An empty store or a nil
// embedder returns nil.
func (s *SemanticStore) Retrieve(ctx context.Context, query string, budget int) []Part {
	if s.embed == nil || budget <= 0 {
		return nil
	}
	s.mu.RLock()
	if len(s.chunks) == 0 {
		s.mu.RUnlock()
		return nil
	}
	chunks := append([]chunkVec(nil), s.chunks...)
	s.mu.RUnlock()

	qvecs, _ := s.embed.Embed(ctx, []string{query})
	if len(qvecs) == 0 {
		return nil
	}
	q := qvecs[0]

	type hit struct {
		i   int
		sim float64
	}
	hits := make([]hit, len(chunks))
	for i, c := range chunks {
		hits[i] = hit{i: i, sim: cosine(q, c.vector)}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].sim > hits[b].sim })

	out := make([]Part, 0, budget)
	for _, h := range hits {
		if h.sim <= 0 {
			break // no similarity → stop (a disjoint chunk adds no signal)
		}
		if len(out) >= budget {
			break
		}
		c := chunks[h.i]
		out = append(out, Part{
			Kind:   KindRAG,
			Source: c.path,
			Text:   c.text,
			Score:  h.sim,
			Attr:   map[string]string{"path": c.path, "name": c.name, "kind": c.kind},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Reindex replaces a path's chunks atomically (§11.7.5): chunk the new content,
// embed each chunk, then in one locked pass drop the path's old chunks and
// insert the new ones (a retrieval sees old or new, never a mix). Content comes
// from the `content` arg if non-nil; otherwise the store's FS reads the path
// (the listener passes nil + relies on the FS). A nil content with no FS, or a
// read failure, leaves the path de-indexed (its old chunks dropped) — a missing
// file has nothing to index.
func (s *SemanticStore) Reindex(ctx context.Context, path string, content []byte) {
	// Resolve content: explicit arg, else read via FS.
	if content == nil && s.fs != nil {
		var err error
		content, err = s.fs.Read(ctx, path)
		if err != nil {
			content = nil // read failed → nothing to index
		}
	}

	// Chunk + embed OUTSIDE the lock (I/O + CPU work; no shared state touched).
	chunks := ChunkFile(path, content)
	var newVecs []chunkVec
	if s.embed == nil {
		s.embed = NewHashEmbedder(256)
	}
	for _, c := range chunks {
		vecs, _ := s.embed.Embed(ctx, []string{c.Text})
		cv := chunkVec{path: c.Path, kind: c.Kind, name: c.Name, text: c.Text}
		if len(vecs) > 0 {
			cv.vector = vecs[0]
		}
		newVecs = append(newVecs, cv)
	}

	// Atomic replace: one locked pass drops the path's old chunks and inserts
	// the new ones. A retrieval before this point sees old; after, sees new.
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.chunks[:0]
	for _, c := range s.chunks {
		if c.path != path {
			kept = append(kept, c)
		}
	}
	for _, cv := range newVecs {
		s.nextID++
		cv.id = s.nextID
		kept = append(kept, cv)
	}
	s.chunks = kept
}
