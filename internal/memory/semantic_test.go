// Tests for L10-003 — the pure-Go vector store (File 11 §11.6). The store
// embeds chunks via an Embedder, retrieves the top-k by cosine similarity, and
// returns them budget-capped as Parts. The MVP embedder is a deterministic
// local stub (hash-based term-frequency) so the store is offline-testable with
// zero deps; a real OpenAI/Ollama embedder plugs behind the same interface later
// (§11.7.4). The exit bar (roadmap L10-003): nearest-k query returns seeded
// docs in order.

package memory

import (
	"context"
	"testing"
)

// TestHashEmbedderIsDeterministic: the same text embeds to the same vector
// across calls (S5 byte-determinism); different texts embed to different
// vectors; an empty input returns an empty slice.
func TestHashEmbedderIsDeterministic(t *testing.T) {
	emb := NewHashEmbedder(256)
	a, _ := emb.Embed(context.Background(), []string{"hello world"})
	b, _ := emb.Embed(context.Background(), []string{"hello world"})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("Embed len = %d/%d, want 1/1", len(a), len(b))
	}
	if !vecEq(a[0], b[0]) {
		t.Error("same text embeds to different vectors (not deterministic)")
	}
	c, _ := emb.Embed(context.Background(), []string{"different words"})
	if vecEq(a[0], c[0]) {
		t.Error("different texts embed to the same vector (no discrimination)")
	}
	empty, _ := emb.Embed(context.Background(), nil)
	if len(empty) != 0 {
		t.Errorf("Embed(nil) = %d vectors, want 0", len(empty))
	}
}

// TestCosineSimilarity: identical vectors = 1; orthogonal vectors = 0;
// a vector with itself scaled is still 1 (cosine is direction, not magnitude).
func TestCosineSimilarity(t *testing.T) {
	if got := cosine([]float32{1, 0}, []float32{1, 0}); got != 1 {
		t.Errorf("cosine(identical) = %v, want 1", got)
	}
	if got := cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("cosine(orthogonal) = %v, want 0", got)
	}
	if got := cosine([]float32{1, 0}, []float32{2, 0}); got != 1 {
		t.Errorf("cosine(scaled) = %v, want 1 (direction, not magnitude)", got)
	}
	if got := cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("cosine(zero) = %v, want 0 (no division by zero)", got)
	}
}

// TestSemanticRetrieveReturnsSeededDocsInOrder: the L10-003 exit bar. Seed
// chunks, query, and assert the nearest-k come back in cosine order — the
// most similar first. The hash embedder makes "function" queries match the
// seeded chunk with shared terms more strongly than a disjoint chunk.
func TestSemanticRetrieveReturnsSeededDocsInOrder(t *testing.T) {
	emb := NewHashEmbedder(512)
	s := NewSemanticStoreWith(emb)

	// Seed three chunks with distinct term profiles.
	s.addChunk(context.Background(), chunkVec{path: "a.go", kind: "function", name: "Parse", text: "func Parse the input stream"})
	s.addChunk(context.Background(), chunkVec{path: "b.go", kind: "function", name: "Encode", text: "func Encode bytes to wire"})
	s.addChunk(context.Background(), chunkVec{path: "c.go", kind: "function", name: "ParseConfig", text: "func Parse the config file"})

	// Query shares terms with a.go and c.go (both "Parse ...") but not b.go.
	// So the top-2 should be a.go + c.go (in some order), NOT b.go.
	parts := s.Retrieve(context.Background(), "Parse the input", 5)
	if len(parts) < 2 {
		t.Fatalf("Retrieve = %d parts, want >=2", len(parts))
	}
	// b.go (Encode) must NOT rank above the Parse chunks.
	sources := make([]string, len(parts))
	for i, p := range parts {
		sources[i] = p.Attr["path"]
	}
	for i, src := range sources {
		if src == "b.go" && i < len(sources)-1 {
			t.Errorf("b.go ranked %d (above a Parse chunk) — cosine order broken: %v", i, sources)
		}
	}
	// The two "Parse" chunks (a.go, c.go) are the top-2.
	top2 := map[string]bool{sources[0]: true, sources[1]: true}
	if !top2["a.go"] || !top2["c.go"] {
		t.Errorf("top-2 = %v, want a.go + c.go (both share \"Parse\" with the query)", top2)
	}
}

// TestSemanticRetrieveBudgetCapsParts: a small budget returns fewer parts
// (§11.6.2 budget-capped retrieval). All seeded chunks share terms with the
// query (so all exceed sim>0 and the cap, not the sim<=0 break, is the binding
// constraint). Budget is in Parts, not tokens for L10-003 (the token-aware
// cap is a later refinement).
func TestSemanticRetrieveBudgetCapsParts(t *testing.T) {
	emb := NewHashEmbedder(256)
	s := NewSemanticStoreWith(emb)
	// All three chunks share "alpha" with the query → all sim>0.
	s.addChunk(context.Background(), chunkVec{path: "a.go", text: "alpha one"})
	s.addChunk(context.Background(), chunkVec{path: "b.go", text: "alpha two"})
	s.addChunk(context.Background(), chunkVec{path: "c.go", text: "alpha three"})

	parts := s.Retrieve(context.Background(), "alpha", 2)
	if len(parts) != 2 {
		t.Errorf("Retrieve budget=2 returned %d parts, want 2 (cap is binding — all chunks share 'alpha')", len(parts))
	}
	// A larger budget returns all 3.
	all := s.Retrieve(context.Background(), "alpha", 5)
	if len(all) != 3 {
		t.Errorf("Retrieve budget=5 returned %d parts, want 3", len(all))
	}
}

