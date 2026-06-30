package scope

// PatchAttempt is one recorded patch the controller has tried, keyed by the bus
// envelope Seq so the loop can correlate it with patch.applied events.
type PatchAttempt struct {
	Seq      int
	Summary  string
	Accepted bool
}

// Memory is the scope loop's anti-loop memory (File 15 §15.x): it tracks which
// files have already been visited, which patches have been tried, which
// hypotheses failed, and which facts are confirmed. The Controller consults it
// to avoid re-exploring dead ends and to force a scope expand/contract when the
// loop makes no progress.
type Memory struct {
	VisitedFiles     map[string]bool
	TestedPatches    []PatchAttempt
	FailedHypotheses []string
	ConfirmedFacts   []string
}

// NewMemory returns an empty, ready-to-use Memory.
func NewMemory() *Memory {
	return &Memory{VisitedFiles: map[string]bool{}}
}

// RecordVisited marks file as already explored.
func (m *Memory) RecordVisited(file string) {
	m.VisitedFiles[file] = true
}

// RecordPatch appends a patch attempt to the history.
func (m *Memory) RecordPatch(seq int, summary string, accepted bool) {
	m.TestedPatches = append(m.TestedPatches, PatchAttempt{Seq: seq, Summary: summary, Accepted: accepted})
}

// RecordFailedHypothesis appends a hypothesis that did not pan out.
func (m *Memory) RecordFailedHypothesis(h string) {
	m.FailedHypotheses = append(m.FailedHypotheses, h)
}

// RecordFact appends a confirmed fact to the memory.
func (m *Memory) RecordFact(f string) {
	m.ConfirmedFacts = append(m.ConfirmedFacts, f)
}

// Visited reports whether file has already been explored.
func (m *Memory) Visited(file string) bool {
	return m.VisitedFiles[file]
}

// Failed reports whether hypothesis h has already failed (and so should not be
// retried at the same scope).
func (m *Memory) Failed(h string) bool {
	for _, x := range m.FailedHypotheses {
		if x == h {
			return true
		}
	}
	return false
}

// LoopGuard reports whether the loop has tried too many patches without
// progress (more than 10 tested patches). The Controller uses this to force a
// scope expand/contract rather than retrying in place.
func (m *Memory) LoopGuard() bool {
	return len(m.TestedPatches) > 10
}
