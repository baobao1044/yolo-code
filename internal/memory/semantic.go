// Semantic memory (Vector RAG) — retrieve code by meaning, not by grep (§11.6).
// L10-001 ships a stub: the Store exists (so Open wires it + the composition
// root's adapter has a non-nil target) but Retrieve returns nothing until the
// vector store + embedder land in L10-003/004. This keeps the aggregate's
// accessors non-nil (the L10-001 exit bar) and the import surface clean.

package memory

import "context"

// SemanticStore is the vector store for RAG. L10-001 stub: no chunks, Retrieve
// returns nil. L10-003 adds the embedder + cosine; L10-004 adds chunking +
// reindex on patch.applied.
type SemanticStore struct {
	embed Embedder // nil in L10-001; set in L10-003.
}

// NewSemanticStore returns a stub store. L10-003 takes an Embedder here.
func NewSemanticStore() *SemanticStore { return &SemanticStore{} }

// Retrieve returns the top-k chunks for a query, budget-capped (§11.6.2).
// L10-001 stub returns nil (no chunks indexed). L10-003 implements cosine.
func (s *SemanticStore) Retrieve(_ context.Context, _ string, _ int) []Part {
	return nil
}

// Reindex replaces a path's chunks (§11.7.5 — atomic). L10-001 stub is a
// no-op; L10-004 implements it (chunk → embed → atomic replace).
func (s *SemanticStore) Reindex(_ context.Context, _ string, _ []byte) {}
