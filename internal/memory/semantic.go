// The code-chunk retrieval index (File 11 §11.6 calls this "Semantic Memory /
// Vector RAG"). Read the name literally and you will be misled, so state it
// plainly here: with the embedder this binary actually ships, retrieval is
// LEXICAL, not semantic. The default Embedder (embed.go) hashes whitespace-
// separated tokens into fixed buckets and counts them, so the "vector" is a
// hashed bag of words and the cosine over it measures shared literal tokens.
// A query and a chunk that mean the same thing in different words score zero.
// There is no model, no learned representation, and nothing here understands
// meaning — retrieve-by-meaning is what the interface is SHAPED for, not what
// it currently does.
//
// The shape is the point: the store only ever talks to an Embedder (§11.7.4),
// so dropping in a real OpenAI/Ollama embedder turns this into genuine
// semantic search with no change below this line. Until then, treat it as a
// hashed term-frequency index — better than nothing, weaker than grep with a
// good regex, and honest about which.
//
// stdlib-only: the default embedder is deterministic and local (L10-003,
// embed.go) so the store is offline-testable with zero deps. The backing is
// in-memory; L10-004 adds chunking + reindex on patch.applied.
//
// Concurrency: single-writer is the listener goroutine (Invariant I1); a mutex
// guards the chunks slice for safety if a multi-task scheduler shares the store.

package memory

import (
	"context"
	"sort"
	"sync"
	"time"
)

// chunkVec is one embedded chunk (§11.6.2): the path/kind/name it came from,
// the text (returned on a hit), its embedding vector (for cosine), and the
// last-accessed time (for LRU eviction, §11.6.3).
type chunkVec struct {
	id         int
	path       string
	kind       string // "function" | "block"
	name       string
	text       string
	vector     []float32
	lastAccess time.Time
}

// FS reads a file's content for reindexing (L10-004). The composition root
// wires the sandbox-confined reader; memory stays free of infra (the import
// matrix lets memory import only event + stdlib). Mirrors verify/patch's FS
// seam.
type FS interface {
	Read(ctx context.Context, path string) ([]byte, error)
}

// LexicalStore holds the embedded chunks and retrieves the top-k by cosine.
// Named for what it does with the shipped embedder — lexical (hashed
// term-frequency) similarity, not semantic similarity; see the file header.
// It is reached through Store.Semantic(), which keeps the spec's §11.6 name.
//
// fs is the reindex reader (set by NewLexicalStoreWithFS); nil means Reindex
// can't read a path's content (it's a no-op unless content is passed directly).
// threshold is the minimum cosine similarity a chunk must reach to be returned
// (§11.6.2 θ; default 0 → only non-positive similarities are filtered, matching
// the original behavior). Read SetThreshold before setting it: θ is specified
// for a semantic embedder and this store ships a lexical one, so the spec's
// number is not transferable and the plausible-looking values are the lethal
// ones.
type LexicalStore struct {
	embed     Embedder
	fs        FS
	mu        sync.RWMutex
	chunks    []chunkVec
	nextID    int
	threshold float64
}

// NewLexicalStore returns a store with no embedder (Retrieve returns nil —
// kept for L10-001's aggregate wiring). Use NewLexicalStoreWith for a real
// embedder.
func NewLexicalStore() *LexicalStore { return &LexicalStore{} }

// NewLexicalStoreWith returns a store backed by the given embedder.
func NewLexicalStoreWith(emb Embedder) *LexicalStore {
	return &LexicalStore{embed: emb}
}

// NewLexicalStoreWithFS returns a store backed by the given embedder + an FS
// reader so Reindex can read a path's content on patch.applied (L10-004).
func NewLexicalStoreWithFS(emb Embedder, fs FS) *LexicalStore {
	return &LexicalStore{embed: emb, fs: fs}
}

