package cognitive

import (
	"bytes"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// multiTask builds a session.Task for multi-candidate reflection tests. The goal
// is deliberately free of the stub provider's keywords ("list"/"files"/"read"/
// "edit"/"fix"/"refactor") so the stub takes its default echo branch and the
// per-turn prompt variation produces distinct, deterministic candidate bodies.
func multiTask(retry, max int) *session.Task {
	return &session.Task{ID: "t_m", Goal: "resolve the build failure", Retry: retry, RetryMax: max}
}

// newStubCore wires a Core over the deterministic stub provider + a real bus,
// returning both so the test can inspect published reflection.note events. The
// stub is a pure function of the last user message, so varied multi-candidate
// prompts yield varied (and deterministic) candidate bodies.
func newStubCore(t *testing.T) (*Core, *event.Bus) {
	t.Helper()
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	core := New(NewStubProvider(128_000), bus)
	return core, bus
}

// TestReflectMultiGeneratesDistinctCandidates is the L6-M3 exit criterion:
// ReflectMulti issues up to maxN reflection turns with a varied instruction and
// returns one distinct candidate per turn. The stub provider echoes each varied
// prompt, so the candidate bodies differ. Each turn consumes one retry
// (mirroring Reflect's per-call increment) and publishes a reflection.note.
func TestReflectMultiGeneratesDistinctCandidates(t *testing.T) {
	core, bus := newStubCore(t)
	noteCh := bus.Subscribe("reflection.note")
	task := multiTask(0, 10)

	cands, err := core.ReflectMulti(ctxWithTask("t_m"), task,
		Verdict{Pass: false, Reason: "build failed"}, Observation{Text: "err"},
		3, true)

	if err != nil {
		t.Fatalf("ReflectMulti err = %v, want nil", err)
	}
	if len(cands) != 3 {
		t.Fatalf("len(cands) = %d, want 3 (one per turn)", len(cands))
	}
	// Each candidate carries a non-empty patch body (the model's proposed fix).
	seen := make(map[string]bool, len(cands))
	for i, cand := range cands {
		if len(cand.Patch.Body) == 0 {
			t.Errorf("cand[%d].Patch.Body empty, want the note as the patch body", i)
		}
		body := string(cand.Patch.Body)
		if seen[body] {
			t.Errorf("cand[%d] body duplicates an earlier candidate; the varied prompt must yield distinct bodies", i)
		}
		seen[body] = true
		if cand.Reason != body {
			t.Errorf("cand[%d].Reason = %q, want it to equal the patch body (the note)", i, cand.Reason)
		}
	}
	// Each turn publishes one reflection.note carrying the same note as the
	// candidate, in order.
	for i, want := range cands {
		env := drain(t, noteCh)
		re, ok := env.Evt.(*event.ReflectionEvent)
		if !ok {
			t.Fatalf("event %d type = %T, want *ReflectionEvent", i, env.Evt)
		}
		if re.Note != want.Reason {
			t.Errorf("reflection.note[%d].Note = %q, want %q", i, re.Note, want.Reason)
		}
	}
	// Each turn consumed one retry.
	if task.Retry != 3 {
		t.Errorf("task.Retry = %d, want 3 (one per candidate turn)", task.Retry)
	}
}

// TestReflectMultiStopsAtRetryCap pins the cost-controlled stop mid-multi (§7.6.4):
// each turn consumes one retry, so a tight RetryMax stops generation before
// maxN candidates are produced. The runtime never blows past the retry cap.
func TestReflectMultiStopsAtRetryCap(t *testing.T) {
	core, _ := newStubCore(t)
	task := multiTask(0, 2) // RetryMax 2 → at most 2 candidate turns

	cands, err := core.ReflectMulti(ctxWithTask("t_m"), task,
		Verdict{Pass: false}, Observation{Text: "err"},
		5, true) // request 5, cap allows 2

	if err != nil {
		t.Fatalf("ReflectMulti err = %v, want nil", err)
	}
	if len(cands) != 2 {
		t.Errorf("len(cands) = %d, want 2 (stopped at the retry cap)", len(cands))
	}
	if task.Retry != 2 {
		t.Errorf("task.Retry = %d, want 2 (the cap, not maxN)", task.Retry)
	}
}

// TestReflectMultiDegradedReturnsSingleCandidate pins the cost-degraded path:
// when allowMulti is false the Core returns exactly one candidate — the same
// decision a single Reflect would produce, wrapped as a 1-element slice. The
// single-path patch decision carries the note as the patch body.
func TestReflectMultiDegradedReturnsSingleCandidate(t *testing.T) {
	note := "missing error check. DECISION: patch"
	core, _ := newTestCore(t, []Chunk{{Delta: note}})
	task := reflTask(0, 3)

	cands, err := core.ReflectMulti(ctxWithTask("t_r"), task,
		Verdict{Pass: false}, Observation{},
		5, false) // allowMulti false → cost-degraded

	if err != nil {
		t.Fatalf("ReflectMulti err = %v, want nil", err)
	}
	if len(cands) != 1 {
		t.Fatalf("len(cands) = %d, want 1 (cost-degraded → single candidate)", len(cands))
	}
	if string(cands[0].Patch.Body) != note {
		t.Errorf("cand.Patch.Body = %q, want %q (the single-path patch decision)", cands[0].Patch.Body, note)
	}
	if cands[0].Reason != note {
		t.Errorf("cand.Reason = %q, want %q", cands[0].Reason, note)
	}
	// The degraded path delegates to Reflect, which increments Retry once.
	if task.Retry != 1 {
		t.Errorf("task.Retry = %d, want 1 (degraded path = one Reflect call)", task.Retry)
	}
}

// TestReflectMultiMaxNOneIsDegenerate pins the maxN<=1 degenerate case: even
// with allowMulti true, requesting a single candidate collapses to the
// single-path decision wrapped as a 1-element slice.
func TestReflectMultiMaxNOneIsDegenerate(t *testing.T) {
	note := "wrong signature. DECISION: patch"
	core, _ := newTestCore(t, []Chunk{{Delta: note}})
	task := reflTask(0, 3)

	cands, err := core.ReflectMulti(ctxWithTask("t_r"), task,
		Verdict{Pass: false}, Observation{},
		1, true) // maxN 1 → degenerate

	if err != nil {
		t.Fatalf("ReflectMulti err = %v, want nil", err)
	}
	if len(cands) != 1 {
		t.Fatalf("len(cands) = %d, want 1 (maxN<=1 → degenerate single candidate)", len(cands))
	}
	if string(cands[0].Patch.Body) != note {
		t.Errorf("cand.Patch.Body = %q, want %q", cands[0].Patch.Body, note)
	}
}

// TestReflectMultiRetryCapReturnsAbortCandidate pins that a task already at its
// retry cap surfaces the single-path abort as a 1-element slice: the candidate
// carries no patch body and its Reason explains the cap. The provider is NOT
// called (the abort is cost-controlled, before any streaming).
func TestReflectMultiRetryCapReturnsAbortCandidate(t *testing.T) {
	called := false
	prov := &trackingProvider{onStream: func() { called = true }}
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	core := New(prov, bus)

	cands, err := core.ReflectMulti(ctxWithTask("t_r"), reflTask(3, 3),
		Verdict{Pass: false}, Observation{},
		3, true) // cap already hit → degraded to single abort candidate

	if err != nil {
		t.Fatalf("ReflectMulti err = %v, want nil", err)
	}
	if len(cands) != 1 {
		t.Fatalf("len(cands) = %d, want 1 (retry cap → single abort candidate)", len(cands))
	}
	if len(cands[0].Patch.Body) != 0 {
		t.Errorf("cand.Patch.Body = %q, want empty (abort proposes no patch)", cands[0].Patch.Body)
	}
	if !strings.Contains(cands[0].Reason, "retry cap") {
		t.Errorf("cand.Reason = %q, want it to mention the retry cap", cands[0].Reason)
	}
	if called {
		t.Error("provider was called despite the retry cap; the abort must precede any streaming")
	}
}

// TestReflectMultiPropagatesStreamError pins that a stream error during a
// multi-candidate turn aborts with an error (no candidates returned), mirroring
// Reflect's stream-error handling but surfaced via the error return.
func TestReflectMultiPropagatesStreamError(t *testing.T) {
	core, _ := newTestCore(t, []Chunk{{Err: errStream("reflect: connection lost")}})

	cands, err := core.ReflectMulti(ctxWithTask("t_r"), reflTask(0, 3),
		Verdict{Pass: false}, Observation{},
		3, true)

	if err == nil {
		t.Fatal("ReflectMulti err = nil, want a stream error")
	}
	if cands != nil {
		t.Errorf("cands = %v, want nil on stream error", cands)
	}
	if !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("err = %v, want it to carry the stream error", err)
	}
}

