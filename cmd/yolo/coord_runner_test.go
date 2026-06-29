// Tests for the runtime.Core-backed coord AgentRunner (Sprint 12 INT-006).

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/cognitive"
	"github.com/yolo-code/yolo/internal/coord"
	"github.com/yolo-code/yolo/internal/event"
)

// TestRuntimeAgentRunnerSpawnsCoder drives the orchestrator with a real
// runtime.Core coder runner for one todo. The coord.* stream still carries the
// canonical sequence, while the shared bus also sees the real task events
// (context.built, assistant.message, task.completed) produced by the agent.
func TestRuntimeAgentRunnerSpawnsCoder(t *testing.T) {
	repo := t.TempDir()
	bus := event.New()
	defer func() { _ = bus.Close() }()

	rec := newRecordingSub(bus)

	// Capture the runtime-level events emitted by the coder agent too.
	allCh := bus.Subscribe(event.Topic(">"))
	var sawContextBuilt, sawAssistantMsg bool
	go func() {
		for env := range allCh {
			if env.Evt.Type() == "context.built" {
				sawContextBuilt = true
			}
			if env.Evt.Type() == "assistant.message" {
				sawAssistantMsg = true
			}
		}
	}()

	runner := newRuntimeAgentRunner(repo, cognitive.NewStubProvider(128_000), bus)
	plan := coord.Plan{ID: "p-1", Goal: "explain repo"}
	plan.Todos = append(plan.Todos, coord.Todo{
		ID: "t1", Title: "summarize", Assignee: string(coord.RoleCoder), Status: coord.Pending,
	})

	o := coord.NewOrchestrator(
		coord.Config{MaxReworkCycles: 1, Concurrency: 1},
		cannedPlanner{plan: plan},
		bus, bus, runner,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := o.Run(ctx, plan.Goal); err != nil && !strings.Contains(err.Error(), "context") {
		t.Fatalf("orchestrator Run: %v", err)
	}
	_ = bus.Close()
	rec.close() // wait for drain to finish before reading rec.types

	if len(rec.types) == 0 || rec.types[0] != "coord.plan.ready" {
		t.Fatalf("first event = %v, want coord.plan.ready", rec.types)
	}
	seq := rec.byTodo["t1"]
	if len(seq) < 3 {
		t.Fatalf("todo t1 events = %v, want >=3", seq)
	}
	got := joinTypes(seq)
	for _, want := range []string{"coord.task.assign", "coord.code.ready", "coord.review.verdict", "coord.test.report"} {
		if !strings.Contains(got, want) {
			t.Fatalf("todo t1 missing %s; got %s", want, got)
		}
	}

	if !sawContextBuilt {
		t.Error("coder agent did not emit context.built")
	}
	if !sawAssistantMsg {
		t.Error("coder agent did not emit assistant.message")
	}
}
