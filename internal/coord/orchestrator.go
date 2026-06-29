// orchestrator.go — the Orchestrator + rework cap (File 12 §12.4, §12.4.1).
//
// The orchestrator is an agent whose role is decompose, delegate, track,
// merge. It runs the canonical 5-event loop:
//
//   Planner.Plan(goal) → publish plan.ready → DispatchReady →
//     per todo: spawn coder (AgentRunner) → coder publishes code.ready →
//       spawn reviewer (direct) → reviewer publishes review.verdict →
//         approved? spawn tester → tester publishes test.report →
//           pass? MarkDone + dispatch dependents / rework
//         rejected? reassignCoder (ReworkCycles++, cap → Failed)
//
// The rework cap (MaxReworkCycles, default 3) escalates a stuck todo to
// Failed instead of looping forever (File 12 §12.4.1). Reviewer/Tester are
// spawned DIRECTLY via the AgentRunner seam (no review.request/test.request
// events on the bus — Decision 2, spec gap: those events aren't in the §5.4.7
// catalog).
//
// Sprint 10 uses fake agents (AgentRunner seam) that publish canned events
// synchronously; the real per-agent drive is the integration sprint.
//
// Spec gap: the typed Plan is marshaled to json.RawMessage on publish (the
// event contract, File 05, is unchanged — PlanReadyEvent.Plan is RawMessage).

package coord

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Orchestrator owns the Plan, the scheduler, and the coord.> subscription.
// It is the File 12 §12.4 loop. Lifecycle: NewOrchestrator subscribes coord.>
// (before any publisher can miss an event); Start initializes the done
// channel (idempotent); Run runs the blocking event loop (closes done on
// return); Stop waits for done (idempotent, ctx-bound — mirrors infra).
type Orchestrator struct {
	cfg      Config
	planner  Planner
	sub      Subscribable
	pub      EventPublisher
	runner   AgentRunner
	Verifier Verifier // optional; when set, triggers merge when all todos are done

	plan  *Plan
	sched *Scheduler
	diffs map[string]string // per-todo diff collected from CodeReadyEvent

	// ch is the coord.> subscription; Run drains it.
	ch <-chan event.Envelope

	done     chan struct{}
	stopOnce sync.Once
	stopErr  error

	log *slog.Logger
}

// NewOrchestrator wires the orchestrator and subscribes coord.> BEFORE any
// publisher can miss an event (mirrors infra.Start's subscribe-then-launch
// ordering). The caller owns the bus; closing it ends the ch range.
func NewOrchestrator(cfg Config, planner Planner, sub Subscribable, pub EventPublisher, runner AgentRunner) *Orchestrator {
	cfg = defaultConfig(cfg)
	ch := sub.Subscribe(event.Topic("coord.>"))
	return &Orchestrator{
		cfg:     cfg,
		planner: planner,
		sub:     sub,
		pub:     pub,
		runner:  runner,
		ch:      ch,
		log:     slog.Default(),
	}
}

// Start initializes the done channel. Idempotent (a second Start is a no-op).
// Run calls it implicitly if the caller didn't, but the test harness calls
// Start before Run to mirror the infra Start/Run split.
func (o *Orchestrator) Start(_ context.Context) {
	if o.done == nil {
		o.done = make(chan struct{})
	}
}

// Run decomposes goal into a Plan, publishes plan.ready, dispatches the
// ready todos, and drains the coord.> event loop until AllDone (returns nil)
// or ctx is canceled (returns ctx.Err() after cancelAll). It blocks the
// caller. Closes done on return so Stop can wait.
func (o *Orchestrator) Run(ctx context.Context, goal string) error {
	o.Start(ctx)
	defer close(o.done)

	plan, _, err := o.planner.Plan(ctx, goal)
	if err != nil {
		return err
	}
	o.plan = &plan
	o.sched = NewScheduler(o.plan, o.cfg.Concurrency)

	// Publish plan.ready once (Plan marshaled to RawMessage; spec gap logged).
	if err := o.publishPlan(ctx); err != nil {
		return err
	}

	// Kick off the first wave of todos.
	o.dispatchReady(ctx)

	// Event loop: drain coord.> until the plan is AllDone or ctx cancels.
	for {
		if o.plan.AllDone() {
			return nil
		}
		select {
		case <-ctx.Done():
			return o.cancelAll(ctx)
		case env, ok := <-o.ch:
			if !ok {
				return nil // bus closed → treat as done
			}
			o.handle(ctx, env)
		}
	}
}

