// Sprint 13 S13-004: the `--plan <goal>` CLI path. Complex (Multi-mode)
// goals are decomposed and executed by the coord orchestrator, with real
// per-role agents wired by runtimeAgentRunner. Simple goals fall back to the
// single-agent headless path in main.go. Output mirrors the headless JSONL
// transcript so downstream tooling stays uniform.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/infra"
)

// runPlanCtx runs the orchestrator against goal and returns the JSONL event
// transcript. The caller owns the context; cancellation stops the plan.
func runPlanCtx(ctx context.Context, goal string) (string, error) {
	// The repo root is --repo / YOLO_REPO_ROOT when set, the working directory
	// otherwise. It is resolved first because both infra's permission policy and
	// the agent runner's sandbox are confined to it.
	repo, err := repoRoot()
	if err != nil {
		return "", err
	}

	// YOLO_EVENT_LOG (--event-log) makes the plan replayable after a crash;
	// unset keeps the in-memory bus.
	bus, err := newBus()
	if err != nil {
		return "", err
	}

	// L12: start telemetry / metrics / permissions / redaction. Without this the
	// whole infrastructure layer is dead for `--plan` runs.
	infraCfg := infra.DefaultConfig()
	infraCfg.Permissions.Root = repo
	inf, err := infra.Start(ctx, bus, infraCfg)
	if err != nil {
		return "", fmt.Errorf("start infrastructure: %w", err)
	}
	// Stop runs on a Background-derived deadline: on Ctrl+C ctx is already
	// cancelled and the flushes would get no budget at all. The bus is closed
	// before it (below and here) so the observers see the queued tail first.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = inf.Stop(stopCtx)
	}()

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

	// Shutdown is one ordered block rather than a chain of defers, because the
	// transcript has to be complete before the caller reads out.String() (a
	// deferred drain runs too late for that) and because the order is load
	// bearing, top to bottom:
	//
	//  1. the cost publisher stops first — after bus.Close its last accruals
	//     lose to ErrBusClosed and never reach the transcript;
	//  2. the bus closes, which ends the subscriber ranges and drains the queued
	//     tail into both the transcript goroutine and infra's root subscriber;
	//  3. the transcript goroutine is joined, so out is complete.
	//
	// inf.Stop is the deferred step registered above, so it runs after all three
	// and flushes observers that have already seen every event.
	shutdown := func() {
		costPub.Stop()
		_ = bus.Close()
		wg.Wait()
		reportDropped(bus)
	}

	// Fail fast on a missing provider: aborting here beats discovering it at the
	// first Stream call, several agents into a plan.
	provider, err := resolveProvider()
	if err != nil {
		shutdown()
		return "", err
	}

	runner := newRuntimeAgentRunner(repo, provider, bus)
	// The runner allocates a shadow tree under os.MkdirTemp on its first
	// buildAdapters and has no teardown hook of its own, so every `--plan`
	// invocation left one directory behind on the user's machine.
	//
	// Runner teardown is the only correct point — buildAdapters hands the same
	// snap to every todo and role, and the patch engine reads the tree back
	// mid-todo to roll a failed verification out — and o.Run returning is the
	// earliest that point arrives. Agent turns run on their own goroutines, but
	// Run's terminal path cancels them and waits (coord.Orchestrator.finish →
	// quiesceAgents) precisely so the caller can tear the runner down the moment
	// Run returns. The two exits that skip that join, a planner or plan.ready
	// error, both precede the first dispatch, so nothing is inflight there either.
	//
	// Deferred rather than closed after o.Run so the provider-error return below
	// and both returns at the end are all covered. Ordering against the rest of
	// the teardown is safe in either direction: shutdown() (costPub.Stop,
	// bus.Close, drain) and the deferred inf.Stop above touch only the bus and
	// the observers hanging off it, never the shadow tree.
	defer func() { _ = runner.close() }()
	planner := &heuristicPlanner{}

	o := coordpkg.NewOrchestrator(
		coordpkg.Config{MaxReworkCycles: 3, Concurrency: 1},
		planner,
		bus, bus,
		runner,
	)
	o.Verifier = mergeVerifier{}

	runErr := o.Run(ctx, goal)

	shutdown()

	if runErr != nil {
		return out.String(), runErr
	}
	return out.String(), nil
}
