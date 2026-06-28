package context

import (
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/session"
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
// zero recency (zero time), no goal-file proximity, no keyword overlap, and no
// @-ref, so they tie at exactly 0 and stable sort preserves insertion order.
func TestRankIsDeterministicForTies(t *testing.T) {
	eng := New(Deps{})
	req := rankReq("do something unrelated")
	parts := []Part{
		{Kind: KindFile, Source: "a.go", Text: "x", Recency: time.Time{}},
		{Kind: KindFile, Source: "b.go", Text: "y", Recency: time.Time{}},
		{Kind: KindFile, Source: "c.go", Text: "z", Recency: time.Time{}},
	}
	first := eng.rank(parts, req)
	second := eng.rank(parts, req)

	want := []string{"a.go", "b.go", "c.go"}
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
