// Tests for the Knowledge insight store (§11.5.1) — Record/Retrieve/Persist/
// Load cross-session. The store keeps short lessons learned from task/verify
// events, retrievable by semantic search, distinct from the code-chunk RAG.

package memory

import (
	"context"
	"path/filepath"
	"testing"
)

// TestKnowledgeRecordAndRetrieve: an recorded insight is retrievable by a
// query that shares terms, ranked above a disjoint insight.
func TestKnowledgeRecordAndRetrieve(t *testing.T) {
	dir := t.TempDir()
	k := NewKnowledgeStore(dir, NewHashEmbedder(256))
	k.Record(context.Background(), "Race detector requires CGO on this project", "verify.fail")
	k.Record(context.Background(), "Always use conventional commits", "task.completed")

	parts := k.Retrieve(context.Background(), "race detector CGO", 5)
	if len(parts) == 0 {
		t.Fatalf("Retrieve returned nothing for a matching query")
	}
	if parts[0].Kind != KindRAG {
		t.Errorf("first hit Kind = %q, want %q (RAG group)", parts[0].Kind, KindRAG)
	}
	if parts[0].Attr["source"] != "verify.fail" {
		t.Errorf("first hit source = %q, want verify.fail", parts[0].Attr["source"])
	}
	// The "race detector" insight must outrank the "conventional commits" one
	// (it shares the query terms).
	if parts[0].Text != "Race detector requires CGO on this project" {
		t.Errorf("first hit Text = %q, want the race-detector insight", parts[0].Text)
	}
}

// TestKnowledgeRecordDedups: recording the same text twice doesn't add a
// second entry — the source is refreshed on the existing one.
func TestKnowledgeRecordDedups(t *testing.T) {
	dir := t.TempDir()
	k := NewKnowledgeStore(dir, NewHashEmbedder(256))
	k.Record(context.Background(), "go test needs CGO", "verify.fail")
	k.Record(context.Background(), "go test needs CGO", "task.completed")
	if got := len(k.All()); got != 1 {
		t.Fatalf("All() = %d items, want 1 (deduped)", got)
	}
	if k.All()[0].Source != "task.completed" {
		t.Errorf("deduped source = %q, want task.completed (refreshed)", k.All()[0].Source)
	}
}

// TestKnowledgeRetrieveEmptyReturnsNil: an empty store returns nil.
func TestKnowledgeRetrieveEmptyReturnsNil(t *testing.T) {
	k := NewKnowledgeStore(t.TempDir(), NewHashEmbedder(256))
	if parts := k.Retrieve(context.Background(), "anything", 5); parts != nil {
		t.Errorf("Retrieve on empty store = %v, want nil", parts)
	}
}

// TestKnowledgePersistLoadCrossSession: insights persist to knowledge.json and
// reload on a fresh store over the same dir (§11.3.3 cross-session recall).
func TestKnowledgePersistLoadCrossSession(t *testing.T) {
	dir := t.TempDir()
	a := NewKnowledgeStore(dir, NewHashEmbedder(256))
	a.Record(context.Background(), "This project uses Go 1.26", "task.completed")
	a.Record(context.Background(), "No generics here", "verify.fail")
	if err := a.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// A fresh store over the same dir must recall both insights.
	b := NewKnowledgeStore(dir, NewHashEmbedder(256))
	if err := b.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(b.All()); got != 2 {
		t.Fatalf("after Load, All() = %d items, want 2 (cross-session recall)", got)
	}
	// And they must be retrievable.
	parts := b.Retrieve(context.Background(), "Go project generics", 5)
	if len(parts) != 2 {
		t.Errorf("after Load, Retrieve = %d parts, want 2 (re-embedded on load)", len(parts))
	}
}

// TestKnowledgeLoadMissingFileIsNotAnError: a missing knowledge.json is "no
// insights yet", not an error.
func TestKnowledgeLoadMissingFileIsNotAnError(t *testing.T) {
	k := NewKnowledgeStore(filepath.Join(t.TempDir(), "nope"), NewHashEmbedder(256))
	if err := k.Load(context.Background()); err != nil {
		t.Errorf("Load on missing file = %v, want nil (no insights yet)", err)
	}
	if got := len(k.All()); got != 0 {
		t.Errorf("All() = %d, want 0", got)
	}
}
