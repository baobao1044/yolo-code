package context

import (
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/session"
)

// rankReq is a minimal ContextRequest for unit-testing rank() in isolation —
// no disk, no store, just a task with a goal. rank() only reads req.Task.Goal
// (proximity/semantic) and checks req.Task != nil; it never touches Session.
func rankReq(goal string) ContextRequest {
	return ContextRequest{Task: &session.Task{ID: "t1", Goal: goal}}
}

// TestRankKnownRelevantFileRanksFirst asserts the §6.2.2 blend orders the
// known-relevant open file above unrelated noise. The relevant file is the one
// the goal @-references, so recency (recent), proximity (same dir), semantic
// (shared "login" token), and explicit (the @-ref) all fire for it and stay
// near zero for noise files in other directories.
func TestRankKnownRelevantFileRanksFirst(t *testing.T) {
	eng := New(Deps{}) // rank uses no collaborators
	now := time.Now()
	recent := now.Add(-1 * time.Minute)
	old := now.Add(-48 * time.Hour) // beyond the 24h decay → recency 0

	goal := "fix the Login function in @auth/login.go"
	req := rankReq(goal)
	parts := []Part{
		{Kind: KindFile, Source: "util/helper.go", Text: "package util\nfunc Help() {}", Recency: old},
		{Kind: KindFile, Source: "main.go", Text: "package main\nfunc main() {}", Recency: old},
		{Kind: KindFile, Source: "auth/login.go", Text: "package auth\nfunc Login(user string) error { return nil }", Recency: recent},
	}
	markExplicit(parts, goal) // same wiring gather() applies before rank()

	ranked := eng.rank(parts, req)

	if ranked[0].Source != "auth/login.go" {
		t.Fatalf("rank[0] = %q, want auth/login.go (the @-referenced relevant file)", ranked[0].Source)
	}
	// The relevant file must strictly outrank every noise file.
	top := ranked[0].Score
	for _, p := range ranked[1:] {
		if p.Score >= top {
			t.Errorf("noise file %q score %.4f not strictly below relevant file %.4f", p.Source, p.Score, top)
		}
	}
	// And the relevant file must actually have a positive score (signals fired).
	if top <= 0 {
		t.Errorf("relevant file score %.4f <= 0; recency/proximity/explicit signals should fire", top)
	}
}

// TestRankExplicitReferenceBeatsSameDirSibling asserts the explicit @-reference
// signal breaks an otherwise-close tie: two recent files in the goal's
// directory, only one @-referenced. The @-referenced one ranks first.
func TestRankExplicitReferenceBeatsSameDirSibling(t *testing.T) {
	eng := New(Deps{})
	recent := time.Now().Add(-1 * time.Minute)
	goal := "edit @auth/login.go"
	req := rankReq(goal)
	parts := []Part{
		{Kind: KindFile, Source: "auth/login_test.go", Text: "package auth\nfunc TestLogin(t *testing.T){}", Recency: recent},
		{Kind: KindFile, Source: "auth/login.go", Text: "package auth\nfunc Login() error { return nil }", Recency: recent},
	}
	markExplicit(parts, goal)

	ranked := eng.rank(parts, req)

	if ranked[0].Source != "auth/login.go" {
		t.Fatalf("rank[0] = %q, want auth/login.go (explicit @-ref must outrank same-dir sibling)", ranked[0].Source)
	}
	if ranked[0].Score <= ranked[1].Score {
		t.Errorf("explicit file score %.4f not above sibling %.4f", ranked[0].Score, ranked[1].Score)
	}
}

