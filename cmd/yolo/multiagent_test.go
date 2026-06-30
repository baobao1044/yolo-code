// Multi-agent end-to-end regression (Sprint 13 S13-002).
// A real planner produces one todo, the runtime-backed runner dispatches
// coder → reviewer → tester, and the orchestrator merges the result.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
)

func TestMultiAgentEndToEndPatchReviewTestMerge(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module testrepo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := event.New()
	defer func() { _ = bus.Close() }()

	costPub := newCostPublisher(bus)
	costPub.Start(context.Background())
	defer costPub.Stop()

	goal := "/plan add hello.go"
	todoID := "plan-1-t1" // heuristic planner naming
	patchBody := "package app\n\nfunc Hello() string { return \"hi\" }\n"

	runner := newRuntimeAgentRunner(repo, nil, bus).
		withCost(costPub).
		withPatches(map[string]string{todoID: patchBody})

	o := coord.NewOrchestrator(
		coord.Config{MaxReworkCycles: 1, Concurrency: 1},
		&heuristicPlanner{},
		bus, bus, runner,
	)
	o.Verifier = mergeVerifier{}

	ch := bus.Subscribe(event.Topic(">"))
	var (
		assign, codeReady, review, testReport, planDone, costIncurred bool
	)
	go func() {
		for env := range ch {
			switch env.Evt.Type() {
			case "coord.task.assign":
				assign = true
			case "coord.code.ready":
				codeReady = true
			case "coord.review.verdict":
				review = true
			case "coord.test.report":
				testReport = true
			case "plan.done":
				planDone = true
			case "cost.incurred":
				costIncurred = true
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := o.Run(ctx, goal); err != nil && !strings.Contains(err.Error(), "context") {
		t.Fatalf("orchestrator Run: %v", err)
	}
	_ = bus.Close()
	time.Sleep(20 * time.Millisecond)

	if !assign {
		t.Error("missing coord.task.assign")
	}
	if !codeReady {
		t.Error("missing coord.code.ready")
	}
	if !review {
		t.Error("missing coord.review.verdict")
	}
	if !testReport {
		t.Error("missing coord.test.report")
	}
	if !planDone {
		t.Error("missing plan.done")
	}
	if !costIncurred {
		t.Error("missing cost.incurred")
	}

	content, err := os.ReadFile(filepath.Join(repo, "hello.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "func Hello()") {
		t.Fatalf("file was not patched:\n%s", content)
	}
}
