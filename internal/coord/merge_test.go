// Tests for L11-005 — Merge + re-verify (File 12 §12.6).
//
// Merge combines per-todo diffs into a MergedPatch and re-verifies it through
// the Verifier seam. Overlap detection is via Todo.Artifacts (file paths):
// two todos touching the same file → Conflict (the Patch Engine serializes
// concurrent overlaps, File 10 §10.5 / File 12 §12.6; Sprint 10 detects the
// overlap in-memory and surfaces it — the real three-way git merge is the
// integration sprint).

package coord

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeVerifier is a Verifier seam that returns a canned pass/fail.
type fakeVerifier struct {
	pass bool
}

func (f fakeVerifier) Verify(_ context.Context, _ string) (bool, error) {
	return f.pass, nil
}

// errVerifier always errors (simulates a verifier crash).
type errVerifier struct{}

func (errVerifier) Verify(_ context.Context, _ string) (bool, error) {
	return false, errors.New("verifier crashed")
}

// TestMergeDistinctFiles: todos touching distinct files combine into a single
// diff, no conflict, and the verifier pass → merge returns ok.
func TestMergeDistinctFiles(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"file1.go"}},
		{ID: "b", Status: Done, Artifacts: []string{"file2.go"}},
	}}
	diffs := map[string]string{
		"a": "diff for file1",
		"b": "diff for file2",
	}
	mp, err := Merge(context.Background(), plan, diffs, fakeVerifier{pass: true})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(mp.Conflicts) != 0 {
		t.Errorf("Conflicts = %v, want empty (distinct files)", mp.Conflicts)
	}
	if !strings.Contains(mp.CombinedDiff, "diff for file1") || !strings.Contains(mp.CombinedDiff, "diff for file2") {
		t.Errorf("CombinedDiff = %q, want both diffs", mp.CombinedDiff)
	}
	if !mp.Verified {
		t.Errorf("Verified = false, want true (verifier passed)")
	}
}

// TestMergeSameFileConflict: two todos touching the same file → Conflict, and
// merge returns an error (the orchestrator must not silently drop one).
func TestMergeSameFileConflict(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"shared.go"}},
		{ID: "b", Status: Done, Artifacts: []string{"shared.go"}},
	}}
	diffs := map[string]string{"a": "diff a", "b": "diff b"}
	_, err := Merge(context.Background(), plan, diffs, fakeVerifier{pass: true})
	if err == nil {
		t.Fatalf("Merge: want error for same-file conflict, got nil")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// TestMergeVerifierFail: distinct files but the verifier fails → merge fails
// (Verified=false, error). The merged patch does not pass verification.
func TestMergeVerifierFail(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"file1.go"}},
		{ID: "b", Status: Done, Artifacts: []string{"file2.go"}},
	}}
	diffs := map[string]string{"a": "diff a", "b": "diff b"}
	mp, err := Merge(context.Background(), plan, diffs, fakeVerifier{pass: false})
	if err == nil {
		t.Fatalf("Merge: want error for verifier fail, got nil")
	}
	if mp.Verified {
		t.Errorf("Verified = true, want false (verifier failed)")
	}
}

// TestMergeVerifierError: a verifier crash propagates as a merge error.
func TestMergeVerifierError(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"file1.go"}},
	}}
	diffs := map[string]string{"a": "diff a"}
	_, err := Merge(context.Background(), plan, diffs, errVerifier{})
	if err == nil {
		t.Fatalf("Merge: want verifier crash propagated, got nil")
	}
}

// TestMergeEmptyPlan: an empty (or all-failed) plan yields an empty merged
// patch with no error and no verifier call.
func TestMergeEmptyPlan(t *testing.T) {
	plan := &Plan{ID: "p", Todos: nil}
	mp, err := Merge(context.Background(), plan, map[string]string{}, fakeVerifier{pass: true})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if mp.CombinedDiff != "" {
		t.Errorf("CombinedDiff = %q, want empty", mp.CombinedDiff)
	}
	if len(mp.Conflicts) != 0 {
		t.Errorf("Conflicts = %v, want empty", mp.Conflicts)
	}
	if !mp.Verified {
		t.Errorf("Verified = false, want true (empty patch trivially verifies)")
	}
}

// TestMergeOnlyDoneTodos: Failed todos are skipped (their diffs are not
// merged); only Done todos contribute.
func TestMergeOnlyDoneTodos(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "ok1", Status: Done, Artifacts: []string{"f1.go"}},
		{ID: "bad", Status: Failed, Artifacts: []string{"f2.go"}},
		{ID: "ok2", Status: Done, Artifacts: []string{"f3.go"}},
	}}
	diffs := map[string]string{
		"ok1": "diff f1",
		"bad": "should be skipped",
		"ok2": "diff f3",
	}
	mp, err := Merge(context.Background(), plan, diffs, fakeVerifier{pass: true})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if strings.Contains(mp.CombinedDiff, "should be skipped") {
		t.Errorf("CombinedDiff includes a Failed todo's diff, want skipped")
	}
	if !strings.Contains(mp.CombinedDiff, "diff f1") || !strings.Contains(mp.CombinedDiff, "diff f3") {
		t.Errorf("CombinedDiff = %q, want both Done todos' diffs", mp.CombinedDiff)
	}
}

// contentVerifier mirrors the real cmd/yolo mergeVerifier: a patch counts as
// verified iff it carries non-whitespace content. Used by the blank-diff tests
// so they pin the behaviour the production seam actually sees.
type contentVerifier struct{}