// TestRankIsDeterministicForTies asserts rank is stable: equal-scored parts
// keep their input order (sort.SliceStable), and two calls over identical
// inputs produce identical ordering (S5 §5.5 determinism). All parts here have
// zero recency (zero time), no keyword overlap, no @-ref, and a goal that names
// no file at all, so proximity abstains — they tie at exactly 0 and stable sort
// preserves insertion order.
//
// The sources are multi-segment repo paths on purpose. This test used to use
// bare filenames (a.go, b.go, c.go) and claim "no goal-file proximity"; that
// was true only by accident, because dirOf("a.go") is "" and the old proximity
// bailed out on an empty directory. Any real repo path has a directory, and for
// those the old code scored a constant 1 against this same goal. Bare filenames
// hid the defect this whole test file is meant to guard.
func TestRankIsDeterministicForTies(t *testing.T) {
	eng := New(Deps{})
	req := rankReq("do something unrelated")
	parts := []Part{
		{Kind: KindFile, Source: "internal/auth/a.go", Text: "x", Recency: time.Time{}},
		{Kind: KindFile, Source: "internal/store/b.go", Text: "y", Recency: time.Time{}},
		{Kind: KindFile, Source: "cmd/yolo/c.go", Text: "z", Recency: time.Time{}},
	}
	first := eng.rank(parts, req)
	second := eng.rank(parts, req)

	want := []string{"internal/auth/a.go", "internal/store/b.go", "cmd/yolo/c.go"}
	for i, w := range want {
		if first[i].Source != w {
			t.Errorf("tie order[%d] = %q, want %q (stable input order)", i, first[i].Source, w)
		}
		if first[i].Source != second[i].Source {
			t.Fatalf("rank not deterministic at %d: %q vs %q", i, first[i].Source, second[i].Source)
		}
		if first[i].Score != 0 {
			t.Errorf("tie score[%d] = %.4f, want exactly 0", i, first[i].Score)
		}
	}
}

// TestRankRecencyDecaysWithin24h pins the recency curve: a file touched 1h ago
// scores higher than one touched 12h ago, which scores higher than one beyond
// the 24h window (recency 0). This guards the 0.30 recency weight and the
// 24h decay boundary in rank.go.
func TestRankRecencyDecaysWithin24h(t *testing.T) {
	eng := New(Deps{})
	now := time.Now()
	parts := []Part{
		{Kind: KindFile, Source: "old.go", Text: "package main", Recency: now.Add(-48 * time.Hour)},
		{Kind: KindFile, Source: "mid.go", Text: "package main", Recency: now.Add(-12 * time.Hour)},
		{Kind: KindFile, Source: "new.go", Text: "package main", Recency: now.Add(-1 * time.Hour)},
	}
	req := rankReq("goal") // no @-ref, no shared dir/token → only recency differs

	ranked := eng.rank(parts, req)

	// Order must be new > mid > old, and old must be exactly 0 (beyond 24h).
	if got := []string{ranked[0].Source, ranked[1].Source, ranked[2].Source}; got[0] != "new.go" || got[1] != "mid.go" || got[2] != "old.go" {
		t.Fatalf("recency order = %v, want [new.go mid.go old.go]", got)
	}
	var oldScore float64
	for _, p := range ranked {
		if p.Source == "old.go" {
			oldScore = p.Score
		}
	}
	if oldScore != 0 {
		t.Errorf("old.go (48h) score = %.4f, want 0 (beyond 24h decay)", oldScore)
	}
}

// TestRankFilePartsTopOutAtTheSumOfTheLiveWeights pins the file branch's scale:
// a part that fires every signal the engine can measure — @-referenced, same
// directory as the goal's file, every goal token present, touched now — scores
// the sum of the live weights, 0.85. The missing 0.15 is centrality (§6.2.3),
// which needs a repo graph that does not exist and so is unearnable by anyone.
//
// This test previously demanded 1.0 and the blend divided by 0.85 to deliver
// it, on the theory that file parts otherwise sit below the 0..1 scale KindRAG
// cosines use. That reasoning was inverted: the division multiplied *every*
// file part by 1.176, and almost no file part is a perfect match, so it
// systematically inflated weak file evidence against retrieved chunks — which
// in practice score ~0.05–0.30 (the default embedder is a hashing vectorizer
// over literal tokens, internal/memory/embed.go), nowhere near the 0.85 the
// division was defending. Do not restore the /0.85.
func TestRankFilePartsTopOutAtTheSumOfTheLiveWeights(t *testing.T) {
	eng := New(Deps{})
	goal := "fix login @auth/login.go"
	req := rankReq(goal)
	rec := time.Now()
	parts := []Part{{
		Kind:     KindFile,
		Source:   "auth/login.go",
		Text:     "fix login @auth/login.go",
		Recency:  rec,
		Explicit: true,
	}}

	// Guard the premise on the three signals with a scalar result. recency
	// decays continuously, so it lands a hair under 1 rather than on it.
	const eps = 1e-6
	if r := recency(parts[0], time.Now()); r < 1-eps {
		t.Fatalf("premise broken: recency=%.9f, must be 1", r)
	}
	if se, e := semantic(parts[0], req), explicit(parts[0]); se < 1-eps || e < 1-eps {
		t.Fatalf("premise broken: semantic=%.9f explicit=%.9f, both must be 1", se, e)
	}
	// Guard proximity differentially rather than by calling it: the same part
	// moved to an unrelated directory differs only in that one signal, so the
	// score gap is proximity's whole weight. This also pins that the weight
	// reaches the score undivided.
	away := parts[0]
	away.Source = "docs/notes.md"
	gap := eng.rank(parts, req)[0].Score - eng.rank([]Part{away}, req)[0].Score
	if gap < 0.25-eps || gap > 0.25+eps {
		t.Fatalf("premise broken: same-dir minus unrelated-dir = %.9f, want exactly 0.25 "+
			"(proximity's §6.2.2 weight, applied without renormalisation)", gap)
	}

	got := eng.rank(parts, req)[0].Score
	if got < 0.85-eps || got > 0.85+eps {
		t.Errorf("a file matching on every measurable signal scored %.4f, want 0.85 "+
			"(0.30 recency + 0.25 proximity + 0.20 semantic + 0.10 explicit)", got)
	}
}

