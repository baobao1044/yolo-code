package workflow

import "strings"

// WorkflowType is the kind of workflow a goal maps to. It is also the registry
// key the Engine looks workflows up by.
type WorkflowType string

const (
	// TypeBugfix drives the localize → repair → validate pipeline.
	TypeBugfix WorkflowType = "bugfix"
	// TypeFeature drives the design → decompose → implement → verify pipeline.
	TypeFeature WorkflowType = "feature"
	// TypeRefactor drives the analyze → transform → verify pipeline.
	TypeRefactor WorkflowType = "refactor"
	// TypeDefault is the fallback when no keyword matches — bugfix, the
	// conservative choice (a misclassified bugfix is cheaper to recover from
	// than a misclassified feature).
	TypeDefault WorkflowType = "bugfix"
)

// Classifier maps a goal string to a WorkflowType.
type Classifier interface {
	Classify(goal string) WorkflowType
}

// heuristicClassifier is the default Classifier: a conservative keyword ruleset
// over the lowercased goal. First match wins in the order bugfix → refactor →
// feature, so a "fix the bug we added" goal is a bugfix, not a feature. When no
// keyword matches it falls back to TypeDefault (bugfix).
type heuristicClassifier struct{}

// NewClassifier returns the default heuristic Classifier.
func NewClassifier() Classifier { return &heuristicClassifier{} }

// Classify returns the WorkflowType for goal. The rules (first match wins):
//   - bugfix:    "fix", "bug", "error", "fail", "crash", "broken"
//   - refactor:  "refactor", "rename", "restructure", "clean up", "cleanup"
//   - feature:   "add", "implement", "feature", "support", "create", "build"
//   - otherwise: TypeDefault (bugfix)
func (heuristicClassifier) Classify(goal string) WorkflowType {
	g := strings.ToLower(goal)
	switch {
	case containsAny(g, "fix", "bug", "error", "fail", "crash", "broken"):
		return TypeBugfix
	case containsAny(g, "refactor", "rename", "restructure", "clean up", "cleanup"):
		return TypeRefactor
	case containsAny(g, "add", "implement", "feature", "support", "create", "build"):
		return TypeFeature
	default:
		return TypeDefault
	}
}

// containsAny reports whether s contains any of the substrings. The first set
// with a hit wins in Classify, so order matters.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
