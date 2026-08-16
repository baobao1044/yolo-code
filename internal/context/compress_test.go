package context

import (
	"strings"
	"testing"
	"time"
)

// TestCompressDeduplicatesSameFileKeepsHighestScored pins pass 1: two parts
// over the same file source collapse to one, and the survivor is the
// higher-scored one (which rank() places first, so it's the first occurrence).
func TestCompressDeduplicatesSameFileKeepsHighestScored(t *testing.T) {
	eng := New(Deps{SoftBudget: 1 << 20}) // generous budget → only dedup matters
	ranked := []Part{
		{Kind: KindFile, Source: "auth/login.go", Text: "package auth // best copy", Score: 0.9},
		{Kind: KindFile, Source: "auth/login.go", Text: "package auth // stale copy", Score: 0.1},
	}
	pkg := eng.compress(ranked)

	if len(pkg.Files) != 1 {
		t.Fatalf("pkg.Files len = %d, want 1 (dedup must collapse same-source file parts)", len(pkg.Files))
	}
	if pkg.Files[0].Text != "package auth // best copy" {
		t.Errorf("survivor text = %q, want the higher-scored (first) copy", pkg.Files[0].Text)
	}
}

// TestCompressDedupIsScopedByKind pins that the dedup key is kind-scoped: a
// KindFile and a KindConversation sharing a source label do NOT collapse,
// because they mean different things.
func TestCompressDedupIsScopedByKind(t *testing.T) {
	eng := New(Deps{SoftBudget: 1 << 20})
	ranked := []Part{
		{Kind: KindFile, Source: "auth/login.go", Text: "file body", Score: 0.5},
		{Kind: KindConversation, Source: "auth/login.go", Text: "we edited login", Score: 0.5},
	}
	pkg := eng.compress(ranked)

	if len(pkg.Files) != 1 {
		t.Errorf("pkg.Files len = %d, want 1", len(pkg.Files))
	}
	if len(pkg.Conversation) != 1 {
		t.Errorf("pkg.Conversation len = %d, want 1 (kind-scoped dedup must not cross kinds)", len(pkg.Conversation))
	}
}

// TestCompressDropsLowestScoredToMeetSoftBudget pins pass 3 (top-K soft
// budget): with four 10-byte parts and a 25-byte budget, only the two
// highest-scored survive and the kept total is under the budget. This is the
// "over-budget compresses under" guarantee.
func TestCompressDropsLowestScoredToMeetSoftBudget(t *testing.T) {
	eng := New(Deps{SoftBudget: 25})
	// Each part is exactly 10 bytes of text; ranked is score-sorted descending.
	ranked := []Part{
		{Kind: KindFile, Source: "a.go", Text: "0123456789", Score: 0.9},
		{Kind: KindFile, Source: "b.go", Text: "0123456789", Score: 0.8},
		{Kind: KindFile, Source: "c.go", Text: "0123456789", Score: 0.7},
		{Kind: KindFile, Source: "d.go", Text: "0123456789", Score: 0.6},
	}
	pkg := eng.compress(ranked)

	if len(pkg.Files) != 2 {
		t.Fatalf("pkg.Files len = %d, want 2 (budget 25 fits two 10-byte parts; 40 total must trim)", len(pkg.Files))
	}
	// The survivors must be the two highest-scored (ranked is descending, so a,b).
	got := []string{pkg.Files[0].Source, pkg.Files[1].Source}
	if got[0] != "a.go" || got[1] != "b.go" {
		t.Errorf("survivors = %v, want [a.go b.go] (highest-scored kept first)", got)
	}
	// And the kept byte total must be under the soft budget.
	total := 0
	for _, p := range pkg.Files {
		total += len(p.Text)
	}
	if total > 25 {
		t.Errorf("kept total = %d, want <= softBudget 25 (over-budget must compress under)", total)
	}
}

// TestCompressAdmitsFirstPartEvenIfOverBudget pins the pass-3 edge: a single
// part larger than the budget is still admitted (bytes==0 guard) so compress
// never returns an empty package when there is input. Subsequent parts are
// dropped.
func TestCompressAdmitsFirstPartEvenIfOverBudget(t *testing.T) {
	eng := New(Deps{SoftBudget: 10})
	ranked := []Part{
		{Kind: KindFile, Source: "big.go", Text: "0123456789ABCDEF", Score: 0.9}, // 16 bytes > 10
		{Kind: KindFile, Source: "small.go", Text: "xy", Score: 0.1},             // 2 bytes
	}
	pkg := eng.compress(ranked)

	if len(pkg.Files) != 1 {
		t.Fatalf("pkg.Files len = %d, want 1 (over-budget first part admitted alone, rest dropped)", len(pkg.Files))
	}
	if pkg.Files[0].Source != "big.go" {
		t.Errorf("survivor = %q, want big.go (first/highest part)", pkg.Files[0].Source)
	}
}

