package cognitive

import (
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestMultiCandidateAllowedBeforeCaps pins the common path: under all caps the
// Cost Controller permits multi-candidate patch generation (File 07 §7.6.2).
func TestMultiCandidateAllowedBeforeCaps(t *testing.T) {
	cfg := CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 10 * time.Minute}
	cost, _ := newCost(t, cfg, 0.0001)
	cost.AddTokens("t_c", 100, 0) // $0.01, well under cap
	cost.IncLoop("t_c")
	cost.IncReflection("t_c")
	if !cost.MultiCandidateAllowed("t_c") {
		t.Error("MultiCandidateAllowed = false under all caps, want true (normal path)")
	}
}

// TestMultiCandidateDisabledAtMaxLoops pins the first degrade rung (§7.6.2):
// after MaxLoops reflection loops the agent degrades to single-candidate
// (only-verify-adjacent) mode, so MultiCandidateAllowed returns false. This
// rung mirrors ReflectionAllowed (the loop threshold) and reuses the same
// counters.
func TestMultiCandidateDisabledAtMaxLoops(t *testing.T) {
	cfg := CostConfig{MaxLoops: 3, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 10 * time.Minute}
	cost, _ := newCost(t, cfg, 0)

	for i := 0; i < cfg.MaxLoops; i++ {
		cost.IncLoop("t_c")
	}

	if cost.MultiCandidateAllowed("t_c") {
		t.Error("MultiCandidateAllowed = true after MaxLoops, want false (single-candidate mode)")
	}
	// The same threshold disables reflection itself, so the rungs are consistent.
	if cost.ReflectionAllowed("t_c") {
		t.Error("ReflectionAllowed = true after MaxLoops, want false (only-verify mode)")
	}
}

// TestMultiCandidateDisabledAtMaxReflections pins the second degrade rung
// (§7.6.4): after MaxReflections the agent degrades to a single forced candidate
// (autosubmit). This rung sits BELOW the reflection-disable rung — multi-
// candidate is off while reflection itself is still allowed (loops < MaxLoops).
func TestMultiCandidateDisabledAtMaxReflections(t *testing.T) {
	cfg := CostConfig{MaxLoops: 100, MaxReflections: 5, MaxDollars: 1.0, MaxTime: 10 * time.Minute}
	cost, _ := newCost(t, cfg, 0)

	for i := 0; i < cfg.MaxReflections; i++ {
		cost.IncReflection("t_c")
	}

	if cost.MultiCandidateAllowed("t_c") {
		t.Error("MultiCandidateAllowed = true after MaxReflections, want false (single forced candidate)")
	}
	// Reflection itself is still allowed: loops < MaxLoops, no spend, no time cap.
	// The autosubmit rung degrades multi-candidate one step below reflection-disable.
	if !cost.ReflectionAllowed("t_c") {
		t.Error("ReflectionAllowed = false after MaxReflections with loops under MaxLoops, want true (multi is the stricter rung)")
	}
}

// TestMultiCandidateDisabledBySpendCap pins that the spend hard cap disables
// multi-candidate too (it is a strict subset of reflection): crossing MaxDollars
// turns MultiCandidateAllowed false, mirroring ReflectionAllowed.
func TestMultiCandidateDisabledBySpendCap(t *testing.T) {
	cfg := CostConfig{MaxLoops: 6, MaxReflections: 10, MaxDollars: 0.50, MaxTime: 10 * time.Minute}
	cost, _ := newCost(t, cfg, 0.001) // $0.001/token → 600 tokens = $0.60 > $0.50

	cost.AddTokens("t_c", 600, 0)

	if cost.MultiCandidateAllowed("t_c") {
		t.Error("MultiCandidateAllowed = true after spend cap, want false (hard cap → reflection off)")
	}
	if cost.ReflectionAllowed("t_c") {
		t.Error("ReflectionAllowed = true after spend cap, want false")
	}
}

// TestMultiCandidateDisabledByTimeCap pins that the wall-clock hard cap
// disables multi-candidate, mirroring ReflectionAllowed: past the deadline,
// multi-candidate is not permitted.
func TestMultiCandidateDisabledByTimeCap(t *testing.T) {
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	cost := NewCost(CostConfig{MaxLoops: 100, MaxReflections: 10, MaxDollars: 1.0, MaxTime: 1 * time.Nanosecond}, nil, bus)
	cost.RegisterTask("t_t")
	time.Sleep(2 * time.Millisecond) // past the 1ns deadline

	if cost.MultiCandidateAllowed("t_t") {
		t.Error("MultiCandidateAllowed = true past the deadline, want false (time cap)")
	}
	if cost.ReflectionAllowed("t_t") {
		t.Error("ReflectionAllowed = true past the deadline, want false")
	}
}
