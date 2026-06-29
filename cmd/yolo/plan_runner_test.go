// Sprint 13 S13-004 tests: the `--plan` runner wires the real orchestrator
// and emits a JSONL transcript (mirroring the headless path).

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	coordpkg "github.com/yolo-code/yolo/internal/coord"
)

func TestPlanRunnerEmitsTranscript(t *testing.T) {
	goal := "add x.txt, add y.txt, add z.txt"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := runPlanCtx(ctx, goal)
	if err != nil {
		t.Fatalf("runPlanCtx: %v", err)
	}
	if out == "" {
		t.Fatal("expected transcript output")
	}
	want := []string{
		"\"type\":\"coord.plan.ready\"",
		"\"type\":\"coord.task.assign\"",
		"\"type\":\"coord.code.ready\"",
		"\"type\":\"coord.review.verdict\"",
		"\"type\":\"coord.test.report\"",
		"\"type\":\"plan.done\"",
		"\"type\":\"cost.incurred\"",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("transcript missing %q; output:\n%s", w, out)
		}
	}
}

func TestPlanRunnerSingleFallsBack(t *testing.T) {
	// Not in the runner itself but verifies ShouldOrchestrate routing in main.
	if coordpkg.ShouldOrchestrate(" explain this function") {
		t.Fatal("single-clause goal should not orchestrate")
	}
	if !coordpkg.ShouldOrchestrate("a, b, c") {
		t.Fatal("three-clause goal should orchestrate")
	}
}