// TestRerankCandidates pins the reranker (File 07 §7.3.4): the base score favors
// earlier candidates, a content heuristic penalizes repeats of failed patch
// bodies, and the result is sorted best-first without mutating the input.
func TestRerankCandidates(t *testing.T) {
	cases := []struct {
		name         string
		cs           []PatchCandidate
		failedBodies [][]byte
		wantOrder    []string // the Reason of each candidate, best-first
		wantScores   []float64
	}{
		{
			name:      "empty input → empty output",
			cs:        nil,
			wantOrder: []string{},
		},
		{
			name: "index score, no failures → order preserved",
			cs: []PatchCandidate{
				{Reason: "A", Patch: PatchOp{Body: []byte("a")}},
				{Reason: "B", Patch: PatchOp{Body: []byte("b")}},
				{Reason: "C", Patch: PatchOp{Body: []byte("c")}},
			},
			wantOrder:  []string{"A", "B", "C"},
			wantScores: []float64{1.0, 0.9, 0.8},
		},
		{
			name: "failed body is penalized and demoted",
			cs: []PatchCandidate{
				{Reason: "A", Patch: PatchOp{Body: []byte("a")}},
				{Reason: "B", Patch: PatchOp{Body: []byte("b")}}, // b already failed
				{Reason: "C", Patch: PatchOp{Body: []byte("c")}},
			},
			failedBodies: [][]byte{[]byte("b")},
			wantOrder:    []string{"A", "C", "B"},  // B demoted below C
			wantScores:   []float64{1.0, 0.8, 0.4}, // A=1.0, C=0.8, B=0.9-0.5=0.4
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Snapshot the input to verify RerankCandidates does not mutate it.
			before := make([]PatchCandidate, len(tc.cs))
			copy(before, tc.cs)

			got := RerankCandidates(tc.cs, tc.failedBodies)

			if len(tc.wantOrder) == 0 {
				if len(got) != 0 {
					t.Errorf("len(got) = %d, want 0", len(got))
				}
				return
			}
			if len(got) != len(tc.wantOrder) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tc.wantOrder))
			}
			for i, want := range tc.wantOrder {
				if got[i].Reason != want {
					t.Errorf("got[%d].Reason = %q, want %q (best-first order)", i, got[i].Reason, want)
				}
			}
			if tc.wantScores != nil {
				for i, want := range tc.wantScores {
					if got[i].Score != want {
						t.Errorf("got[%d].Score = %v, want %v", i, got[i].Score, want)
					}
				}
			}
			// Input must not be mutated.
			for i := range tc.cs {
				if tc.cs[i].Reason != before[i].Reason {
					t.Errorf("input cs[%d].Reason mutated: %q → %q", i, before[i].Reason, tc.cs[i].Reason)
				}
				if tc.cs[i].Score != before[i].Score {
					t.Errorf("input cs[%d].Score mutated: %v → %v", i, before[i].Score, tc.cs[i].Score)
				}
			}
		})
	}
}