// handle routes one agent-produced event to its handler (File 12 §12.4 switch).
func (o *Orchestrator) handle(ctx context.Context, env event.Envelope) {
	switch e := env.Evt.(type) {
	case *event.CodeReadyEvent:
		o.requestReview(ctx, *e)
	case *event.ReviewVerdictEvent:
		if e.Approved {
			o.requestTest(ctx, *e)
		} else {
			o.reassignCoder(ctx, *e)
		}
	case *event.TestReportEvent:
		if e.Passed {
			o.markDone(ctx, e.TodoID)
		} else {
			o.reassignWithTestFail(ctx, *e)
		}
	}
}

// dispatchReady dispatches all ready todos, spawning a coder per todo. The
// spawn publishes task.assign (so the TUI board gets a row) then runs the
// coder (which publishes code.ready).
func (o *Orchestrator) dispatchReady(ctx context.Context) {
	o.sched.DispatchReady(func(td *Todo) {
		o.spawnCoder(ctx, td)
	})
}

// spawnCoder publishes task.assign for the todo, then runs the coder (which
// publishes code.ready). Publish happens BEFORE the run so the board row
// appears before the code event.
func (o *Orchestrator) spawnCoder(ctx context.Context, td *Todo) {
	task := event.TaskAssignEvent{
		PlanID:    o.plan.ID,
		TodoID:    td.ID,
		Agent:     string(RoleCoder),
		Brief:     td.Title,
		Artifacts: td.Artifacts,
	}
	_ = o.pub.Publish(ctx, &task)
	_ = o.runner.Run(ctx, RoleCoder, task)
}

// requestReview spawns a reviewer for the todo's diff (File 12 §12.3.3). The
// reviewer is spawned DIRECTLY — no review.request event (spec gap, Decision 2).
func (o *Orchestrator) requestReview(ctx context.Context, e event.CodeReadyEvent) {
	if o.diffs == nil {
		o.diffs = make(map[string]string)
	}
	o.diffs[e.TodoID] = e.Diff
	td := o.plan.Todo(e.TodoID)
	var artifacts []string
	if td != nil {
		artifacts = td.Artifacts
	}
	task := event.TaskAssignEvent{
		PlanID: e.PlanID, TodoID: e.TodoID, Agent: string(RoleReviewer),
		Brief:     e.Diff, // the reviewer audits the coder's diff
		Artifacts: artifacts,
	}
	_ = o.runner.Run(ctx, RoleReviewer, task)
}

// requestTest spawns a tester for the todo (approved by the reviewer).
func (o *Orchestrator) requestTest(ctx context.Context, e event.ReviewVerdictEvent) {
	td := o.plan.Todo(e.TodoID)
	var artifacts []string
	if td != nil {
		artifacts = td.Artifacts
	}
	task := event.TaskAssignEvent{
		PlanID: e.PlanID, TodoID: e.TodoID, Agent: string(RoleTester),
		Artifacts: artifacts,
	}
	_ = o.runner.Run(ctx, RoleTester, task)
}

// reassignCoder re-dispatches the coder with the reviewer's comments, capped
// at MaxReworkCycles (File 12 §12.4.1). On cap exceedance the todo is Failed
// and surfaced (no infinite retry).
func (o *Orchestrator) reassignCoder(ctx context.Context, v event.ReviewVerdictEvent) {
	td := o.plan.Todo(v.TodoID)
	if td == nil {
		return
	}
	td.ReworkCycles++
	if td.ReworkCycles > o.cfg.MaxReworkCycles {
		o.sched.MarkFailed(td.ID, func(newTD *Todo) { o.spawnCoder(ctx, newTD) })
		o.log.Warn("rework cap exceeded", "todo", td.ID, "cycles", td.ReworkCycles)
		return
	}
	task := event.TaskAssignEvent{
		PlanID: o.plan.ID, TodoID: td.ID, Agent: string(RoleCoder),
		Brief:     td.Title + "\n\nReviewer comments:\n" + strings.Join(v.Comments, "\n"),
		Artifacts: td.Artifacts,
	}
	_ = o.pub.Publish(ctx, &task)
	_ = o.runner.Run(ctx, RoleCoder, task)
}

