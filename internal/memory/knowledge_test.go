// Tests for the Knowledge insight store (§11.5.1) — Record/Retrieve/Persist/
// Load cross-session. The store keeps short lessons learned from task/verify
// events, retrievable by semantic search, distinct from the code-chunk RAG.

package memory

import (
	"context"
	"path/filepath"
	"strconv"
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

// TestKnowledgeRecordIsCapped: the store is bounded at maxInsights. Without a
// cap, knowledge.json grows monotonically for the life of the project and Open
// re-embeds every entry, so the boot cost grows with it.
func TestKnowledgeRecordIsCapped(t *testing.T) {
	k := NewKnowledgeStore(t.TempDir(), NewHashEmbedder(64))
	for i := 0; i < maxInsights+50; i++ {
		k.Record(context.Background(), "lesson number "+strconv.Itoa(i), "verify.fail")
	}
	if got := len(k.All()); got != maxInsights {
		t.Fatalf("All() = %d insights after %d Records, want the cap %d", got, maxInsights+50, maxInsights)
	}
	// LRU with no retrievals ⇒ oldest-first: the survivors are the newest 512.
	if first := k.All()[0].Text; first != "lesson number 50" {
		t.Errorf("oldest survivor = %q, want \"lesson number 50\" (the first 50 evicted)", first)
	}
}

// TestKnowledgeEvictionKeepsRecentlyUsed: an insight the retrieval path has
// returned (LastUsed bumped) outlives newer never-recalled ones. That is the
// point of LRU over plain FIFO — the lesson the agent actually recalls is the
// one worth its disk and its startup embedding.
func TestKnowledgeEvictionKeepsRecentlyUsed(t *testing.T) {
	k := NewKnowledgeStore(t.TempDir(), NewHashEmbedder(64))
	k.Record(context.Background(), "zzzq unique cgo race token", "verify.fail")
	// Recall it, so LastUsed is set.
	if parts := k.Retrieve(context.Background(), "zzzq unique cgo race token", 1); len(parts) != 1 {
		t.Fatalf("Retrieve = %d parts, want 1 (seeded insight)", len(parts))
	}
	for i := 0; i < maxInsights+10; i++ {
		k.Record(context.Background(), "filler lesson "+strconv.Itoa(i), "verify.fail")
	}
	if got := len(k.All()); got != maxInsights {
		t.Fatalf("All() = %d, want the cap %d", got, maxInsights)
	}
	for _, it := range k.All() {
		if it.Text == "zzzq unique cgo race token" {
			return
		}
	}
	t.Error("the recalled insight was evicted while never-recalled fillers survived — eviction is not LRU")
}

// TestKnowledgeLoadTrimsAnOversizedFile: a knowledge.json written before the
// cap existed must be trimmed on Load, BEFORE the re-embed — otherwise every
// Open keeps paying for the historical bloat.
func TestKnowledgeLoadTrimsAnOversizedFile(t *testing.T) {
	dir := t.TempDir()
	oversized := make([]Insight, 0, maxInsights+40)
	for i := 0; i < maxInsights+40; i++ {
		oversized = append(oversized, Insight{Text: "old lesson " + strconv.Itoa(i), Source: "verify.fail", Seq: i + 1})
	}
	if err := writeJSON(filepath.Join(dir, "knowledge.json"), oversized); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	k := NewKnowledgeStore(dir, NewHashEmbedder(64))
	if err := k.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(k.All()); got != maxInsights {
		t.Fatalf("after Load, All() = %d, want the cap %d", got, maxInsights)
	}
	// The trimmed set must round-trip: Persist writes the capped file back.
	if err := k.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	var back []Insight
	if err := readJSON(filepath.Join(dir, "knowledge.json"), &back); err != nil {
		t.Fatalf("readJSON: %v", err)
	}
	if len(back) != maxInsights {
		t.Errorf("knowledge.json holds %d insights after Persist, want the cap %d", len(back), maxInsights)
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
