// classify.go — the mode classifier (File 12 §12.1.3).
//
// Multi-agent is NOT the default. A quick question stays single-agent — paying
// the coordination tax for "explain this function" would be absurd. The
// classifier is a conservative ruleset: when unsure, default to Single.
//
// Counting raw clauses is not enough to be conservative. Splitting on commas
// and " and " turns "explain what this does, why it's slow, and how to fix it"
// into three "clauses" and fans a single question out to a
// Planner/Coder/Reviewer/Tester — expensive and wrong. Two extra gates fix
// that without loosening anything:
//
//   - an inquiry lead ("what", "explain", "show me", "read") answers the goal
//     with one agent no matter how many clauses it has: it is one question in
//     several parts, not several tasks;
//   - a clause only counts toward the ≥3 threshold when it actually asks for
//     WORK — it opens with an imperative change verb. "add tests" counts;
//     "bar.go", "why it's slow" and a trailing "please" do not.
//
// Both gates only ever move a goal from Multi to Single, which is the
// documented direction of the doubt.

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
//   - an inquiry lead ("what does X do", "explain Y") → Single, whatever the
//     clause count
//   - ≥3 clauses that each ask for work → Multi (auto)
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
	body := stripPolite(strings.ToLower(g))
	if inquiryLeads[firstWord(body)] {
		return Single
	}
	if workClauseCount(body) >= 3 {
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

// politePrefixes are the conversational wrappers that carry no intent. They are
// stripped before classification so "can you refactor auth, update callers, and
// add tests" is judged on the request rather than on the "can you" — otherwise
// the inquiry gate below would read "can" and wrongly stay Single.
var politePrefixes = []string{
	"please ", "pls ", "can you ", "could you ", "would you ", "will you ",
	"i want you to ", "i'd like you to ", "i would like you to ",
	"go ahead and ", "let's ", "lets ",
}

// inquiryLeads open a question or a read-only request. A goal that starts with
// one is answered by a single agent however many clauses it has. Note what is
// NOT here: every verb that changes the repo lives in workVerbs instead.
var inquiryLeads = map[string]bool{
	"what": true, "whats": true, "why": true, "how": true, "when": true,
	"where": true, "who": true, "which": true, "whose": true,
	"is": true, "are": true, "was": true, "were": true, "am": true,
	"does": true, "do": true, "did": true, "can": true, "could": true,
	"should": true, "would": true, "will": true,
	"explain": true, "describe": true, "summarize": true, "summarise": true,
	"tell": true, "show": true, "list": true, "find": true, "locate": true,
	"read": true, "look": true, "check": true, "compare": true, "count": true,
	"search": true, "grep": true, "inspect": true, "review": true,
	"analyze": true, "analyse": true, "diagnose": true, "investigate": true,
	"trace": true, "understand": true, "print": true, "dump": true,
}

// workVerbs open a clause that asks for a change to the repo. Only such clauses
// count toward the ≥3 multi-agent threshold: an enumeration of files
// ("foo.go, bar.go, and baz.go") or a chain of questions is not three tasks.
var workVerbs = map[string]bool{
	"add": true, "implement": true, "write": true, "create": true,
	"build": true, "generate": true, "introduce": true, "expose": true,
	"refactor": true, "rewrite": true, "rework": true, "restructure": true,
	"extract": true, "split": true, "inline": true, "simplify": true,
	"update": true, "upgrade": true, "bump": true, "migrate": true,
	"port": true, "convert": true, "replace": true, "swap": true,
	"fix": true, "repair": true, "patch": true, "harden": true,
	"remove": true, "delete": true, "drop": true, "deprecate": true,
	"rename": true, "move": true, "merge": true, "revert": true,
	"wire": true, "hook": true, "configure": true, "install": true,
	"enable": true, "disable": true, "set": true, "make": true,
	"document": true, "annotate": true, "instrument": true,
	"optimize": true, "optimise": true, "clean": true, "cleanup": true,
	"validate": true, "handle": true, "support": true, "cache": true,
	"test": true, "cover": true, "benchmark": true, "profile": true,
	"lint": true, "format": true, "publish": true, "release": true,
	"backfill": true, "seed": true, "restore": true,
}

// clauseFillers are the connectives and adverbs a clause may open with. They
// are skipped so "and then fix CI" is judged on "fix".
var clauseFillers = map[string]bool{
	"and": true, "then": true, "also": true, "next": true, "plus": true,
	"finally": true, "lastly": true, "additionally": true, "afterwards": true,
	"please": true, "now": true, "first": true, "second": true, "third": true,
	"maybe": true, "just": true,
}

// stripPolite removes the leading conversational wrappers, repeatedly (a goal
// can open with "please can you ...").
func stripPolite(g string) string {
	g = strings.TrimSpace(g)
	for {
		trimmed := false
		for _, p := range politePrefixes {
			if strings.HasPrefix(g, p) {
				g = strings.TrimSpace(g[len(p):])
				trimmed = true
			}
		}
		if !trimmed {
			return g
		}
	}
}

// workClauseCount counts the clauses that ask for a change. The Oxford join
// ", and " is normalized to a plain comma FIRST: splitting it on both
// separators used to yield an empty segment plus a real one, which is what made
// the two-task "fix the bug, and add a test" look like three clauses.
func workClauseCount(g string) int {
	g = strings.ReplaceAll(g, ", and ", ", ")
	g = strings.ReplaceAll(g, ", then ", ", ")
	g = strings.ReplaceAll(g, ";", ",")
	n := 0
	for _, part := range strings.Split(g, ",") {
		for _, seg := range strings.Split(part, " and ") {
			for _, clause := range strings.Split(seg, " then ") {
				if isWorkClause(clause) {
					n++
				}
			}
		}
	}
	return n
}

// isWorkClause reports whether a clause opens with an imperative change verb,
// ignoring leading connectives. "add tests" does; "bar.go" and "why it's slow"
// do not.
func isWorkClause(clause string) bool {
	for _, f := range strings.Fields(clause) {
		w := trimWord(f)
		if w == "" || clauseFillers[w] {
			continue
		}
		return workVerbs[w]
	}
	return false
}

// firstWord returns the first non-empty word of g, stripped of punctuation.
func firstWord(g string) string {
	for _, f := range strings.Fields(g) {
		if w := trimWord(f); w != "" {
			return w
		}
	}
	return ""
}

// trimWord strips the punctuation that clings to a word in prose.
func trimWord(w string) string {
	return strings.Trim(w, `.,;:!?"'()[]`)
}
