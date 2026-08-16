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

// TestClassifyDoesNotOverTrigger pins the 4.12 regression: a bare comma/"and"
// count routed almost any three-part prompt into a full fan-out. Every goal
// here is ONE piece of work (or one question) that a single agent should
// answer, and every one of them was classified Multi before the inquiry gate
// and the work-verb requirement went in.
func TestClassifyDoesNotOverTrigger(t *testing.T) {
	cases := []struct {
		name string
		goal string
	}{
		{"question in three parts", "explain what this does, why it's slow, and how to fix it"},
		{"and-chained comparison", "what is the difference between foo and bar and baz"},
		{"read then narrate", "read the file and tell me what it does"},
		{"file enumeration", "look at foo.go, bar.go, and baz.go"},
		{"run then narrate", "run the tests and tell me which ones fail and why"},
		{"read-only survey", "search for TODO comments, list them, and count them"},
		{"oxford comma on two tasks", "fix the bug, and add a test"},
		{"trailing politeness", "refactor X and add a test, please"},
		{"trailing comma", "fix the bug, update the docs,"},
		{"one task with an aside", "fix this bug in file X, it's on line 12 and it panics on nil"},
		{"review request", "review the diff, point out the bugs, and suggest fixes"},
		{"single task, and-joined objects", "add a flag for verbose and quiet output"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.goal); got != Single {
				t.Errorf("Classify(%q) = %v, want Single (one request should not pay the coordination tax)", c.goal, got)
			}
		})
	}
}

// TestClassifyStillFansOutRealWork is the other side of the line: genuinely
// multi-part work must still reach the orchestrator. Tightening the classifier
// is only correct if it did not also silence these.
func TestClassifyStillFansOutRealWork(t *testing.T) {
	cases := []struct {
		name string
		goal string
	}{
		{"comma list", "refactor auth, update callers, add tests"},
		{"oxford comma", "refactor X, add tests, and fix CI"},
		{"and chain", "update the API and fix the docs and add a migration"},
		{"four tasks", "refactor the auth layer, add OAuth support, update the docs, and bump the version"},
		{"schema migration", "migrate the database schema, update the ORM models, and rewrite the integration tests"},
		{"feature + tests + docs", "implement the parser, write unit tests for it, and document the grammar"},
		{"then-chained", "extract the client, then wire it into main, then add a benchmark"},
		{"polite multi", "can you refactor auth, update callers, and add tests"},
		{"repeated verb", "add x.txt, add y.txt, add z.txt"},
		{"semicolons", "remove the dead code; rename the helper; regenerate the mocks and update the docs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.goal); got != Multi {
				t.Errorf("Classify(%q) = %v, want Multi (genuinely multi-part work must fan out)", c.goal, got)
			}
		})
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
