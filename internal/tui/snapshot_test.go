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

// TestS2TokenToScreenBudget measures the time from token events to rendered
// output (S2). 1 000 token deltas must fold and produce a View in ≤ 50 ms.
func TestS2TokenToScreenBudget(t *testing.T) {
	m := newModel(nil, nil)
	m, _ = fold(m, event.Envelope{
		Evt: &event.TaskStartedEvent{Task: "t-2", Goal: "token stream"},
	})

	const tokens = 1000
	tokenEnv := event.Envelope{
		Evt: &event.TokenEvent{Task: "t-2", Delta: "x"},
	}

	start := time.Now()
	for i := 0; i < tokens; i++ {
		m, _ = fold(m, tokenEnv)
	}
	_ = m.View()
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("token-to-screen %v exceeds 50ms budget", elapsed)
	}
}