// TestRankProximityAbstainsWhenTheGoalNamesNoFile is the regression guard for
// the defect this file's scale test used to hide. Most goals name no file at
// all ("fix the login bug", "make the tests pass"). proximity has nothing to
// measure against then, and the old code expressed that by treating the part's
// own source as the goal's file — so goalDir always equalled the part's own
// directory and the term returned a constant 1 for every file in the repo,
// regardless of how unrelated it was.
//
// The measurable consequence: 0.25 of unearned weight (0.294 after the old
// /0.85) attached to file parts and to nothing else, a floor no KindRAG cosine
// can clear. These parts have no other live signal, so with the term abstaining
// each must score exactly 0 — and a retrieved chunk with an ordinary cosine
// must outrank all of them.
func TestRankProximityAbstainsWhenTheGoalNamesNoFile(t *testing.T) {
	eng := New(Deps{})
	req := rankReq("fix the Login function so bad passwords are rejected")
	// Realistic multi-segment repo paths: a bare "a.go" has no directory and
	// escaped the old bug through the empty-dir guard, proving nothing.
	parts := []Part{
		{Kind: KindFile, Source: "auth/login.go", Text: "zzz"},
		{Kind: KindFile, Source: "docs/CHANGELOG.md", Text: "zzz"},
		{Kind: KindFile, Source: "vendor/x/y/z.go", Text: "zzz"},
		{Kind: KindFile, Source: "a/b/c/d/e.go", Text: "zzz"},
		// A retrieved chunk at a cosine measured from the real hash embedder
		// for this exact goal against the real Login body (0.1529).
		{Kind: KindRAG, Source: "auth/login.go#1", Score: 0.1529, Text: "func Login"},
	}

	ranked := eng.rank(parts, req)

	for _, p := range ranked {
		if p.Kind != KindFile {
			continue
		}
		if p.Score != 0 {
			t.Errorf("file %q scored %.4f with no measurable signal, want exactly 0: "+
				"proximity must abstain when the goal names no file, not vote 1", p.Source, p.Score)
		}
	}
	if ranked[0].Kind != KindRAG {
		t.Errorf("rank[0] = %s %q (%.4f), want the retrieved chunk: a file the user merely "+
			"had open must not outrank the code the goal actually names",
			ranked[0].Kind, ranked[0].Source, ranked[0].Score)
	}
}

