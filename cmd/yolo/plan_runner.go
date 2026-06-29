// Sprint 13 S13-004: the `--plan <goal>` CLI path. Complex (Multi-mode)
// goals are decomposed and executed by the coord orchestrator, with real
// per-role agents wired by runtimeAgentRunner. Simple goals fall back to the
// single-agent headless path in main.go. Output mirrors the headless JSONL
// transcript so downstream tooling stays uniform.

package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"

	coordpkg "github.com/yolo-code/yolo/internal/coord"
	"github.com/yolo-code/yolo/internal/event"
)

// runPlanCtx runs the orchestrator against goal and returns the JSONL event
// transcript. The caller owns the context; cancellation stops the plan.
func runPlanCtx(ctx context.Context, goal string) (string, error) {
	bus := event.New()
	costPub := newCostPublisher(bus)
	costPub.Start(ctx)

	// The subscriber must exist before any event is published.
	ch := bus.Subscribe(event.Topic(">"))

	var out strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		enc := json.NewEncoder(&out)
		for env := range ch {
			_ = enc.Encode(projectEnvelope(env))
		}
	}()

	repo, err := os.Getwd()
	if err != nil {
		_ = bus.Close()
		costPub.Stop()
		wg.Wait()
		return "", err
	}

	runner := newRuntimeAgentRunner(repo, resolveProvider(), bus).
		withCost(costPub)
	planner := &heuristicPlanner{}

	o := coordpkg.NewOrchestrator(
		coordpkg.Config{MaxReworkCycles: 3, Concurrency: 1},
		planner,
		bus, bus,
		runner,
	)
	o.Verifier = mergeVerifier{}

	runErr := o.Run(ctx, goal)

	_ = bus.Close()
	wg.Wait()
	costPub.Stop()

	if runErr != nil {
		return out.String(), runErr
	}
	return out.String(), nil
}
