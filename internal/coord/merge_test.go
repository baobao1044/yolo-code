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