// TestRankRetrievedChunkOutranksAnUnrelatedRecentFile is the ordering guard for
// the cosine/blend scale mismatch. The setup is the ordinary one: the user asks
// to fix a function, the store returns the chunk holding it, and two files the
// user happens to have open are freshly written and contain literally nothing —
// 400 bytes of "z", not one token shared with the goal, an unrelated directory.
//
// Before the cosine was normalised, those two content-free files ranked 1st and
// 2nd at 0.3000 on recency alone and the retrieved chunk ranked 3rd at 0.1529.
// 0.1529 is measured, not chosen: it is what internal/memory's hashing
// embedder actually returns for this goal against this Login body. A cosine
// cannot reach the 1.0 the blend's other signals reach, because its denominator
// carries the chunk's own length, so retrieval lost to "the user opened
// something recently" structurally rather than occasionally.
func TestRankRetrievedChunkOutranksAnUnrelatedRecentFile(t *testing.T) {
	eng := New(Deps{})
	req := rankReq("fix the Login function so bad passwords are rejected")
	now := time.Now()
	parts := []Part{
		{Kind: KindFile, Source: "docs/CHANGELOG.md", Text: "zzzz", Recency: now},
		{Kind: KindFile, Source: "docs/NOTES.md", Text: "zzzz", Recency: now},
		{Kind: KindRAG, Source: "auth/login.go#1", Score: 0.1529, Text: "func Login(user, pass string) error"},
		{Kind: KindRAG, Source: "auth/session.go#1", Score: 0, Text: "func NewSession(user string) *Session"},
	}

	ranked := eng.rank(parts, req)
	if ranked[0].Source != "auth/login.go#1" {
		got := make([]string, 0, len(ranked))
		for _, p := range ranked {
			got = append(got, string(p.Kind)+" "+p.Source)
		}
		t.Errorf("rank[0] = %s %q (%.4f), want the retrieved chunk auth/login.go#1. Order was %v.\n"+
			"A file with no goal token in it must not outrank the code the goal names, on the "+
			"strength of having been written recently", ranked[0].Kind, ranked[0].Source, ranked[0].Score, got)
	}
	// The rescale must not reorder the retrieved set — the store already ordered
	// it, and a pure division by the set maximum preserves that.
	var ragOrder []string
	for _, p := range ranked {
		if p.Kind == KindRAG {
			ragOrder = append(ragOrder, p.Source)
		}
	}
	if len(ragOrder) != 2 || ragOrder[0] != "auth/login.go#1" || ragOrder[1] != "auth/session.go#1" {
		t.Errorf("retrieved set reordered to %v; normalisation is a rescale, not a re-rank", ragOrder)
	}
}

// TestRankNormalisationInventsNoSignalFromAnEmptyRetrievedSet: dividing by the
// set maximum is only safe if the division cannot happen when there is no
// maximum. A set of all-zero cosines has no best member to normalise against,
// and must stay at zero rather than promoting its first element to 1.0.
func TestRankNormalisationInventsNoSignalFromAnEmptyRetrievedSet(t *testing.T) {
	eng := New(Deps{})
	req := rankReq("fix the Login function")
	parts := []Part{
		{Kind: KindRAG, Source: "a#1", Score: 0, Text: "func A()"},
		{Kind: KindRAG, Source: "b#1", Score: 0, Text: "func B()"},
		{Kind: KindFile, Source: "docs/x.md", Text: "zzz", Recency: time.Now()},
	}
	ranked := eng.rank(parts, req)
	for _, p := range ranked {
		if p.Kind == KindRAG && p.Score != 0 {
			t.Errorf("RAG %q scored %.4f from a set whose best cosine is 0, want 0", p.Source, p.Score)
		}
	}
	if ranked[0].Kind != KindFile {
		t.Errorf("rank[0] = %s %q, want the file: with no retrieval signal at all the "+
			"recent file is the best evidence there is", ranked[0].Kind, ranked[0].Source)
	}
}

