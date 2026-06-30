// Reflection memory (File 07 §7.3.3). Reflection accumulates lessons — root
// causes of prior verification failures — and facts — stable truths about the
// task — across iterations so the next reflection turn is primed with what
// already went wrong. The memory is pure: it stores strings and produces a
// compact prefix to prepend to a reflection prompt; it does no I/O and calls no
// tools (reflection only thinks, §7.3.1).
//
// Sprint 3 (L6-M3) carries lessons and facts as append-only string slices.
// Retrieval returns the slice in insertion order; dedup is intentionally not
// performed — a repeated lesson is signal, not noise, and the model can weigh
// repetition. PromptPrefix is "" when the memory is empty and a deterministic,
// compact summary otherwise (S5).

package cognitive

import (
	"fmt"
	"strings"
)

// ReflectionMemory accumulates lessons and facts across reflection iterations
// (File 07 §7.3.3) so the next reflection turn starts with prior failures
// already in view. It is pure: no provider, no bus, no I/O.
type ReflectionMemory struct {
	lessons []string
	facts   []string
}

// NewReflectionMemory returns an empty ReflectionMemory.
func NewReflectionMemory() *ReflectionMemory {
	return &ReflectionMemory{}
}

// AddLesson records a root-cause lesson from a prior failed verification. Lessons
// are appended in insertion order without dedup — a recurring lesson is itself
// signal the next reflection turn should weigh.
func (m *ReflectionMemory) AddLesson(l string) {
	m.lessons = append(m.lessons, l)
}

// AddFact records a stable fact about the task (something established as true
// and not worth re-deriving). Facts are appended in insertion order without
// dedup, mirroring AddLesson.
func (m *ReflectionMemory) AddFact(f string) {
	m.facts = append(m.facts, f)
}

// Lessons returns the accumulated lessons in insertion order. The returned
// slice aliases the internal storage; callers should treat it as read-only.
func (m *ReflectionMemory) Lessons() []string { return m.lessons }

// Facts returns the accumulated facts in insertion order. The returned slice
// aliases the internal storage; callers should treat it as read-only.
func (m *ReflectionMemory) Facts() []string { return m.facts }

// PromptPrefix returns a compact summary of lessons and facts to prepend to a
// reflection prompt, or "" when the memory is empty (so a fresh task adds no
// noise). The format is deterministic (S5): same lessons+facts → same prefix,
// every run.
func (m *ReflectionMemory) PromptPrefix() string {
	if len(m.lessons) == 0 && len(m.facts) == 0 {
		return ""
	}
	var b strings.Builder
	if len(m.lessons) > 0 {
		b.WriteString("Prior lessons (avoid repeating these failures):\n")
		for i, l := range m.lessons {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, l)
		}
	}
	if len(m.facts) > 0 {
		b.WriteString("Established facts:\n")
		for i, f := range m.facts {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, f)
		}
	}
	return b.String()
}
