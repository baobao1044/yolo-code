// Tests for L10-004 — per-function chunking + reindex on patch.applied (File 11
// §11.7.2 + §11.6.3). ChunkFile splits a Go file at function boundaries via
// go/parser (stdlib — memory may import stdlib; tree-sitter was replaced by
// go/parser project-wide); files with no functions fall back to fixed 40-line/
// 8-overlap windows. On patch.applied, the listener reindexes the touched path:
// read the new content via an FS seam, chunk, re-embed, and atomically replace
// that path's chunks (delete old + insert new in one pass). The exit bar (roadmap
// L10-004): an edited function's chunks refresh.

package memory

import (
	"context"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestChunkFileSplitsAtFunctionBoundaries: a Go file with two functions splits
// into two chunks, each carrying the full function text + the preceding comment
// as context (§11.7.2 — the docstring/signature precedes the body).
func TestChunkFileSplitsAtFunctionBoundaries(t *testing.T) {
	src := `package main

// Parse reads the input.
func Parse(s string) int { return len(s) }

// Encode writes bytes.
func Encode(b []byte) []byte { return b }
`
	chunks := ChunkFile("a.go", []byte(src))
	if len(chunks) != 2 {
		t.Fatalf("ChunkFile = %d chunks, want 2 (one per function)", len(chunks))
	}
	if chunks[0].Kind != "function" {
		t.Errorf("chunk[0].Kind = %q, want \"function\"", chunks[0].Kind)
	}
	if chunks[0].Name != "Parse" {
		t.Errorf("chunk[0].Name = %q, want \"Parse\"", chunks[0].Name)
	}
	// The preceding comment is included as context (§11.7.2).
	if !contains(chunkText(chunks[0]), "Parse reads the input") {
		t.Errorf("chunk[0] missing the doc comment: %q", chunkText(chunks[0]))
	}
	if chunks[1].Name != "Encode" {
		t.Errorf("chunk[1].Name = %q, want \"Encode\"", chunks[1].Name)
	}
}

// TestChunkFileFallsBackToWindowsForNonGo: a non-Go file (no function grammar)
// falls back to fixed windows. A short file is one window.
func TestChunkFileFallsBackToWindowsForNonGo(t *testing.T) {
	src := "line1\nline2\nline3\n"
	chunks := ChunkFile("b.md", []byte(src))
	if len(chunks) != 1 {
		t.Fatalf("ChunkFile(.md) = %d chunks, want 1 (one window for a short file)", len(chunks))
	}
	if chunks[0].Kind != "block" {
		t.Errorf("chunk[0].Kind = %q, want \"block\" (window fallback)", chunks[0].Kind)
	}
}

// TestChunkFileEmptyFileReturnsEmpty: an empty file has no chunks.
func TestChunkFileEmptyFileReturnsEmpty(t *testing.T) {
	if chunks := ChunkFile("empty.go", nil); len(chunks) != 0 {
		t.Errorf("ChunkFile(empty) = %d chunks, want 0", len(chunks))
	}
}

// TestReindexRefreshesEditedFunctionChunks: the L10-004 exit bar. Seed a file
// with one function; reindex with an edited version (the function's body
// changed); assert the stored chunk now carries the new text and the old text is
// gone (atomic replace — §11.7.5).
func TestReindexRefreshesEditedFunctionChunks(t *testing.T) {
	fs := memFS{
		"a.go": []byte("package main\n\nfunc Old() int { return 1 }\n"),
	}
	s := NewSemanticStoreWithFS(NewHashEmbedder(256), fs)
	ctx := context.Background()
	// Initial index: one chunk for Old().
	s.Reindex(ctx, "a.go", nil) // nil content → read via FS
	parts := s.Retrieve(ctx, "Old", 5)
	if len(parts) == 0 {
		t.Fatal("Retrieve('Old') = 0 after index, want the Old() chunk")
	}

	// Edit the file: rename Old() → New().
	fs["a.go"] = []byte("package main\n\nfunc New() int { return 2 }\n")
	s.Reindex(ctx, "a.go", nil)

	// The old chunk is gone; the new one is present.
	if got := s.Retrieve(ctx, "Old", 5); len(got) != 0 {
		t.Errorf("Retrieve('Old') = %d after reindex, want 0 (old chunk replaced)", len(got))
	}
	if got := s.Retrieve(ctx, "New", 5); len(got) == 0 {
		t.Error("Retrieve('New') = 0 after reindex, want the New() chunk (refresh)")
	}
}

// TestListenerReindexesOnPatchApplied: the listener's patch.applied handler
// reads the touched file via the store's FS and reindexes it. After a
// patch.applied, the new function's chunk is retrievable.
func TestListenerReindexesOnPatchApplied(t *testing.T) {
	fs := memFS{
		"a.go": []byte("package main\n\nfunc Hello() string { return \"hi\" }\n"),
	}
	emb := NewHashEmbedder(256)
	store := NewSemanticStoreWithFS(emb, fs)
	// Wire the store as the knowledge store with its FS so the listener can
	// reindex. (Test uses the real listener path: patch.applied → Reindex.)
	s, err := Open(Deps{Root: t.TempDir(), Bus: event.New()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.bus.Close(); _ = s.Close() })
	// Replace the stub knowledge store with the FS-backed one for this test.
	s.knowledge = store

	ch := s.bus.Subscribe(event.Topic("memory.update"))
	s.bus.Publish(context.Background(), &event.PatchAppliedEvent{
		Task:  "t_1",
		Files: []event.PatchFile{{Path: "a.go", Insertions: 1}},
	})
	drain(t, ch, 100*time.Millisecond)

	if got := s.Semantic().Retrieve(context.Background(), "Hello", 5); len(got) == 0 {
		t.Error("Retrieve('Hello') = 0 after patch.applied, want the Hello() chunk reindexed")
	}
}

// memFS is an in-memory FS for the semantic store's reindex seam.
type memFS map[string][]byte

func (f memFS) Read(_ context.Context, path string) ([]byte, error) {
	if b, ok := f[path]; ok {
		return b, nil
	}
	return nil, errFSNotFound{path: path}
}

type errFSNotFound struct{ path string }

func (e errFSNotFound) Error() string { return "memory: fs not found: " + e.path }

// chunkText returns a chunk's text (helper so the test reads cleanly).
func chunkText(c Chunk) string { return c.Text }

// contains reports whether s contains sub (local helper; keeps the test from
// importing strings just for one assertion).
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
