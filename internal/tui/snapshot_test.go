//go:build snapshot

// Snapshot performance budgets (Sprint 11 H-002/H-003). These tests are isolated
// behind the `snapshot` build tag because micro-benchmark timing is noisy and
// should not block the fast default `go test ./...` suite.

package tui

import (
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// TestS1ColdStartBudget measures the very first TUI fold path (cold) and the
// amortized second+ pass (warm). The cold budget is S1 from Sprint 9's exit
// bar: ≤ 50 ms for the first init+fold; warm ≤ 10 ms.
func TestS1ColdStartBudget(t *testing.T) {
	env := event.Envelope{
		Evt: &event.TaskStartedEvent{Task: "t-1", Goal: "benchmark task"},
	}

	// Cold start: first init + first fold.
	start := time.Now()
	m := newModel(nil, nil)
	fold(m, env)
	cold := time.Since(start)
	if cold > 50*time.Millisecond {
		t.Fatalf("cold start %v exceeds 50ms budget", cold)
	}

	// Warm start: cached code path, amortized over many iterations.
	const warmIters = 10000
	start = time.Now()
	for i := 0; i < warmIters; i++ {
		m := newModel(nil, nil)
		fold(m, env)
	}
	warm := time.Since(start) / warmIters
	if warm > 10*time.Millisecond {
		t.Fatalf("warm init %v exceeds 10ms budget", warm)
	}
}