// TestRerankCandidatesSortsStablyByScore pins the tie-break: candidates with
// equal scores keep their original order (sort.SliceStable), so reranking is
// deterministic (S5).
func TestRerankCandidatesSortsStablyByScore(t *testing.T) {
	cs := []PatchCandidate{
		{Reason: "first", Patch: PatchOp{Body: []byte("x")}},
		{Reason: "second", Patch: PatchOp{Body: []byte("x")}}, // same body → same penalty path
		{Reason: "third", Patch: PatchOp{Body: []byte("x")}},
	}
	got := RerankCandidates(cs, [][]byte{[]byte("y")}) // no failures match
	// All share body "x" but no failure matches, so scores are purely by index:
	// 1.0, 0.9, 0.8 → order preserved (stable, already sorted).
	if got[0].Reason != "first" || got[1].Reason != "second" || got[2].Reason != "third" {
		t.Errorf("stable order broken: %q %q %q", got[0].Reason, got[1].Reason, got[2].Reason)
	}
}

// TestReflectMultiThenRerankIntegration pins the integrated flow: ReflectMulti
// produces candidates and RerankCandidates sorts them best-first, demoting any
// that repeat a previously failed patch body.
func TestReflectMultiThenRerankIntegration(t *testing.T) {
	core, _ := newStubCore(t)
	task := multiTask(0, 10)

	cands, err := core.ReflectMulti(ctxWithTask("t_m"), task,
		Verdict{Pass: false, Reason: "build failed"}, Observation{Text: "err"},
		3, true)
	if err != nil {
		t.Fatalf("ReflectMulti err = %v, want nil", err)
	}
	if len(cands) != 3 {
		t.Fatalf("len(cands) = %d, want 3", len(cands))
	}

	// The first candidate's body is a previously failed patch → rerank demotes it.
	failed := [][]byte{cands[0].Patch.Body}
	reranked := RerankCandidates(cands, failed)

	if len(reranked) != 3 {
		t.Fatalf("len(reranked) = %d, want 3", len(reranked))
	}
	// The failed candidate (originally first, score 1.0-0.5=0.5) must rank below
	// the originally-second (0.9) and originally-third (0.8) candidates.
	if bytes.Equal(reranked[0].Patch.Body, cands[0].Patch.Body) {
		t.Error("reranked[0] is the failed candidate; it should be demoted below the non-failed ones")
	}
}
