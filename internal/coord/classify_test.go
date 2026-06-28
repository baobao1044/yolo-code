// Tests for L11-001 — the mode classifier (File 12 §12.1.3).
//
// The classifier decides whether a goal pays the coordination tax
// (multi-agent) or stays single-agent. It is conservative: when unsure,
// default to Single, because the coordination tax is real ("explain this
// function" should not spawn a Planner/Coder/Reviewer/Tester).

package coord

import "testing"

// TestClassify is the §12.1.3 trigger table. The classifier must be
// conservative: a short question or a single-file fix stays Single; a
// multi-clause "refactor X, update Y, add Z" goes Multi; an explicit /plan
// is Multi; an explicit /agent is SingleNamed.
func TestClassify(t *testing.T) {
	cases := []struct {
		goal string
		want Mode
	}{
		// Single-agent (no coordination tax).
		{"explain this function", Single},
		{"what does core.go do", Single},
		{"fix this bug in file X", Single},
		{"rename the helper", Single},
		// Two clauses stay Single (the ≥3 threshold for auto-Multi is a
		// coordination-tax guard — two clauses don't yet justify the tax).
		{"refactor X and add a test", Single},
		{"fix the bug, update the docs", Single},

		// Multi-agent (auto-classified by clause count, ≥3).
		{"refactor auth, update callers, add tests", Multi},
		{"refactor X, add tests, fix CI", Multi},
		{"update the API and fix the docs and add a migration", Multi},

		// Explicit overrides.
		{"/plan refactor the auth layer end to end", Multi},
		{"/agent coder implement the token counter", SingleNamed},

		// Conservative default for ambiguous / short input.
		{"hello", Single},
		{"", Single},
	}
	for _, c := range cases {
		got := Classify(c.goal)
		if got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.goal, got, c.want)
		}
	}
}

// TestShouldOrchestrate is the L11-008 helper surface: only Multi pays the
// tax. Defined here because it shares the classifier truth table.
func TestShouldOrchestrate(t *testing.T) {
	if ShouldOrchestrate("explain this function") {
		t.Errorf("ShouldOrchestrate(explain...) = true, want false")
	}
	if !ShouldOrchestrate("refactor auth, update callers, add tests") {
		t.Errorf("ShouldOrchestrate(refactor...) = false, want true")
	}
}
