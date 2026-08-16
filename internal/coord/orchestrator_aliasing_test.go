package coord

import (
	"context"
	"testing"
	"time"
)

// TestRunDoesNotWriteThroughThePlannersTodos pins that Run owns its plan.
//
// Planner.Plan returns a Plan by value, which reads as "you get a copy" — but
// Todos is a slice, so the copy shares the planner's backing array. Run writes
// Status and ReworkCycles into that slice on every state change, so without an
// explicit clone those writes reach back into the planner. Today's production
// planners happen to build a fresh slice per call and survive it; every planner
// in the test suite aliases, and so would any planner that caches a decomposed
// plan or reuses a buffer. The consumer is the right place to fix it once,
// rather than making "don't hand out your own slice" an unwritten rule every
// future planner has to know.
func TestRunDoesNotWriteThroughThePlannersTodos(t *testing.T) {
	o, bus, _, _ := newTestOrchestrator(t, []string{"implement X"}, true, 0, true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	o.Start(ctx)
	if err := o.Run(ctx, "refactor X, add tests, fix CI"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	o.Stop(ctx)
	_ = bus.Close()

	// The run drove its own copy to Done — that part must still work.
	if o.plan.Todos[0].Status != Done {
		t.Fatalf("orchestrator todo status = %v, want Done", o.plan.Todos[0].Status)
	}

	// The planner's plan must be exactly as it handed it over.
	planner, ok := o.planner.(fakePlanner)
	if !ok {
		t.Fatalf("planner is %T, want fakePlanner", o.planner)
	}
	if got := planner.plan.Todos[0].Status; got != Pending {
		t.Errorf("planner's own todo status = %v, want Pending: Run wrote through the "+
			"shared backing array into the planner's plan", got)
	}
}
