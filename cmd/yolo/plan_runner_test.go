// Sprint 13 S13-004 tests: the `--plan` runner wires the real orchestrator
// and emits a JSONL transcript (mirroring the headless path).

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
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
		"\"type\":\"coord.plan.done\"",
		"\"type\":\"cost.incurred\"",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("transcript missing %q; output:\n%s", w, out)
		}
	}
}

// Shutdown ordering: every tool.result must have its cost.incurred in the
// transcript. While bus.Close ran before costPub.Stop the trailing accruals
// lost to ErrBusClosed and vanished — the publisher counts them, but by then
// there is no subscriber left to be told. Which accrual loses is a race, so
// this runs the plan several times; one clean run proves nothing.
func TestPlanRunnerCostAccrualsOutliveTheBus(t *testing.T) {
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := runPlanCtx(ctx, "add x.txt, add y.txt, add z.txt")
		cancel()
		if err != nil {
			t.Fatalf("run %d: runPlanCtx: %v", i, err)
		}
		results := strings.Count(out, `"type":"tool.result"`)
		costs := strings.Count(out, `"type":"cost.incurred"`)
		if results == 0 {
			t.Fatalf("run %d: no tool.result in transcript, so the count below proves nothing", i)
		}
		if costs != results {
			t.Fatalf("run %d: %d tool.result but only %d cost.incurred; accruals were lost to a bus closed before costPub.Stop", i, results, costs)
		}
	}
}

func TestPlanRunnerSingleFallsBack(t *testing.T) {
	// Not in the runner itself but verifies ShouldOrchestrate routing in main.
	if coordpkg.ShouldOrchestrate(" explain this function") {
		t.Fatal("single-clause goal should not orchestrate")
	}
	// Three clauses alone are not three tasks — "a, b, c" is a list of nouns
	// asking for nothing, and classifying it Multi was exactly the
	// over-triggering the classifier was fixed to stop. Assert on a goal that
	// genuinely carries three units of work.
	if !coordpkg.ShouldOrchestrate("add x.txt, add y.txt, add z.txt") {
		t.Fatal("three-task goal should orchestrate")
	}
}
