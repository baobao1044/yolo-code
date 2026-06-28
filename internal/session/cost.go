// The Cost Controller (File 07) owns the reflection cap a task's RetryMax is
// seeded from. Sprint 1 has no cost controller yet, so a default view is
// injected; Sprint 3 replaces it with the real one. Keeping it an interface
// means the Session Manager's Deps shape does not change later.

package session

// CostView is the read-only slice of the Cost Controller the Session Manager
// needs (File 03 §3.7).
type CostView interface {
	// ReflectionCap returns the maximum reflection-driven retries a task may
	// attempt before the loop steps down (File 07 §7.5).
	ReflectionCap() int
}

// DefaultCostView is the Sprint 1 stand-in: a modest reflection cap so the
// retry machinery has a sane bound before the real controller exists.
type DefaultCostView struct{}

// ReflectionCap returns 3 — enough for a reflection loop to self-correct a
// couple of times before the controller would degrade it.
func (DefaultCostView) ReflectionCap() int { return 3 }