// TestCompressGroupsByKind pins assign(): parts land in their group slots,
// not a flat list, so the Prompt Compiler can order groups deterministically.
func TestCompressGroupsByKind(t *testing.T) {
	eng := New(Deps{SoftBudget: 1 << 20})
	ranked := []Part{
		{Kind: KindSystem, Source: "<system>", Text: "role", Score: 0.4},
		{Kind: KindProject, Source: "AGENTS.md", Text: "rules", Score: 0.3},
		{Kind: KindConversation, Source: "turn#1", Text: "hi", Score: 0.2},
		{Kind: KindFile, Source: "a.go", Text: "body", Score: 0.5},
		{Kind: KindGraph, Source: "sym", Text: "Sym()", Score: 0.1},
		{Kind: KindDiagnostics, Source: "diag", Text: "err", Score: 0.1},
		{Kind: KindPreferences, Source: "pref", Text: "pref", Score: 0.1},
	}
	pkg := eng.compress(ranked)

	if len(pkg.System) != 1 {
		t.Errorf("pkg.System len = %d, want 1", len(pkg.System))
	}
	if len(pkg.Project) != 1 {
		t.Errorf("pkg.Project len = %d, want 1", len(pkg.Project))
	}
	if len(pkg.Conversation) != 1 {
		t.Errorf("pkg.Conversation len = %d, want 1", len(pkg.Conversation))
	}
	if len(pkg.Files) != 1 {
		t.Errorf("pkg.Files len = %d, want 1", len(pkg.Files))
	}
	if len(pkg.Graph) != 1 {
		t.Errorf("pkg.Graph len = %d, want 1", len(pkg.Graph))
	}
	if len(pkg.Diagnostics) != 1 {
		t.Errorf("pkg.Diagnostics len = %d, want 1", len(pkg.Diagnostics))
	}
	if len(pkg.Preferences) != 1 {
		t.Errorf("pkg.Preferences len = %d, want 1", len(pkg.Preferences))
	}
}

// TestCompressPreservesScoreOrderWithinGroup pins that pass 3 keeps the ranked
// (descending score) order, so within each group the highest-scored part is
// first — the order the compiler emits.
func TestCompressPreservesScoreOrderWithinGroup(t *testing.T) {
	eng := New(Deps{SoftBudget: 1 << 20})
	ranked := []Part{
		{Kind: KindFile, Source: "high.go", Text: "h", Score: 0.9},
		{Kind: KindFile, Source: "mid.go", Text: "m", Score: 0.5},
		{Kind: KindFile, Source: "low.go", Text: "l", Score: 0.1},
	}
	pkg := eng.compress(ranked)

	if len(pkg.Files) != 3 {
		t.Fatalf("pkg.Files len = %d, want 3", len(pkg.Files))
	}
	if pkg.Files[0].Source != "high.go" || pkg.Files[1].Source != "mid.go" || pkg.Files[2].Source != "low.go" {
		t.Errorf("group order = %v, want [high.go mid.go low.go] (descending score preserved)", []string{pkg.Files[0].Source, pkg.Files[1].Source, pkg.Files[2].Source})
	}
}

