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
	// The patch tool is a high-risk write and now parks on the HITL gate like
	// any other. This test drives the runner directly, so neither of the two
	// things that answer that prompt in production (the TUI human, the plan
	// runner's refuse-on-stall watcher) is present and the run would stall.
	// Opt the class out — the subject here is coder → reviewer → tester →
	// merge, not the gate.
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "true")

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
		withPatches(map[string]string{todoID: patchBody})
	// The runner allocates a shadow tree on its first buildAdapters and has no
	// teardown of its own. Cleanup-time, not inline: the patch engine below
	// checkpoints into the tree and reads it back during the run.
	t.Cleanup(func() { _ = runner.close() })

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
	// done signals when the subscriber goroutine has drained the channel, so
	// the main goroutine reads the flags only after all writes are complete
	// (fixes a pre-existing race: the subscriber wrote flags while the main
	// goroutine read them with only a time.Sleep for synchronization).
	done := make(chan struct{})
	go func() {
		defer close(done)
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
			case "coord.plan.done":
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
	<-done // wait for the subscriber to finish before reading the flags

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
