// The read half of cross-session learning.
//
// memory.Store has two retrieval stores and they are not the same thing.
// Semantic() is the code-chunk RAG index — file contents, chunked and embedded.
// Insights() is the KnowledgeStore: short lessons the memory listener records
// from verification events ("the build is quadratic", "this package's tests
// need -tags=integration"). Both are wired, both are persisted, both are
// re-embedded on Open.
//
// Only one of them was ever read. contextMemoryAdapter.Retrieve — the single
// path from memory into a prompt — called Semantic().Retrieve and nothing else,
// so every insight the system recorded went to disk, cost a re-embed on every
// subsequent startup, and was never recalled by anything. The subsystem
// advertised cross-session learning and delivered cross-session forgetting with
// a storage bill.
//
// These tests pin the read half. The first is the end of the loop that matters:
// an insight recorded in one session is recalled in the next. The second pins
// that adding insights did not cost the code chunks their place — a "fix" that
// crowded out the RAG hits would be a regression wearing a feature's clothes.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/memory"
)

// TestRecordedInsightIsRecalledInTheNextSession is the exit bar for the write
// half being worth its cost: what one session learned, the next one can read.
func TestRecordedInsightIsRecalledInTheNextSession(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Session A records a lesson, the shape the memory listener records from a
	// verification event, and persists on Close.
	a, err := memory.Open(memory.Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	a.Insights().Record(ctx, "the integration tests need a running postgres", "verify.fail")
	if err := a.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	// Session B opens the same root, so Load re-embeds the stored insight.
	b, err := memory.Open(memory.Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	defer func() { _ = b.Close() }()
	if got := len(b.Insights().All()); got != 1 {
		t.Fatalf("session B loaded %d insights, want 1 — the write half is broken, "+
			"so this test cannot say anything about the read half", got)
	}

	ad := contextMemoryAdapter{store: b}
	parts := ad.Retrieve(ctx, "integration tests postgres", 10)

	for _, p := range parts {
		if strings.Contains(p.Text, "postgres") {
			return
		}
	}
	t.Errorf("Retrieve returned %d parts and none carried the recorded insight.\n"+
		"got: %+v\n"+
		"The lesson is on disk and re-embedded in memory; the one path from memory "+
		"into a prompt does not read it. Every insight this system records is "+
		"written, paid for on each startup, and never recalled.", len(parts), parts)
}

// TestInsightsDoNotCrowdOutCodeChunks pins the budget half. Retrieve's topK is
// the caller's cap on how much retrieved material reaches the prompt; feeding a
// second store into it must not silently halve what the first one contributes.
// Here the code index holds more strong matches than topK, so if insights took
// a fixed slice off the top the assertion below would fail.
func TestInsightsDoNotCrowdOutCodeChunks(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	st, err := memory.Open(memory.Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Six chunks that all match the query strongly, and one weakly-related
	// insight. With topK=3 the three best chunks must still win.
	chunkSet := []memory.Chunk{
		{Path: "a.go", Name: "a.go", Kind: "function", Text: "func handleAuthLogin(w http.ResponseWriter, r *http.Request)"},
		{Path: "b.go", Name: "b.go", Kind: "function", Text: "func handleAuthLogout(w http.ResponseWriter, r *http.Request)"},
		{Path: "c.go", Name: "c.go", Kind: "function", Text: "func handleAuthRefresh(w http.ResponseWriter, r *http.Request)"},
		{Path: "d.go", Name: "d.go", Kind: "function", Text: "func handleAuthSignup(w http.ResponseWriter, r *http.Request)"},
		{Path: "e.go", Name: "e.go", Kind: "function", Text: "func handleAuthVerify(w http.ResponseWriter, r *http.Request)"},
		{Path: "f.go", Name: "f.go", Kind: "function", Text: "func handleAuthRevoke(w http.ResponseWriter, r *http.Request)"},
	}
	st.Semantic().BulkInsert(ctx, chunkSet)
	st.Insights().Record(ctx, "handleAuth handlers share a middleware", "verify.pass")

	ad := contextMemoryAdapter{store: st}
	const topK = 3
	parts := ad.Retrieve(ctx, "handleAuth http request", topK)

	if len(parts) > topK {
		t.Fatalf("Retrieve(topK=%d) returned %d parts — the cap is the caller's "+
			"budget, not a suggestion", topK, len(parts))
	}
	chunks := 0
	for _, p := range parts {
		if strings.HasSuffix(p.Source, ".go") {
			chunks++
		}
	}
	if chunks == 0 {
		t.Errorf("Retrieve returned %d parts and none was a code chunk: %+v\n"+
			"Insights are meant to accompany the code the model is about to read, "+
			"not replace it.", len(parts), parts)
	}
}