// TestCompressKeepsASecondChunkOverASecondStaleFile is the selection-level
// consequence of the cosine rescale — the half that matters, because a score
// that moves while the evicted set stays identical has fixed nothing.
//
// Three open files, freshly written, 400 bytes of "z" each: no goal token, no
// directory relation, 0.30 apiece on recency. Two retrieved chunks at cosines
// the real embedder produces. The soft budget fits one head from each Kind plus
// exactly one more part, so the ranking decides which.
//
// Before: files at 0.30 took 1st/2nd/3rd, so the spare slot went to a second
// content-free file and the second chunk was evicted. After: the normalised
// chunks take 1st/2nd and the spare slot goes to the second chunk.
func TestCompressKeepsASecondChunkOverASecondStaleFile(t *testing.T) {
	eng := New(Deps{SoftBudget: 900})
	req := rankReq("fix the Login function so bad passwords are rejected")
	now := time.Now()
	noise := strings.Repeat("z", 400)
	chunk := strings.Repeat("q", 100)
	parts := []Part{
		{Kind: KindFile, Source: "docs/a.md", Text: noise, Recency: now},
		{Kind: KindFile, Source: "docs/b.md", Text: noise, Recency: now},
		{Kind: KindFile, Source: "docs/c.md", Text: noise, Recency: now},
		{Kind: KindRAG, Source: "auth/login.go#1", Score: 0.1529, Text: chunk},
		{Kind: KindRAG, Source: "auth/login.go#2", Score: 0.1400, Text: chunk},
	}

	pkg := eng.compress(eng.rank(parts, req))
	if len(pkg.RAG) != 2 || len(pkg.Files) != 1 {
		got := make([]string, 0, len(pkg.Files)+len(pkg.RAG))
		for _, p := range pkg.Files {
			got = append(got, "file "+p.Source)
		}
		for _, p := range pkg.RAG {
			got = append(got, "rag "+p.Source)
		}
		t.Errorf("kept %v; want both retrieved chunks and one file. The last slot in the "+
			"budget must go to the second chunk from the store, not to a third file of "+
			"literal \"z\" that happens to be recent", got)
	}
}

// TestCompressNeverEvictsTheSystemFrame: at SoftBudget 900 the <system> part —
// 1505 bytes, ranked 0.05 because a role description shares almost no tokens
// with any particular goal — was dropped in full, while a 500-byte file was
// kept. The system prompt is what tells the model what it is, what the rules
// are and what tools exist; a run without it is a different agent, not a
// degraded one. §6.7.3 puts it in the never-trimmed set and Layer 5 honours
// that, but Layer 4 was evicting it before Layer 5 ever saw the package.
func TestCompressNeverEvictsTheSystemFrame(t *testing.T) {
	eng := New(Deps{SoftBudget: 900})
	ranked := []Part{ // already in score order, as rank() would leave them
		{Kind: KindFile, Source: "docs/a.md", Text: strings.Repeat("z", 500), Score: 0.30},
		{Kind: KindFile, Source: "docs/b.md", Text: strings.Repeat("z", 500), Score: 0.30},
		{Kind: KindSystem, Source: "<system>", Text: strings.Repeat("s", 1505), Score: 0.05},
	}
	pkg := eng.compress(ranked)
	if len(pkg.System) != 1 {
		t.Errorf("pkg.System = %d parts, want 1: the system frame outranks the byte budget", len(pkg.System))
	}
	// The exemption must not be paid for by everything else: the frame's bytes
	// are not charged, so the gathered context still gets the whole budget.
	if len(pkg.Files) == 0 {
		t.Error("pkg.Files empty: exempting the system frame must not spend the budget " +
			"the soft budget exists to allocate to gathered repository state")
	}
}

// TestCompressExemptsExactlyOneSystemPart bounds the exemption, which is the
// argument against the option not taken. A blanket Kind-level exemption would
// make <system> the one label under which unbounded text escapes every budget,
// and "the system prompt is protected" would come to mean "anything tagged
// system is protected" — a protection that has become a hole.
//
// So: the highest-ranked system part (the frame gather() authors) is exempt and
// uncharged; a second one competes like any other part and loses here, because
// 1000 bytes do not fit in what is left of 500. The file is the tell that the
// frame was not charged — before the exemption the frame consumed the budget as
// the first part admitted and the file was evicted instead.
func TestCompressExemptsExactlyOneSystemPart(t *testing.T) {
	eng := New(Deps{SoftBudget: 500})
	ranked := []Part{
		{Kind: KindSystem, Source: "<system>", Text: strings.Repeat("s", 1000), Score: 0.05},
		{Kind: KindFile, Source: "docs/a.md", Text: strings.Repeat("z", 100), Score: 0.04},
		{Kind: KindSystem, Source: "<injected>", Text: strings.Repeat("x", 1000), Score: 0.03},
	}
	pkg := eng.compress(ranked)
	if len(pkg.System) != 1 {
		t.Errorf("pkg.System = %d parts, want exactly 1: only the frame is exempt, a second "+
			"system part is budgeted like anything else", len(pkg.System))
	}
	if len(pkg.System) > 0 && pkg.System[0].Source != "<system>" {
		t.Errorf("pkg.System[0] = %q, want the highest-ranked system part", pkg.System[0].Source)
	}
	if len(pkg.Files) != 1 {
		t.Errorf("pkg.Files = %d parts, want 1: the exempt frame's 1000 bytes must not be "+
			"charged against the 500-byte budget the file needs 100 of", len(pkg.Files))
	}
}
