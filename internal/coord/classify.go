// classify.go — the mode classifier (File 12 §12.1.3).
//
// Multi-agent is NOT the default. A quick question stays single-agent — paying
// the coordination tax for "explain this function" would be absurd. The
// classifier is a conservative ruleset: when unsure, default to Single.

package coord

import "strings"

// Mode is the orchestration decision for a goal (File 12 §12.1.3).
type Mode int

const (
	// Single: defer to the single-agent runtime (no plan, no spawn).
	Single Mode = iota
	// Multi: run the orchestrator — decompose, dispatch, merge.
	Multi
	// SingleNamed: a single named agent (e.g. "/agent coder <task>"), ad-hoc.
	SingleNamed
)

// Classify decides the Mode for a goal. Rules (File 12 §12.1.3, conservative):
//   - "/plan <complex task>" → Multi (explicit)
//   - "/agent <role> <task>" → SingleNamed (explicit)
//   - ≥3 clauses (split on "and"/commas) → Multi (auto)
//   - otherwise → Single (default; coordination tax is real)
func Classify(goal string) Mode {
	g := strings.TrimSpace(goal)
	if g == "" {
		return Single
	}
	switch {
	case strings.HasPrefix(g, "/plan"):
		return Multi
	case strings.HasPrefix(g, "/agent"):
		return SingleNamed
	}
	if clauseCount(g) >= 3 {
		return Multi
	}
	return Single
}

// ShouldOrchestrate reports whether a goal pays the coordination tax (Multi
// only). Used by the routing layer (L11-008) to decide whether to start the
// orchestrator or defer to the single-agent runtime.
func ShouldOrchestrate(goal string) bool {
	return Classify(goal) == Multi
}

// clauseCount estimates the number of independent clauses in a goal by
// splitting on "and" (surrounded by spaces, so "sand" is unaffected) and on
// commas. A "refactor X, add tests, fix CI" goal has 3 clauses → Multi.
func clauseCount(g string) int {
	// Split on commas first.
	parts := strings.Split(g, ",")
	// Within each comma-part, split on " and ".
	n := 0
	for _, p := range parts {
		segs := strings.Split(p, " and ")
		n += len(segs)
	}
	// A goal with no separators yields 1 part; we want the clause count to
	// reflect "and"-chains and comma lists. strings.Split on a missing
	// separator returns a 1-element slice, so the count is the total of
	// segments across all comma parts.
	if n < 1 {
		n = 1
	}
	return n
}
