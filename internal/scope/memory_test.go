package scope

import "testing"

// TestMemory_RecordAndQuery exercises the record/query helpers on Memory.
func TestMemory_RecordAndQuery(t *testing.T) {
	m := NewMemory()

	if m.Visited("a.go") {
		t.Fatal("fresh memory should not report a.go visited")
	}
	m.RecordVisited("a.go")
	if !m.Visited("a.go") {
		t.Error("Visited(a.go) = false after RecordVisited")
	}

	m.RecordPatch(1, "fix import", true)
	m.RecordPatch(2, "fix typo", false)
	if len(m.TestedPatches) != 2 {
		t.Fatalf("TestedPatches len = %d, want 2", len(m.TestedPatches))
	}
	if !m.TestedPatches[0].Accepted {
		t.Error("first patch should be accepted")
	}
	if m.TestedPatches[1].Accepted {
		t.Error("second patch should not be accepted")
	}
	if m.TestedPatches[1].Seq != 2 {
		t.Errorf("second patch Seq = %d, want 2", m.TestedPatches[1].Seq)
	}

	m.RecordFailedHypothesis("bug in foo")
	if !m.Failed("bug in foo") {
		t.Error("Failed should report a recorded hypothesis")
	}
	if m.Failed("never tried") {
		t.Error("Failed should not report an unrecorded hypothesis")
	}

	m.RecordFact("tests green")
	if len(m.ConfirmedFacts) != 1 || m.ConfirmedFacts[0] != "tests green" {
		t.Errorf("ConfirmedFacts = %#v, want [tests green]", m.ConfirmedFacts)
	}
}

// TestMemory_LoopGuardThreshold verifies LoopGuard trips only past 10 tested
// patches (the W3 anti-loop threshold).
func TestMemory_LoopGuardThreshold(t *testing.T) {
	m := NewMemory()
	if m.LoopGuard() {
		t.Fatal("fresh memory should not trip the loop guard")
	}
	for i := 0; i < 10; i++ {
		m.RecordPatch(i, "attempt", false)
	}
	if m.LoopGuard() {
		t.Error("LoopGuard should be false at exactly 10 patches (>10 trips it)")
	}
	m.RecordPatch(10, "one more", false)
	if !m.LoopGuard() {
		t.Error("LoopGuard should be true after 11 patches")
	}
}