// TestSemanticRetrieveEmptyStoreReturnsNil: an empty store retrieves nothing.
func TestSemanticRetrieveEmptyStoreReturnsNil(t *testing.T) {
	s := NewSemanticStoreWith(NewHashEmbedder(256))
	if parts := s.Retrieve(context.Background(), "anything", 5); parts != nil {
		t.Errorf("Retrieve on empty store = %v, want nil", parts)
	}
}

// TestSemanticSizeReflectsInsertedChunks: Size() tracks addChunk/BulkInsert/
// Delete (§11.6.2).
func TestSemanticSizeReflectsInsertedChunks(t *testing.T) {
	s := NewSemanticStoreWith(NewHashEmbedder(256))
	if got := s.Size(); got != 0 {
		t.Errorf("Size on empty = %d, want 0", got)
	}
	s.addChunk(context.Background(), chunkVec{path: "a.go", text: "alpha"})
	s.addChunk(context.Background(), chunkVec{path: "b.go", text: "beta"})
	if got := s.Size(); got != 2 {
		t.Errorf("Size after 2 addChunk = %d, want 2", got)
	}
}

// TestSemanticDeleteRemovesChunk: Delete(id) drops one chunk by id (§11.6.2);
// a nonexistent id is a no-op.
func TestSemanticDeleteRemovesChunk(t *testing.T) {
	s := NewSemanticStoreWith(NewHashEmbedder(256))
	s.addChunk(context.Background(), chunkVec{path: "a.go", text: "alpha one"})
	s.addChunk(context.Background(), chunkVec{path: "b.go", text: "beta two"})
	before := s.Size()
	// Snapshot ids: the last appended chunk has the highest id.
	s.mu.RLock()
	lastID := s.chunks[len(s.chunks)-1].id
	s.mu.RUnlock()
	s.Delete(lastID)
	if got := s.Size(); got != before-1 {
		t.Errorf("Size after Delete = %d, want %d", got, before-1)
	}
	// Deleting a nonexistent id doesn't shrink further.
	s.Delete(999999)
	if got := s.Size(); got != before-1 {
		t.Errorf("Size after bogus Delete = %d, want %d (no-op)", got, before-1)
	}
}

// TestSemanticEvictDropsLeastRecentlyUsed: Evict(capacity) keeps the most-
// recently-accessed chunks and drops the LRU ones (§11.6.3). Never-accessed
// chunks (zero lastAccess) are evicted first.
func TestSemanticEvictDropsLeastRecentlyUsed(t *testing.T) {
	emb := NewHashEmbedder(256)
	s := NewSemanticStoreWith(emb)
	// Three chunks; "alpha" shares a term with a retrieval so its lastAccess
	// gets bumped; the other two stay never-accessed.
	s.addChunk(context.Background(), chunkVec{path: "a.go", text: "alpha shared"})
	s.addChunk(context.Background(), chunkVec{path: "b.go", text: "beta disjoint"})
	s.addChunk(context.Background(), chunkVec{path: "c.go", text: "gamma disjoint"})
	_ = s.Retrieve(context.Background(), "alpha", 5) // bumps a.go's lastAccess
	s.Evict(1)
	if s.Size() != 1 {
		t.Fatalf("Size after Evict(1) = %d, want 1", s.Size())
	}
	parts := s.Retrieve(context.Background(), "alpha", 5)
	if len(parts) == 0 || parts[0].Source != "a.go" {
		t.Errorf("after Evict(1) the survivor = %v, want a.go (just accessed)", parts)
	}
}

// TestSemanticEvictBelowCapacityIsNoop: Evict with a capacity already
// satisfied doesn't drop anything.
func TestSemanticEvictBelowCapacityIsNoop(t *testing.T) {
	s := NewSemanticStoreWith(NewHashEmbedder(256))
	s.addChunk(context.Background(), chunkVec{path: "a.go", text: "alpha"})
	s.addChunk(context.Background(), chunkVec{path: "b.go", text: "beta"})
	s.Evict(5)
	if got := s.Size(); got != 2 {
		t.Errorf("Size after Evict(5) = %d, want 2 (no-op)", got)
	}
}

// TestSemanticThresholdFiltersLowSimilarity: SetThreshold(θ) drops chunks
// whose cosine is below θ (§11.6.2). With a high θ, a weak match is filtered.
func TestSemanticThresholdFiltersLowSimilarity(t *testing.T) {
	emb := NewHashEmbedder(256)
	s := NewSemanticStoreWith(emb)
	s.addChunk(context.Background(), chunkVec{path: "a.go", text: "alpha alpha alpha"})
	s.addChunk(context.Background(), chunkVec{path: "b.go", text: "completely different tokens"})

	// Without a threshold, the strong "alpha" match returns.
	noThresh := s.Retrieve(context.Background(), "alpha", 5)
	if len(noThresh) == 0 {
		t.Fatal("Retrieve without threshold returned nothing for a strong match")
	}
	// With a high threshold, only the strong match survives; the disjoint one
	// is filtered (its sim is 0).
	s.SetThreshold(0.5)
	withThresh := s.Retrieve(context.Background(), "alpha", 5)
	for _, p := range withThresh {
		if p.Score < 0.5 {
			t.Errorf("threshold let through a chunk with score %v < 0.5: %+v", p.Score, p)
		}
	}
}

// vecEq reports whether two float32 slices are element-wise equal.
func vecEq(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