// TestRankRetrievedChunkStaysBelowAPerfectlyMatchedFile bounds the fix in the
// other direction. The rescale gives the best chunk in a set 0.90, so it clears
// the 0.30 an unrelated recent file earns — but a file that fires every
// measurable signal (fresh, same directory as the goal's file, every goal token
// present, @-referenced) reaches 0.85 and must stay in the same neighbourhood
// rather than being buried. 0.90 is 1.00 less the explicit bonus's own 0.10,
// which is what keeps the RAG branch inside the [0,1] §6.2.2 promises.
func TestRankRetrievedChunkStaysBelowAPerfectlyMatchedFile(t *testing.T) {
	eng := New(Deps{})
	goal := "fix login @auth/login.go"
	req := rankReq(goal)
	perfect := Part{
		Kind: KindFile, Source: "auth/login.go", Text: goal,
		Recency: time.Now(), Explicit: true,
	}
	chunk := Part{Kind: KindRAG, Source: "auth/login.go#1", Score: 0.20, Text: "func Login"}

	ranked := eng.rank([]Part{perfect, chunk}, req)
	var fileScore, ragScore float64
	for _, p := range ranked {
		if p.Kind == KindFile {
			fileScore = p.Score
		} else {
			ragScore = p.Score
		}
	}
	if ragScore > 1.0 {
		t.Errorf("RAG score %.4f exceeds the [0,1] the §6.2.2 blend promises", ragScore)
	}
	if gap := ragScore - fileScore; gap > 0.10 {
		t.Errorf("best chunk %.4f beats a perfect file %.4f by %.4f; the rescale is meant to "+
			"put retrieval on the blend's scale, not above it", ragScore, fileScore, gap)
	}
}

// TestPreferencesAreRankedByGoalOverlap pins what decides which preferences
// survive trimming, because that turned out not to be what an audit of this
// tree assumed.
//
// The assumption was that preferences reach the prompt in key order and are
// therefore trimmed alphabetically: PreferenceStore.Preferences sorts its keys
// (S5 determinism), the Prompt Compiler's trimGroup keeps the leading parts,
// and nothing in between looked like ranking. Measured, that is wrong — rank()
// scores preference parts like anything else, so a preference sharing
// vocabulary with the goal is kept ahead of one that does not.
//
// What is true is narrower and worth writing down. Of the four §6.2.2 signals,
// three cannot fire for a preference: Recency is never set by the store,
// proximity has no path to measure, and Explicit is only set by an @-reference
// to a file source. Only semantic contributes, at weight 0.20, and its
// denominator is the goal's token count — so a long goal drives every
// preference toward 0. Preferences that share nothing with the goal all score
// exactly 0.0, and sort.SliceStable then preserves their input order, which is
// the store's alphabetical one.
//
// So: relevance first, alphabet only as the tie-break among equally irrelevant
// preferences. That tie is the common case, and improving it needs data the
// store does not keep (when the user set each preference) — a schema question,
// not a ranking bug. This test exists so the next reader measures instead of
// assuming, in either direction.
func TestPreferencesAreRankedByGoalOverlap(t *testing.T) {
	eng := New(Deps{})
	req := rankReq("fix the login bug in auth")

	// Deliberately adversarial ordering: the relevant preference sorts in the
	// middle, so neither "first wins" nor "last wins" can pass by accident.
	ranked := eng.rank([]Part{
		{Kind: KindPreferences, Source: "aaa_colour", Text: "aaa_colour: prefer blue"},
		{Kind: KindPreferences, Source: "mmm_login", Text: "mmm_login: the login flow in auth uses bearer tokens"},
		{Kind: KindPreferences, Source: "zzz_tabs", Text: "zzz_tabs: never use tabs"},
	}, req)

	if ranked[0].Source != "mmm_login" {
		t.Errorf("top preference = %q (score %.4f), want mmm_login — the goal-overlapping preference must"+
			" outrank the alphabetically first one, or trimming keeps prefs by first letter",
			ranked[0].Source, ranked[0].Score)
	}
	if ranked[0].Score <= 0 {
		t.Errorf("top preference scored %.4f; semantic() is the only signal a preference can earn and it"+
			" contributed nothing", ranked[0].Score)
	}
	// The two with no overlap tie at zero and keep the store's alphabetical
	// order. Asserted rather than left implicit: it is the actual trimming rule
	// in the common case, and a silent change to it changes which preferences a
	// user loses.
	if ranked[1].Score != 0 || ranked[2].Score != 0 {
		t.Errorf("non-overlapping preferences scored %.4f/%.4f, want 0/0", ranked[1].Score, ranked[2].Score)
	}
	if ranked[1].Source != "aaa_colour" || ranked[2].Source != "zzz_tabs" {
		t.Errorf("tie order = %q, %q; want aaa_colour, zzz_tabs — equal scores must hold the input order"+
			" (sort.SliceStable), or S5 determinism is gone", ranked[1].Source, ranked[2].Source)
	}
}
