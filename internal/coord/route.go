// route.go — Single-agent fallback (File 12 §12.1.3, §12.7).
//
// Multi-agent is NOT the default. The routing layer maps the classifier's Mode
// to a Decision: a trivial goal (Single) delegates to the single-agent runtime
// — no plan, no spawn, no coordination tax. Only Multi starts the orchestrator.
// This is the §12.1.3 invariant: "explain this function" stays single-agent
// because paying the coordination tax for a quick question would be absurd.
//
// Spec gap (Decision 2 + Sprint 10 design): the actual routing into the
// single-agent runtime loop is the composition root (the integration sprint
// wires `runtime.Core` to either `startTurn` for Single or `orchestrator.Run`
// for Multi, File 12 §12.7 step 3). Sprint 10 ships the Decision + Route map +
// ShouldOrchestrate so the integration sprint's composition root has a pure
// function to call.

package coord

// Decision is the routing outcome (File 12 §12.1.3, §12.7).
type Decision int

const (
	// DelegateSingle defers to the single-agent runtime: no plan, no spawn,
	// no coordination tax. The goal runs as one agent turn.
	DelegateSingle Decision = iota
	// Orchestrate starts the orchestrator: decompose, dispatch, merge.
	Orchestrate
	// SingleNamedAgent runs a single named agent ad-hoc (e.g. "/agent coder …").
	SingleNamedAgent
)

// decisionNames backs Decision.String for logs / error messages.
var decisionNames = [...]string{
	DelegateSingle:   "delegate_single",
	Orchestrate:      "orchestrate",
	SingleNamedAgent: "single_named_agent",
}

// String returns a readable name for the Decision (logs / errors).
func (d Decision) String() string {
	if d >= 0 && int(d) < len(decisionNames) {
		return decisionNames[d]
	}
	return "unknown_decision"
}

// Route maps a classifier Mode to the routing Decision (File 12 §12.1.3):
//
//	Single      → DelegateSingle (defer to the single-agent runtime)
//	Multi       → Orchestrate (start the orchestrator)
//	SingleNamed → SingleNamedAgent (ad-hoc single named agent)
//
// Only Multi pays the coordination tax; the other two defer. This is the pure
// function the composition root calls to decide whether to start the
// orchestrator.
func Route(mode Mode) Decision {
	switch mode {
	case Multi:
		return Orchestrate
	case SingleNamed:
		return SingleNamedAgent
	default:
		return DelegateSingle
	}
}