// reassignWithTestFail re-dispatches the coder after a test failure (treated
// as a rework cycle, same cap).
func (o *Orchestrator) reassignWithTestFail(ctx context.Context, e event.TestReportEvent) {
	td := o.plan.Todo(e.TodoID)
	if td == nil {
		return
	}
	td.ReworkCycles++
	if td.ReworkCycles > o.cfg.MaxReworkCycles {
		o.sched.MarkFailed(td.ID, func(newTD *Todo) { o.spawnCoder(ctx, newTD) })
		o.log.Warn("rework cap exceeded (test fail)", "todo", td.ID, "cycles", td.ReworkCycles)
		return
	}
	task := event.TaskAssignEvent{
		PlanID: o.plan.ID, TodoID: td.ID, Agent: string(RoleCoder),
		Brief:     td.Title + "\n\nTest output:\n" + e.Output,
		Artifacts: td.Artifacts,
	}
	_ = o.pub.Publish(ctx, &task)
	_ = o.runner.Run(ctx, RoleCoder, task)
}

// markDone marks the todo Done and re-dispatches dependents. When all todos
// are terminal and a verifier is wired, the orchestrator merges the
// collected diffs and publishes plan.done.
func (o *Orchestrator) markDone(ctx context.Context, todoID string) {
	o.sched.MarkDone(todoID, func(td *Todo) { o.spawnCoder(ctx, td) })
	if o.Verifier == nil {
		return
	}
	if !o.plan.AllDone() {
		return
	}
	merged, err := Merge(ctx, o.plan, o.diffs, o.Verifier)
	done := err == nil && merged.Verified
	summary := "merged"
	if !done {
		summary = err.Error()
	}
	_ = o.pub.Publish(ctx, &event.PlanDoneEvent{
		PlanID:  o.plan.ID,
		Done:    done,
		Merged:  merged.Merged(),
		Summary: summary,
	})
}

// MergedReport is exported so PlanDoneEvent can report whether a merge
// actually ran/hash conflicts.
func (mp MergedPatch) Merged() bool { return mp.Summary.Done > 0 }

// publishPlan marshals the typed Plan to RawMessage and publishes plan.ready.
func (o *Orchestrator) publishPlan(ctx context.Context) error {
	raw, err := json.Marshal(o.plan)
	if err != nil {
		return err
	}
	return o.pub.Publish(ctx, &event.PlanReadyEvent{PlanID: o.plan.ID, Plan: raw})
}

// cancelAll is the cancellation path (File 12 §12.4.2): mark every inflight
// todo Failed and return ctx.Err(). Real per-agent context cancel + patch
// rollback is the integration sprint; Sprint 10 marks todos terminal.
func (o *Orchestrator) cancelAll(ctx context.Context) error {
	for i := range o.plan.Todos {
		if o.plan.Todos[i].Status == InProgress {
			o.plan.Todos[i].Status = Failed
		}
	}
	return ctx.Err()
}

// Stop waits for the event loop to exit (done closes when Run returns) or
// ctx's deadline. Idempotent (sync.Once). Mirrors infra.Stop.
func (o *Orchestrator) Stop(ctx context.Context) error {
	o.stopOnce.Do(func() {
		if o.done == nil {
			return // Start never called; nothing to wait for.
		}
		select {
		case <-o.done:
		case <-ctx.Done():
			o.stopErr = ctx.Err()
		}
	})
	return o.stopErr
}

// Done returns a channel that closes when the event loop exits (Run returns).
// Used by the no-leak exit bar (mirrors infra's done).
func (o *Orchestrator) Done() <-chan struct{} {
	return o.done
}