// embedder returns the store's embedder, installing the default hash embedder
// on first use. The install is under the write lock: NewLexicalStore leaves
// embed nil, so two concurrent inserts would otherwise both read-then-write
// s.embed — a data race, and one of the two embedders is silently discarded
// after chunks have been vectorised with it. Every insert path goes through
// here.
func (s *LexicalStore) embedder() Embedder {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.embed == nil {
		s.embed = NewHashEmbedder(384)
	}
	return s.embed
}

// addChunk embeds + appends a chunk. Test-visible (the L10-003 exit-bar test
// seeds the store); production reindexing goes through Reindex (L10-004).
func (s *LexicalStore) addChunk(_ context.Context, c chunkVec) {
	vecs, _ := s.embedder().Embed(context.Background(), []string{c.text})
	if len(vecs) > 0 {
		c.vector = vecs[0]
	}
	s.mu.Lock()
	s.nextID++
	c.id = s.nextID
	s.chunks = append(s.chunks, c)
	s.mu.Unlock()
}

// Retrieve returns the top-k chunks for a query, budget-capped (§11.6.2).
// Ranking is by cosine over the Embedder's vectors — with the default embedder
// that is shared-token overlap, so a query only matches chunks that literally
// contain its words (see the file header). It embeds the query, scores every
// chunk by cosine, sorts descending, and
// returns up to `budget` Parts (one per hit, carrying path/name/kind in Attr
// so the Context Engine can attribute the RAG hit). Chunks below the threshold
// (§11.6.2 θ, default 0 → non-positive similarities filtered) are dropped. A
// returned chunk's lastAccess is bumped so Evict's LRU order reflects recent
// use. An empty store or a nil embedder returns nil.
func (s *LexicalStore) Retrieve(ctx context.Context, query string, budget int) []Part {
	if budget <= 0 {
		return nil
	}
	// embed is read under the lock, not bare: an insert on another goroutine
	// may be installing the default embedder right now. Retrieve does not
	// install one — a store with no embedder has no chunks to rank anyway.
	s.mu.RLock()
	emb := s.embed
	if emb == nil || len(s.chunks) == 0 {
		s.mu.RUnlock()
		return nil
	}
	chunks := append([]chunkVec(nil), s.chunks...)
	threshold := s.threshold
	s.mu.RUnlock()

	qvecs, _ := emb.Embed(ctx, []string{query})
	if len(qvecs) == 0 {
		return nil
	}
	q := qvecs[0]

	type hit struct {
		id  int
		sim float64
	}
	hits := make([]hit, len(chunks))
	for i, c := range chunks {
		hits[i] = hit{id: c.id, sim: cosine(q, c.vector)}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].sim > hits[b].sim })

	kept := make([]hit, 0, budget)
	for _, h := range hits {
		if len(kept) >= budget {
			break
		}
		if threshold > 0 {
			if h.sim < threshold {
				break // below θ → no more useful hits (sorted desc)
			}
		} else if h.sim <= 0 {
			break // default: filter non-positive (disjoint chunks add no signal)
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		return nil
	}

	// Bump lastAccess for the returned chunks (LRU, §11.6.3) under the write
	// lock, then build Parts from the snapshot. A hit that was evicted between
	// the snapshot and the bump is simply skipped (its id is gone).
	now := time.Now()
	keptIDs := make(map[int]bool, len(kept))
	for _, h := range kept {
		keptIDs[h.id] = true
	}
	s.mu.Lock()
	for i := range s.chunks {
		if keptIDs[s.chunks[i].id] {
			s.chunks[i].lastAccess = now
		}
	}
	s.mu.Unlock()

	out := make([]Part, 0, len(kept))
	for _, h := range kept {
		for _, c := range chunks {
			if c.id == h.id {
				out = append(out, Part{
					Kind:   KindRAG,
					Source: c.path,
					Text:   c.text,
					Score:  h.sim,
					Attr:   map[string]string{"path": c.path, "name": c.name, "kind": c.kind},
				})
				break
			}
		}
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
func (s *LexicalStore) Reindex(ctx context.Context, path string, content []byte) {
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
	emb := s.embedder()
	for _, c := range chunks {
		vecs, _ := emb.Embed(ctx, []string{c.Text})
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

// Size returns the number of indexed chunks (§11.6.2). O(1).
func (s *LexicalStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks)
}

// Delete removes the chunk with the given id (§11.6.2). A nonexistent id is a
// no-op. Used by Evict and by explicit invalidation.
func (s *LexicalStore) Delete(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.chunks[:0]
	for _, c := range s.chunks {
		if c.id != id {
			kept = append(kept, c)
		}
	}
	s.chunks = kept
}

// Evict drops the least-recently-accessed chunks until the store holds at most
// `capacity` (§11.6.3). Chunks never retrieved (zero lastAccess) are evicted
// first (oldest by insert order is the tiebreaker). A capacity <= 0 or one
// already satisfied is a no-op.
func (s *LexicalStore) Evict(capacity int) {
	if capacity <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.chunks) <= capacity {
		return
	}
	// Sort indices by (lastAccess, id) ascending — never-accessed (zero time)
	// chunks come first, then oldest-accessed. Stable on id for determinism.
	idx := make([]int, len(s.chunks))
	for i := range s.chunks {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ca, cb := s.chunks[idx[a]], s.chunks[idx[b]]
		if ca.lastAccess.Equal(cb.lastAccess) {
			return ca.id < cb.id // tiebreak: lower id (older insert) first
		}
		return ca.lastAccess.Before(cb.lastAccess)
	})
	drop := len(s.chunks) - capacity
	dropIDs := make(map[int]bool, drop)
	for i := 0; i < drop && i < len(idx); i++ {
		dropIDs[s.chunks[idx[i]].id] = true
	}
	kept := s.chunks[:0]
	for _, c := range s.chunks {
		if !dropIDs[c.id] {
			kept = append(kept, c)
		}
	}
	s.chunks = kept
}

// SetThreshold sets the minimum cosine similarity a chunk must reach to be
// returned by Retrieve (§11.6.2 θ). 0 (the default) keeps the "filter
// non-positive" behavior.
//
// Do not set this to 0.7 because §11.6.2 says 0.7. That sentence used to be in
// this comment, phrased as a recommendation, and it is a trap: θ is specified
// against a semantic embedder, and the shipped one (NewHashEmbedder — hashed
// term frequency) produces cosines on a completely different scale. Measured
// over this repo indexed at 40-line chunks, three realistic goals scored a
// best-chunk cosine of 0.46, 0.33 and 0.16. Retrieve with a budget of 10
// returned, per goal:
//
//	θ=0.0 → 10, 10, 10      θ=0.3 → 10,  2,  0
//	θ=0.1 → 10, 10, 10      θ=0.5 →  0,  0,  0
//	θ=0.2 → 10, 10,  0      θ=0.7 →  0,  0,  0
//
// So the spec's value is not a noise filter here, it is an off switch for
// retrieval — and a silent one, because an empty result is indistinguishable
// from an index that held nothing relevant. TestSpecThetaEmptiesLexicalRetrieval
// pins that so the sentence cannot come back.
//
// θ=0.2 is the more interesting warning. It looks conservative and it still
// zeroed the third goal outright while leaving the first at full budget. The
// usable range is not just narrow, it is goal-dependent: absolute cosine has no
// fixed meaning across queries on this embedder, so no single constant serves
// them all. If low-quality hits need filtering, the instrument has to be
// relative (a fraction of the top hit, say), not absolute — which is a design
// decision, not a config value, and is why nothing in cmd/yolo calls this yet.
func (s *LexicalStore) SetThreshold(theta float64) {
	s.mu.Lock()
	s.threshold = theta
	s.mu.Unlock()
}
