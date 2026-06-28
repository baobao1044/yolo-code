// Tests for L11-007 — Board wiring: coord.* events populate the TUI board
// (roadmap §15.13 L11-007, depends on TUI-009).
//
// This cross-package test proves the §12.3.3 contract: the orchestrator
// publishes the canonical 5-event sequence (plan.ready → task.assign →
// code.ready → review.verdict → test.report) on the real event bus, in the
// order the TUI board consumes (TUI-009's fold already pins that the board
// opens on plan.ready and advances on the subsequent events). The test does
// NOT drive the TUI directly — fold is unexported, and TUI-009's board_test
// already pins the consumption side. This test pins the PRODUCTION side: the
// orchestrator is the publisher TUI-009 subscribes to.
//
// No headless.go change (CLI wiring deferred to the integration sprint, spec
// gap documented). The test runs the orchestrator with fake agents against a
// real bus and asserts the captured coord.* stream matches the canonical turn.
//
// This test lives in cmd/yolo (the composition-root package) so it can import
// both internal/coord (the producer) and internal/event (the bus) — the same
// package the integration sprint will wire the real adapter in.

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/coord"
	"github.com/yolo-code/yolo/internal/event"
)

// recordingSub subscribes coord.> and records every event type in arrival
// order. It is the board's stand-in: a consumer that captures the coord.*
// stream the orchestrator publishes.
type recordingSub struct {
	ch     <-chan event.Envelope
	types  []event.Topic
	byTodo map[string][]event.Topic
}

// newRecordingSub subscribes coord.> on the bus and drains in a goroutine,
// recording event types in arrival order + per-todo type sequences.
func newRecordingSub(bus *event.Bus) *recordingSub {
	ch := bus.Subscribe(event.Topic("coord.>"))
	rs := &recordingSub{ch: ch, byTodo: map[string][]event.Topic{}}
	go rs.drain()
	return rs
}

func (r *recordingSub) drain() {
	for env := range r.ch {
		r.types = append(r.types, env.Evt.Type())
		switch e := env.Evt.(type) {
		case *event.TaskAssignEvent:
			r.byTodo[e.TodoID] = append(r.byTodo[e.TodoID], env.Evt.Type())
		case *event.CodeReadyEvent:
			r.byTodo[e.TodoID] = append(r.byTodo[e.TodoID], env.Evt.Type())
		case *event.ReviewVerdictEvent:
			r.byTodo[e.TodoID] = append(r.byTodo[e.TodoID], env.Evt.Type())
		case *event.TestReportEvent:
			r.byTodo[e.TodoID] = append(r.byTodo[e.TodoID], env.Evt.Type())
		}
	}
}

// close stops the drain goroutine (the bus close ends the range).
func (r *recordingSub) close() {}

// fakeAgentRunner publishes canned coord.* events per role, exactly as the
// board expects them. It publishes to the bus (the recordingSub reads it).
type fakeAgentRunner struct {
	bus       coord.EventPublisher // the bus
	codeReady map[string]string
	verdict   bool
	testPass  bool
}

func (r *fakeAgentRunner) Run(ctx context.Context, role coord.Role, task event.TaskAssignEvent) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch role {
	case coord.RoleCoder:
		_ = r.bus.Publish(ctx, &event.CodeReadyEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Diff: r.codeReady[task.TodoID], SelfReport: "done",
		})
	case coord.RoleReviewer:
		_ = r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: r.verdict,
		})
	case coord.RoleTester:
		_ = r.bus.Publish(ctx, &event.TestReportEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Passed: r.testPass, Output: "ok",
		})
	}
	return nil
}

// cannedPlanner implements coord.Planner with a 2-todo plan.
type cannedPlanner struct {
	plan coord.Plan
}

func (p cannedPlanner) Plan(_ context.Context, _ string) (coord.Plan, coord.Mode, error) {
	return p.plan, coord.Multi, nil
}