func (contentVerifier) Verify(_ context.Context, combinedDiff string) (bool, error) {
	return strings.TrimSpace(combinedDiff) != "", nil
}

// TestMergeAllDoneBlankDiffs: every Done todo produced no patch. The combined
// diff must be empty and trivially verify — joining the blanks would fabricate
// separator-only content ("\n\n" for three todos) that no todo produced and
// that the verifier then rejects.
func TestMergeAllDoneBlankDiffs(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"f1.go"}},
		{ID: "b", Status: Done, Artifacts: []string{"f2.go"}},
		{ID: "c", Status: Done, Artifacts: []string{"f3.go"}},
	}}
	diffs := map[string]string{"a": "", "b": "", "c": ""}
	mp, err := Merge(context.Background(), plan, diffs, contentVerifier{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if mp.CombinedDiff != "" {
		t.Errorf("CombinedDiff = %q, want empty (no todo produced a patch)", mp.CombinedDiff)
	}
	if !mp.Verified {
		t.Errorf("Verified = false, want true (empty patch trivially verifies)")
	}
}

// TestMergeSkipsBlankDiffs: a blank diff between two real ones must not leave
// a stray separator run in the combined patch.
func TestMergeSkipsBlankDiffs(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"f1.go"}},
		{ID: "blank", Status: Done, Artifacts: []string{"f2.go"}},
		{ID: "c", Status: Done, Artifacts: []string{"f3.go"}},
	}}
	diffs := map[string]string{"a": "diff a", "blank": "   \n\t", "c": "diff c"}
	mp, err := Merge(context.Background(), plan, diffs, contentVerifier{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if want := "diff a\ndiff c"; mp.CombinedDiff != want {
		t.Errorf("CombinedDiff = %q, want %q", mp.CombinedDiff, want)
	}
}

// TestMergeNotVerifiedErrorDetail: a rejected patch must say WHY — the bare
// "failed verification" string told the caller nothing. The error wraps
// ErrNotVerified and carries the merge's shape.
func TestMergeNotVerifiedErrorDetail(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"f1.go"}},
	}}
	diffs := map[string]string{"a": "diff a"}
	_, err := Merge(context.Background(), plan, diffs, fakeVerifier{pass: false})
	if err == nil {
		t.Fatalf("Merge: want error for verifier fail, got nil")
	}
	if !errors.Is(err, ErrNotVerified) {
		t.Errorf("err = %v, want ErrNotVerified", err)
	}
	for _, want := range []string{"6 bytes", "1 done todo", `"diff a"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %s", err.Error(), want)
		}
	}
}

// TestMergeVerifierErrorDetail: a verifier crash must reach the caller with
// the underlying cause attached, not as a bare sentinel.
func TestMergeVerifierErrorDetail(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"f1.go"}},
	}}
	diffs := map[string]string{"a": "diff a"}
	_, err := Merge(context.Background(), plan, diffs, errVerifier{})
	if err == nil {
		t.Fatalf("Merge: want verifier crash propagated, got nil")
	}
	if !strings.Contains(err.Error(), "verifier crashed") {
		t.Errorf("err = %q, want the underlying verifier error", err.Error())
	}
}

// TestMergeSummary: the MergedPatch carries a summary with the done/failed
// counts per todo (File 12 §12.6 status table).
func TestMergeSummary(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{
		{ID: "a", Status: Done, Artifacts: []string{"f1.go"}},
		{ID: "b", Status: Failed, Artifacts: []string{"f2.go"}},
	}}
	diffs := map[string]string{"a": "diff a"}
	mp, err := Merge(context.Background(), plan, diffs, fakeVerifier{pass: true})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if mp.Summary.Done != 1 || mp.Summary.Failed != 1 {
		t.Errorf("Summary = {Done:%d Failed:%d}, want {1,1}", mp.Summary.Done, mp.Summary.Failed)
	}
}

// TestMergedReportsTheDiffNotTheTodoCount pins what plan.done's Merged field
// means. It used to answer Summary.Done > 0 — "some todo finished" — which is
// not the same question. Coders that finish without emitting a diff are the
// normal case today (withPatches is unwired, so every coder emits ""), and the
// old answer announced a successful merge over zero bytes. Merged must describe
// the patch.
func TestMergedReportsTheDiffNotTheTodoCount(t *testing.T) {
	plan := &Plan{ID: "p", Todos: []Todo{{ID: "a", Status: Done, Artifacts: []string{"f1.go"}}}}

	// A done todo that produced no diff: merged nothing.
	empty, err := Merge(context.Background(), plan, map[string]string{}, fakeVerifier{pass: true})
	if err != nil {
		t.Fatalf("Merge (no diffs): %v", err)
	}
	if empty.Summary.Done != 1 {
		t.Fatalf("Summary.Done = %d, want 1 — the premise of this test is a done todo", empty.Summary.Done)
	}
	if empty.Merged() {
		t.Errorf("Merged() = true over a %d-byte diff: it is reporting the todo count, not the patch",
			len(empty.CombinedDiff))
	}

	// The same todo with a real diff: merged something.
	full, err := Merge(context.Background(), plan, map[string]string{"a": "diff a"}, fakeVerifier{pass: true})
	if err != nil {
		t.Fatalf("Merge (with diff): %v", err)
	}
	if !full.Merged() {
		t.Errorf("Merged() = false for a %d-byte combined diff", len(full.CombinedDiff))
	}
}
