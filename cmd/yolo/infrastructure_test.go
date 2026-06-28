// Tests for L12-009 composition-root wiring — the Sprint 8 exit bar (File 13
// §13.1, roadmap §15.12.2): a headless run with Infra wired exports full
// observability from the same event stream the transcript already uses, with
// zero agent-logic changes — the runtime publishes the events it always has;
// Infra only observes them. The transcript stays byte-identical (Infra is a
// pure observer, §13.1.2); the aggregate's Tel/Metrics populate from the run,
// and Stop returns nil with no goroutine leak.
//
// The wiring lives in runHeadlessDeps (the composition root): when the caller
// injects an *infra.Infra, Start subscribes the root topic on the shared bus
// and Stop runs in the close chain after the bus is closed (so the subscriber
// range ends → done closes → Stop's wait returns promptly, no leak). The
// layer-port adapters (Secrets→exec, Perms/Limiter→exec dispatch, Cost→cognitive)
// are deferred to the integration sprint per the existing §15.9.2 deferral; this
// test proves the lifecycle + observability wiring, the L12-009 exit bar.

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/infra"
	"github.com/yolo-code/yolo/internal/runtime"
)

// TestHeadlessWithInfraObservesRun is the Sprint 8 exit bar: a headless run
// with Infra wired produces the deterministic transcript AND the Infra
// aggregate observed it — Tel records ≥1 span (task.started), Metrics counts
// events.total per topic, and Stop returns nil with no goroutine leak. The
// transcript is unchanged from the unwired run (Infra is a pure observer).
//
// Both runs use the SAME injected cognitive core so the only variable is Infra.
// The baseline passes a stub-only deps (same bus, no Infra); the wired pass
// adds Infra. If Infra perturbed the stream the transcripts would diverge.
func TestHeadlessWithInfraObservesRun(t *testing.T) {
	prompt := "say hi\n"
	stub := runtime.StubCognitive{Answer: "say hi"}

	// The unwired baseline: same input + stub core, no Infra. Shares the bus
	// shape so the only difference in the wired run is the Infra aggregate.
	busBase := event.New()
	baseOut, err := runHeadlessDeps(context.Background(), bytes.NewBufferString(prompt), 0, &headlessDeps{
		bus: busBase,
		cog: stub,
	})
	if err != nil {
		t.Fatalf("baseline runHeadlessDeps: %v", err)
	}

	// Wired run: a bus we own (so Infra subscribes the SAME bus the runtime
	// publishes to) + an Infra aggregate Start/Stop'd around the drive.
	bus := event.New()
	cfg := infra.DefaultConfig()
	in, err := infra.Start(context.Background(), bus, cfg)
	if err != nil {
		t.Fatalf("infra.Start: %v", err)
	}
	out, err := runHeadlessDeps(context.Background(), bytes.NewBufferString(prompt), 0, &headlessDeps{
		bus:   bus,
		cog:   stub,
		infra: in,
	})
	if err != nil {
		t.Fatalf("wired runHeadlessDeps: %v", err)
	}

	// 1) Transcript unchanged — Infra is a pure observer (no events added, no
	//    seq renumbered). S5 byte-identical property holds.
	if out != baseOut {
		t.Errorf("transcript changed when Infra was wired (pure-observer invariant broken)\nbase:\n%s\nwired:\n%s", baseOut, out)
	}

	// 2) Infra observed the run: Tel recorded ≥1 span (task.started fires first).
	if got := len(in.Tel.Spans()); got == 0 {
		t.Error("Infra.Tel.Spans is empty — the root subscriber didn't fan out (Start didn't subscribe the shared bus, or the bus wasn't the one the runtime publishes to)")
	}

	// 3) Metrics: events.total across topics > 0 (task.started at minimum).
	var total int64
	for topic := range topicsOf(baseOut) {
		total += in.Metrics.Counter("events.total", map[string]string{"topic": topic})
	}
	if total == 0 {
		t.Error("events.total is 0 across the run's topics — Metrics didn't record (no fan-out)")
	}

	// 4) Stop returns nil promptly (the bus was closed in runHeadlessDeps, so
	//    the subscriber range ended → done closed → Stop's wait returns fast).
	stopDeadline, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := in.Stop(stopDeadline); err != nil {
		t.Fatalf("Infra.Stop after headless run: %v (goroutine did not exit — leak)", err)
	}
}

// topicsOf extracts the set of "type=X" values from a headless transcript so the
// test can ask Metrics for each topic's events.total without hardcoding the
// exact event sequence. The transcript lines look like:
//
//	{"seq":1,"type":"task.started","task":"t_1","evt":{...}}
func topicsOf(transcript string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, line := range strings.Split(transcript, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, `"type":"`)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(`"type":"`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			continue
		}
		set[rest[:end]] = struct{}{}
	}
	return set
}
