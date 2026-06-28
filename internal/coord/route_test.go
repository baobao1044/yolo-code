// Tests for L11-008 — Single-agent fallback (File 12 §12.1.3, §12.7).
//
// Multi-agent is NOT the default. The routing layer maps the classifier's Mode
// to a Decision: a trivial goal (Single) delegates to the single-agent runtime
// — no plan, no spawn, no coordination tax. Only Multi starts the orchestrator.
// This is the §12.1.3 invariant: "explain this function" stays single-agent
// because paying the coordination tax for a quick question would be absurd.

package coord

import "testing"

// TestRouteMapsModeToDecision: the classifier Mode → the routing Decision.
//
//	Single        → DelegateSingle (defer to the single-agent runtime)
//	Multi         → Orchestrate (start the orchestrator)
//	SingleNamed   → SingleNamedAgent (ad-hoc single named agent)
func TestRouteMapsModeToDecision(t *testing.T) {
	cases := []struct {
		mode Mode
		want Decision
	}{
		{Single, DelegateSingle},
		{Multi, Orchestrate},
		{SingleNamed, SingleNamedAgent},
	}
	for _, c := range cases {
		got := Route(c.mode)
		if got != c.want {
			t.Errorf("Route(%v) = %v, want %v", c.mode, got, c.want)
		}
	}
}

// TestRouteTrivialStaysSingle: "explain this function" classifies Single →
// Route returns DelegateSingle. The orchestrator must NOT start, no plan, no
// spawn — the coordination tax is avoided.
func TestRouteTrivialStaysSingle(t *testing.T) {
	for _, goal := range []string{
		"explain this function",
		"what does core.go do",
		"fix this bug in file X",
		"rename the helper",
	} {
		if d := Route(Classify(goal)); d != DelegateSingle {
			t.Errorf("Route(Classify(%q)) = %v, want DelegateSingle (trivial goal stays single-agent)", goal, d)
		}
	}
}

// TestRouteComplexStartsOrchestrator: a multi-clause goal classifies Multi →
// Route returns Orchestrate. The orchestrator starts.
func TestRouteComplexStartsOrchestrator(t *testing.T) {
	for _, goal := range []string{
		"refactor auth, update callers, add tests",
		"refactor X, add tests, fix CI",
		"/plan refactor the auth layer end to end",
	} {
		if d := Route(Classify(goal)); d != Orchestrate {
			t.Errorf("Route(Classify(%q)) = %v, want Orchestrate (complex goal starts the orchestrator)", goal, d)
		}
	}
}

// TestRouteExplicitAgentIsSingleNamed: "/agent coder <task>" classifies
// SingleNamed → Route returns SingleNamedAgent.
func TestRouteExplicitAgentIsSingleNamed(t *testing.T) {
	if d := Route(Classify("/agent coder implement the token counter")); d != SingleNamedAgent {
		t.Errorf("Route(/agent...) = %v, want SingleNamedAgent", d)
	}
}

// TestShouldOrchestrateIsMultiOnly: ShouldOrchestrate is true only for Multi —
// Single and SingleNamed both defer (no orchestrator).
func TestShouldOrchestrateIsMultiOnly(t *testing.T) {
	if !ShouldOrchestrate("refactor auth, update callers, add tests") {
		t.Errorf("ShouldOrchestrate(Multi goal) = false, want true")
	}
	if ShouldOrchestrate("explain this function") {
		t.Errorf("ShouldOrchestrate(Single goal) = true, want false")
	}
	if ShouldOrchestrate("/agent coder do X") {
		t.Errorf("ShouldOrchestrate(SingleNamed goal) = true, want false")
	}
}

// TestDecisionString: the Decision values have readable names (for logs /
// error messages). This pins the enum so a reorder doesn't silently change
// the wire meaning.
func TestDecisionString(t *testing.T) {
	if DelegateSingle.String() != "delegate_single" {
		t.Errorf("DelegateSingle.String() = %q, want delegate_single", DelegateSingle.String())
	}
	if Orchestrate.String() != "orchestrate" {
		t.Errorf("Orchestrate.String() = %q, want orchestrate", Orchestrate.String())
	}
	if SingleNamedAgent.String() != "single_named_agent" {
		t.Errorf("SingleNamedAgent.String() = %q, want single_named_agent", SingleNamedAgent.String())
	}
}