// TestL11_007_CoordEventsCanonicalOrder: the orchestrator publishes the
// canonical 5-event sequence on the bus: plan.ready first, then per todo
// task.assign → code.ready → review.verdict → test.report. This is the stream
// the TUI board (TUI-009) consumes.
func TestL11_007_CoordEventsCanonicalOrder(t *testing.T) {
	bus := event.New()
	runner := &fakeAgentRunner{
		bus:       bus,
		codeReady: map[string]string{"t1": "diff1", "t2": "diff2"},
		verdict:   true,
		testPass:  true,
	}
	plan := coord.Plan{ID: "p", Goal: "refactor X, add tests, fix CI"}
	plan.Todos = append(plan.Todos,
		coord.Todo{ID: "t1", Title: "implement X", Assignee: "coder", Status: coord.Pending},
		coord.Todo{ID: "t2", Title: "add tests", Assignee: "coder", Status: coord.Pending},
	)
	rec := newRecordingSub(bus)
	defer rec.close()

	o := coord.NewOrchestrator(coord.Config{MaxReworkCycles: 3, Concurrency: 1},
		cannedPlanner{plan: plan}, bus, bus, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := o.Run(ctx, plan.Goal); err != nil {
		t.Fatalf("orchestrator Run: %v", err)
	}
	// Give the recording goroutine a moment to drain the last events.
	_ = bus.Close()
	time.Sleep(10 * time.Millisecond)

	// plan.ready must be the FIRST event.
	if len(rec.types) == 0 || rec.types[0] != "coord.plan.ready" {
		t.Fatalf("first event = %v, want coord.plan.ready (the board opens on this)", rec.types)
	}
	// Every todo must have seen the full assigned→coded→approved→tested sequence.
	for _, id := range []string{"t1", "t2"} {
		seq := rec.byTodo[id]
		if len(seq) < 3 {
			t.Errorf("todo %s: only %d coord.* events %v, want ≥3 (assign/code/verdict/report)", id, len(seq), seq)
			continue
		}
		// task.assign (the board's row-create) is published by the orchestrator
		// BEFORE code.ready; it's recorded under the same todo id.
		if got := joinTypes(seq); !strings.Contains(got, "coord.task.assign") ||
			!strings.Contains(got, "coord.code.ready") ||
			!strings.Contains(got, "coord.review.verdict") ||
			!strings.Contains(got, "coord.test.report") {
			t.Errorf("todo %s seq = %s, want all of {assign, code.ready, review.verdict, test.report}", id, got)
		}
		// task.assign precedes code.ready (the board row appears before the code event).
		assignAt, codeAt := indexOf(rec.types, "coord.task.assign"), indexOf(rec.types, "coord.code.ready")
		if assignAt >= 0 && codeAt >= 0 && assignAt > codeAt {
			t.Errorf("task.assign (idx %d) after code.ready (idx %d) — board row would appear after the code event", assignAt, codeAt)
		}
	}
	// Both todos ended Done (the canonical happy path).
	if got := len(rec.byTodo); got != 2 {
		t.Errorf("distinct todos in coord.* stream = %d, want 2", got)
	}
}

// TestL11_007_BoardContractMatchesTUI009: the event types the orchestrator
// publishes are EXACTLY the five topics TUI-009 subscribes to (coord.plan.ready,
// coord.task.assign, coord.code.ready, coord.review.verdict, coord.test.report).
// This guards the contract: a new coord.* topic the TUI doesn't fold would be
// invisible on the board; a topic the TUI folds but the orchestrator never
// publishes would leave the board stuck.
func TestL11_007_BoardContractMatchesTUI009(t *testing.T) {
	bus := event.New()
	runner := &fakeAgentRunner{
		bus:       bus,
		codeReady: map[string]string{"t1": "d"},
		verdict:   true,
		testPass:  true,
	}
	plan := coord.Plan{ID: "p", Todos: []coord.Todo{{ID: "t1", Title: "x", Assignee: "coder", Status: coord.Pending}}}
	rec := newRecordingSub(bus)
	defer rec.close()
	o := coord.NewOrchestrator(coord.Config{Concurrency: 1}, cannedPlanner{plan: plan}, bus, bus, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = o.Run(ctx, plan.Goal)
	_ = bus.Close()
	time.Sleep(10 * time.Millisecond)

	// The set of published topics must be a subset of the TUI-009 fold topics.
	tui009Topics := map[event.Topic]bool{
		"coord.plan.ready":     true,
		"coord.task.assign":    true,
		"coord.code.ready":     true,
		"coord.review.verdict": true,
		"coord.test.report":    true,
	}
	for _, tp := range rec.types {
		if !tui009Topics[tp] {
			t.Errorf("orchestrator published %q, which TUI-009's board does not fold — board would drop it", tp)
		}
	}
	// And the orchestrator must have published ALL five (a complete canonical run).
	published := map[event.Topic]bool{}
	for _, tp := range rec.types {
		published[tp] = true
	}
	for want := range tui009Topics {
		if !published[want] {
			t.Errorf("orchestrator never published %q — TUI-009 folds it but the board would never see it", want)
		}
	}
}

// joinTypes joins the type slice with " > " for a readable failure message.
func joinTypes(ts []event.Topic) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			b.WriteString(" > ")
		}
		b.WriteString(string(t))
	}
	return b.String()
}

// indexOf returns the first index of topic in the slice, or -1.
func indexOf(ts []event.Topic, want event.Topic) int {
	for i, t := range ts {
		if t == want {
			return i
		}
	}
	return -1
}
