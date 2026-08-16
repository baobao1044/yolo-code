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
// Threading: the event loop goroutine owns every field below (plan, sched,
// diffs, runErr, turns). Agent turns run on their OWN goroutines — never on
// the loop — for three reasons: an inflight turn must not stop the loop from
// draining its own subscription (a turn that publishes into a full coord.>
// buffer would otherwise wedge against the only goroutine that could drain it,
// see internal/event/bus.go's reentrancy note); Concurrency > 1 has to mean
// something; and a turn needs its own deadline. Those goroutines touch nothing
// but their snapshotted task and the results channel, and Run does not return
// until every one of them has.
//
// Spec gap: the typed Plan is marshaled to json.RawMessage on publish (the
// event contract, File 05, is unchanged — PlanReadyEvent.Plan is RawMessage).

package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// ErrPlanFailed reports that the run terminated with at least one todo in the
// Failed terminal state. Run wraps it with the failed todo IDs. Without it a
// caller cannot tell a plan that gave up from a plan that succeeded: AllDone
// counts Failed as terminal, so the loop exiting is not evidence of success.
var ErrPlanFailed = errors.New("coord: plan finished with failed todos")

// ErrAgentPanic reports that an agent turn panicked. The panic is converted
// into an ordinary todo failure (File 12's invariant: one todo's failure must
// not abandon the rest of the plan) and carries the recovered value plus the
// stack — a panic flattened into a bare "failed" throws away the only evidence
// of what broke.
var ErrAgentPanic = errors.New("coord: agent panicked")

// ErrAgentTimeout reports that an agent turn burned its per-turn cap
// (Config.AgentTimeout / RoleTimeouts) without erroring and without producing
// the event the todo was waiting for. Before the cap existed, that agent
// stalled the plan until the parent context expired — which in production has
// no deadline, so it stalled forever with no diagnostic.
var ErrAgentTimeout = errors.New("coord: agent turn timed out")

// planDonePublishTimeout bounds the terminal plan.done publish. It runs after
// the event loop has stopped draining, on a context deliberately detached from
// the run's (see finish), so it needs a deadline of its own — otherwise a
// backed-up subscriber turns "the run is over" into a hang.
const planDonePublishTimeout = 5 * time.Second

// termination is why the event loop stopped. It decides what plan.done says:
// a cancelled plan has not failed, and a bus that closed under the plan is a
// different accident from a plan that ran to the end.
type termination int

const (
	// termComplete: every todo reached a terminal state (Done or Failed).
	termComplete termination = iota
	// termCanceled: the caller's context was canceled or expired.
	termCanceled
	// termBusClosed: the bus closed before the plan finished.
	termBusClosed
)

// agentResult is one agent goroutine reporting back to the event loop. Only
// failures are sent: a turn that succeeded has already published its own
// coord.* event, which is the loop's real signal.
type agentResult struct {
	todoID string
	err    error
}

// Orchestrator owns the Plan, the scheduler, and the coord.> subscription.
// It is the File 12 §12.4 loop. Lifecycle: NewOrchestrator subscribes coord.>
// (before any publisher can miss an event) and creates the done channel; Start
// is a no-op kept for the infra-mirroring split; Run runs the blocking event
// loop (closes done on return); Stop waits for done (idempotent, ctx-bound —
// mirrors infra). Everything below plan/sched/diffs is touched only by Run's
// goroutine — the agent goroutines Run spawns communicate with it exclusively
// through results; done is immutable after construction so Stop/Done may be
// called from another.
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

	// runErr accumulates the errors the event loop cannot return inline (a
	// publish that failed, an agent that would not start, a merge that did not
	// verify). One todo's failure must not abandon the rest of the plan, so the
	// loop keeps going and Run joins these into its return value.
	runErr []error

	// turns cancels the inflight agent turn per todo. Arming happens when a
	// turn is spawned, disarming when the todo's event arrives or the todo goes
	// terminal; the cancel both stops a cooperative runner and retires the
	// turn's watchdog. Touched only by the event loop goroutine.
	turns map[string]context.CancelFunc

	// agents counts inflight agent goroutines. Run waits on it before
	// returning, which is what lets a caller tear the runner down afterwards
	// (cmd/yolo's runtimeAgentRunner.close deletes the shadow tree the patch
	// engine is still reading from).
	agents sync.WaitGroup

	// results carries agent-goroutine failures back to the event loop. Only
	// the loop receives from it, and the loop keeps receiving through shutdown,
	// so a send never strands a goroutine.
	results chan agentResult

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
		turns:   make(map[string]context.CancelFunc),
		results: make(chan agentResult),
		// done is created HERE, not in Start: Run calls Start on the event
		// loop's goroutine while the owner reads done via Stop/Done on its
		// own, and that write/read pair is a data race. Creating it at
		// construction makes done immutable for the orchestrator's lifetime.
		done: make(chan struct{}),
		log:  slog.Default(),
	}
}

// Start is the infra-mirroring Start/Run split. It is a no-op: done is created
// in NewOrchestrator (see the comment there) so nothing mutates the
// orchestrator's lifecycle state after construction. Idempotent, and Run calls
// it implicitly if the caller didn't.
func (o *Orchestrator) Start(_ context.Context) {}

// Run decomposes goal into a Plan, publishes plan.ready, dispatches the
// ready todos, and drains the coord.> event loop until AllDone or ctx is
// canceled (returns ctx.Err() after cancelAll). It blocks the caller. Closes
// done on return so Stop can wait.
//
// Run returns nil ONLY when every todo reached Done and nothing was dropped
// along the way. A plan that terminated with Failed todos returns ErrPlanFailed
// — AllDone is true for Failed todos too, so "the loop exited" is not the same
// as "the work succeeded".
//
// Every exit — the plan completing, the context cancelling, the bus closing —
// goes through finish, which merges whatever the successful todos produced and
// publishes plan.done. plan.done used to be emitted only from the success path,
// so a consumer waiting on it could not tell a total failure from a run still
// in progress, and the diffs of the todos that DID succeed were merged by
// nobody.
//
// Run also does not return while an agent turn is still inflight: it cancels
// the turns and waits. The AgentRunner is the caller's, and tearing it down
// (closing a shadow tree, flushing a store) is only safe once nothing is using
// it.
func (o *Orchestrator) Run(ctx context.Context, goal string) error {
	o.Start(ctx)
	defer close(o.done)

	// agentCtx is the parent of every agent turn's context: cancelling it is
	// how the terminal path reaches turns the plan no longer needs.
	agentCtx, stopAgents := context.WithCancel(ctx)
	defer stopAgents()

	plan, _, err := o.planner.Plan(ctx, goal)
	if err != nil {
		return err
	}
	// Plan comes back by value but Todos is a slice, so the value return implies
	// a copy it does not give. Run writes Status and ReworkCycles through this
	// slice; without the clone those writes land in the planner's own backing
	// array and a planner that reuses or caches a plan sees them.
	plan.Todos = append([]Todo(nil), plan.Todos...)
	o.plan = &plan
	o.sched = NewScheduler(o.plan, o.cfg.Concurrency)

	// Publish plan.ready once (Plan marshaled to RawMessage; spec gap logged).
	if err := o.publishPlan(ctx); err != nil {
		return err
	}

	// Kick off the first wave of todos.
	o.dispatchReady(agentCtx)

	// Event loop: drain coord.> (and the agent goroutines' failure reports)
	// until the plan is AllDone, the bus closes, or ctx cancels.
	reason := termComplete
loop:
	for !o.plan.AllDone() {
		select {
		case <-ctx.Done():
			reason = termCanceled
			break loop
		case env, ok := <-o.ch:
			if !ok {
				// Bus closed before the plan finished: no further agent event
				// can arrive, so the outstanding todos can never complete.
				reason = termBusClosed
				break loop
			}
			o.handle(agentCtx, env)
		case res := <-o.results:
			o.applyAgentResult(agentCtx, res)
		}
	}
	return o.finish(ctx, stopAgents, reason)
}

// finish is the ONE terminal path, shared by all three exits. It drives the
// outstanding todos terminal (the two exits where no further agent event can
// arrive), quiesces the agent goroutines, merges whatever the successful todos
// produced, publishes plan.done, and returns the run's error.
//
// Merging on the cancel/bus-closed exits is not bookkeeping: those diffs are
// real work that used to be collected in o.diffs and then dropped on the floor
// because only the success path ever called Merge.
func (o *Orchestrator) finish(ctx context.Context, stopAgents context.CancelFunc, reason termination) error {
	switch reason {
	case termCanceled:
		o.failOutstanding("run canceled")
	case termBusClosed:
		o.failOutstanding("bus closed before the plan completed")
	}

	// Quiesce before merging: o.diffs must not gain an entry while Merge reads
	// it, and the caller may tear the runner down the moment Run returns.
	o.quiesceAgents(stopAgents)

	merged, mergeErr := o.mergeForReport(ctx, reason)
	o.publishPlanDone(ctx, reason, merged, mergeErr)
	return o.result(ctx, reason)
}

// quiesceAgents cancels every inflight agent turn and waits for its goroutine
// to return, draining the subscription and the results channel meanwhile. The
// draining is what makes the wait safe: an agent parked on a full coord.>
// buffer needs someone to keep reading, and after the event loop exits that
// someone is this function.
func (o *Orchestrator) quiesceAgents(stopAgents context.CancelFunc) {
	stopAgents()
	for id, cancel := range o.turns {
		cancel()
		delete(o.turns, id)
	}
	waited := make(chan struct{})
	go func() {
		o.agents.Wait()
		close(waited)
	}()
	ch := o.ch
	for {
		select {
		case <-waited:
			return
		case _, ok := <-ch:
			if !ok {
				ch = nil // a closed channel is always ready; stop selecting it
			}
		case res := <-o.results:
			// The plan is over, so the todo can no longer be failed — but the
			// error is still the only record that an agent panicked or refused
			// to start, and Run's caller is owed it.
			o.recordErr(fmt.Errorf("todo %s: %w", res.todoID, res.err))
		}
	}
}

// recordErr accumulates an error the event loop cannot return inline. The loop
// keeps running (one failed todo must not abandon the rest of the plan) but
// Run surfaces every recorded error to the caller.
func (o *Orchestrator) recordErr(err error) {
	if err != nil {
		o.runErr = append(o.runErr, err)
	}
}

// result is Run's return value once the loop exits: every recorded error plus
// ErrPlanFailed when any todo ended Failed. nil means every todo reached Done
// and nothing was dropped.
//
// A cancelled run returns ctx.Err() and deliberately NOT ErrPlanFailed. The
// cancel path marks the outstanding todos Failed so the plan is not left in
// limbo, but "the operator stopped this" is not "the plan gave up", and
// reporting the second is a lie the caller cannot see through.
func (o *Orchestrator) result(ctx context.Context, reason termination) error {
	errs := append([]error(nil), o.runErr...)
	if reason == termCanceled {
		return errors.Join(append(errs, ctx.Err())...)
	}
	if failed := o.plan.FailedTodos(); len(failed) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s", ErrPlanFailed, strings.Join(failed, ", ")))
	}
	return errors.Join(errs...)
}

// failOutstanding drives every non-terminal todo to Failed. Used by the two
// paths where no further agent event can arrive (cancellation, bus close):
// without it those todos stay Pending/InProgress forever and the plan is
// neither done nor failed.
func (o *Orchestrator) failOutstanding(reason string) {
	for i := range o.plan.Todos {
		td := &o.plan.Todos[i]
		if td.Status == Done || td.Status == Failed {
			continue
		}
		td.Status = Failed
		o.disarmTurn(td.ID)
		o.log.Warn("todo failed", "todo", td.ID, "reason", reason)
	}
}

// handle routes one agent-produced event to its handler (File 12 §12.4 switch).
// The todo's inflight turn is disarmed first: the event IS the turn reporting,
// so its watchdog has nothing left to guard and whatever runs next arms its own.
func (o *Orchestrator) handle(ctx context.Context, env event.Envelope) {
	switch e := env.Evt.(type) {
	case *event.CodeReadyEvent:
		o.disarmTurn(e.TodoID)
		o.requestReview(ctx, *e)
	case *event.ReviewVerdictEvent:
		o.disarmTurn(e.TodoID)
		if e.Approved {
			o.requestTest(ctx, *e)
		} else {
			o.reassignCoder(ctx, *e)
		}
	case *event.TestReportEvent:
		o.disarmTurn(e.TodoID)
		if e.Passed {
			o.markDone(ctx, e.TodoID)
		} else {
			o.reassignWithTestFail(ctx, *e)
		}
	}
}

// applyAgentResult turns one agent goroutine's failure — a spawn that would not
// start, a publish the bus rejected, a panic, a turn that blew its cap — into
// the same thing: the todo is Failed (nothing else will produce an event for
// it) and the cause reaches Run's caller.
func (o *Orchestrator) applyAgentResult(ctx context.Context, res agentResult) {
	o.log.Error("agent turn failed", "todo", res.todoID, "err", res.err)
	o.recordErr(fmt.Errorf("todo %s: %w", res.todoID, res.err))
	if st := o.plan.StatusOf(res.todoID); st == Done || st == Failed {
		return // already terminal (a cascade got here first); nothing to fail
	}
	o.failTodo(ctx, res.todoID)
}

// dispatchReady dispatches all ready todos, spawning a coder per todo. The
// spawn itself cannot fail here any more — it launches a goroutine — so there
// is nothing to drain: a coder that will not start reports on o.results and the
// event loop fails its todo from there, outside the scheduler's dispatch walk.
func (o *Orchestrator) dispatchReady(ctx context.Context) {
	o.sched.DispatchReady(o.spawnFn(ctx))
}

// spawnFn is the scheduler's spawn callback. It snapshots the todo INSIDE the
// dispatch walk — the goroutine it launches must not read a Todo the event loop
// goes on mutating — and then hands the snapshot to spawnAgent.
func (o *Orchestrator) spawnFn(ctx context.Context) func(*Todo) {
	return func(td *Todo) {
		task := event.TaskAssignEvent{
			PlanID:    o.plan.ID,
			TodoID:    td.ID,
			Agent:     string(RoleCoder),
			Brief:     td.Title,
			Artifacts: append([]string(nil), td.Artifacts...),
		}
		o.spawnAgent(ctx, RoleCoder, task, true)
	}
}

// failTodo drives one todo to Failed through the scheduler (freeing its
// inflight slot and releasing dependents, which may dispatch a new wave).
func (o *Orchestrator) failTodo(ctx context.Context, id string) {
	o.disarmTurn(id)
	o.sched.MarkFailed(id, o.spawnFn(ctx))
}

// spawnAgent runs one agent turn on its own goroutine and arms its cap.
//
// Off the event loop by construction: a turn publishes coord.* events, and the
// event loop is the only goroutine draining the coord.> subscription. Running
// the turn inline meant a plan wide enough to fill the 64-slot subscriber
// buffer blocked the publisher against its own reader — a real deadlock at 32
// concurrent todos, with no deadline on the production context to break it.
//
// publishAssign is true for the turns that put a new row on the board (the
// initial coder and each rework); the reviewer and tester ride the row their
// coder already published.
func (o *Orchestrator) spawnAgent(ctx context.Context, role Role, task event.TaskAssignEvent, publishAssign bool) {
	turnCtx, cancel := o.armTurn(ctx, task.TodoID, role)
	limit := o.cfg.turnTimeout(role)

	o.agents.Add(1)
	go func() {
		defer o.agents.Done()
		defer cancel()

		if err := o.runTurn(turnCtx, role, task, publishAssign); err != nil {
			o.results <- agentResult{todoID: task.TodoID, err: err}
			return
		}
		// The turn returned clean, which is not the same as the turn having
		// reported: the agent still owes the loop a coord.* event. Hold the
		// watchdog until the loop disarms this turn (its event arrived, or its
		// todo went terminal) or the cap expires. Without this an agent that
		// returns nil and publishes nothing is indistinguishable from one still
		// working, and the plan waits on it until the parent context dies.
		<-turnCtx.Done()
		if errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
			o.results <- agentResult{
				todoID: task.TodoID,
				err:    fmt.Errorf("%w: %s produced no event within %s", ErrAgentTimeout, role, limit),
			}
		}
	}()
}

// runTurn is one agent turn's body, run on the turn's own goroutine: publish
// the board row (when this turn owns one), then run the agent.
//
// The recover is the whole of File 12's "one todo's failure must not abandon
// the rest of the plan" applied to the case the package had no answer for. A
// panicking sub-agent used to unwind straight through Orchestrator.Run and kill
// the process — on the TUI path, mid alt-screen, leaving the terminal in raw
// mode. It becomes an ordinary todo failure here, carrying the recovered value
// and the stack: a panic reported as a bare "failed" loses the only evidence
// there was.
func (o *Orchestrator) runTurn(ctx context.Context, role Role, task event.TaskAssignEvent, publishAssign bool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %s on todo %s: %v\n%s",
				ErrAgentPanic, role, task.TodoID, r, debug.Stack())
		}
	}()

	if publishAssign {
		// A task.assign the bus rejected means the board never shows the row
		// and the coder never gets dispatched — the todo cannot proceed, so the
		// error is returned rather than dropped.
		if err := o.pub.Publish(ctx, &task); err != nil {
			return fmt.Errorf("publish task.assign: %w", err)
		}
	}
	if err := o.runner.Run(ctx, role, task); err != nil {
		return fmt.Errorf("run %s: %w", role, err)
	}
	return nil
}

// armTurn gives one todo's inflight turn its own cancellable context, carrying
// the role's cap when there is one, and records the cancel so the event loop
// can retire the turn when its event lands.
//
// A non-positive cap means NO cap, not "already elapsed": handing zero to
// context.WithTimeout would expire every turn instantly, which is the exact
// inversion an unset limit must never take.
func (o *Orchestrator) armTurn(ctx context.Context, todoID string, role Role) (context.Context, context.CancelFunc) {
	o.disarmTurn(todoID) // a todo has at most one turn inflight

	var turnCtx context.Context
	var cancel context.CancelFunc
	if d := o.cfg.turnTimeout(role); d > 0 {
		turnCtx, cancel = context.WithTimeout(ctx, d)
	} else {
		turnCtx, cancel = context.WithCancel(ctx)
	}
	o.turns[todoID] = cancel
	return turnCtx, cancel
}

// disarmTurn retires the todo's inflight turn: the cancel stops a cooperative
// runner and releases the turn goroutine's watchdog without it reporting a
// timeout. Called from the event loop only.
func (o *Orchestrator) disarmTurn(todoID string) {
	if cancel, ok := o.turns[todoID]; ok {
		delete(o.turns, todoID)
		cancel()
	}
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
		Artifacts: append([]string(nil), artifacts...),
	}
	// No reviewer → no review.verdict will ever arrive for this todo, so a
	// turn that fails reports on o.results and the loop fails the todo rather
	// than blocking on an event that cannot come.
	o.spawnAgent(ctx, RoleReviewer, task, false)
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
		Artifacts: append([]string(nil), artifacts...),
	}
	// Same reasoning as the reviewer: no tester → no test.report.
	o.spawnAgent(ctx, RoleTester, task, false)
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
		o.failTodo(ctx, td.ID)
		o.log.Warn("rework cap exceeded", "todo", td.ID, "cycles", td.ReworkCycles)
		return
	}
	o.rerunCoder(ctx, event.TaskAssignEvent{
		PlanID: o.plan.ID, TodoID: td.ID, Agent: string(RoleCoder),
		Brief:     td.Title + "\n\nReviewer comments:\n" + strings.Join(v.Comments, "\n"),
		Artifacts: append([]string(nil), td.Artifacts...),
	})
}

// rerunCoder re-publishes task.assign (the rework gets its own board row) and
// re-runs the coder for a rework cycle. A rework that cannot be dispatched
// fails the todo through the same o.results path as any other turn: the
// previous coder has already reported, so nothing else will produce an event.
func (o *Orchestrator) rerunCoder(ctx context.Context, task event.TaskAssignEvent) {
	o.spawnAgent(ctx, RoleCoder, task, true)
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
		o.failTodo(ctx, td.ID)
		o.log.Warn("rework cap exceeded (test fail)", "todo", td.ID, "cycles", td.ReworkCycles)
		return
	}
	o.rerunCoder(ctx, event.TaskAssignEvent{
		PlanID: o.plan.ID, TodoID: td.ID, Agent: string(RoleCoder),
		Brief:     td.Title + "\n\nTest output:\n" + e.Output,
		Artifacts: append([]string(nil), td.Artifacts...),
	})
}

// markDone marks the todo Done and re-dispatches dependents. The merge and
// plan.done live in finish, not here: markDone only ever fired on the success
// path, so a plan whose LAST todo failed — or that was cancelled, or whose bus
// closed — published no plan.done at all, and a consumer waiting on it could
// not tell total failure from still-running.
func (o *Orchestrator) markDone(ctx context.Context, todoID string) {
	o.disarmTurn(todoID)
	o.sched.MarkDone(todoID, o.spawnFn(ctx))
}

// mergeForReport combines the Done todos' diffs for the terminal report. On the
// cancel path it passes a nil Verifier: the successful todos' work is still
// worth combining, but re-running the verifier (in production, the test suite)
// after the operator pressed Ctrl-C is not what they asked for.
//
// A panicking Verifier is contained here for the same reason an agent's panic
// is contained in runTurn — a verifier is third-party code on the seam, and the
// run is over either way.
func (o *Orchestrator) mergeForReport(ctx context.Context, reason termination) (mp MergedPatch, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: verifier on plan %s: %v\n%s",
				ErrAgentPanic, o.plan.ID, r, debug.Stack())
		}
	}()
	v := o.Verifier
	if reason == termCanceled {
		v = nil
	}
	return Merge(ctx, o.plan, o.diffs, v)
}

// publishPlanDone emits the run's single terminal event. It runs on EVERY exit
// — complete, cancelled, bus closed — which is the whole point: plan.done is
// the only thing a consumer (the TUI board, the --plan JSONL transcript) can
// wait on, and an event that only ever arrives on success turns every failure
// into an indefinite wait.
//
// Done is not "the loop exited". It is: the plan ran to the end, the merge
// worked, the verifier (when there is one) accepted the patch, and NO todo
// ended Failed. That last term is the one that was missing — Merge deliberately
// skips a Failed todo's diff, so a plan where one todo exhausted its rework cap
// and another finished last merged cleanly and announced itself green with half
// the work absent.
func (o *Orchestrator) publishPlanDone(ctx context.Context, reason termination, mp MergedPatch, mergeErr error) {
	failed := o.plan.FailedTodos()
	verified := mp.Verified || o.Verifier == nil
	done := reason == termComplete && mergeErr == nil && verified && len(failed) == 0

	switch {
	case mergeErr != nil:
		o.recordErr(fmt.Errorf("merge plan %s: %w", o.plan.ID, mergeErr))
	case o.Verifier != nil && !mp.Verified && reason == termComplete:
		// Defensive: today Merge only reports Verified=false alongside an
		// error, but a future verifier must not be able to slip an unverified
		// patch through as a silent success.
		o.recordErr(errors.New("coord: merged patch not verified"))
	}

	// The run's own context is dead on the cancel path, and a Publish on a dead
	// context loses the race against the send about half the time. Detach it —
	// the terminal report is exactly the message a cancelled run must still
	// deliver — but bound it, because nothing drains the loop any more.
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), planDonePublishTimeout)
	defer cancel()
	if err := o.pub.Publish(pubCtx, &event.PlanDoneEvent{
		PlanID: o.plan.ID,
		Done:   done,
		// A cancelled plan has not failed. Done is false for both, so this is
		// the only thing that lets a consumer render Ctrl-C as something other
		// than a failure.
		Canceled: reason == termCanceled,
		Merged:   mp.Merged(),
		Summary:  o.planDoneSummary(reason, mp, mergeErr, failed),
	}); err != nil {
		// plan.done is the caller's only signal that the run finished; a bus
		// that dropped it makes the run unreportable, not merely noisy.
		o.log.Error("plan.done publish failed", "plan", o.plan.ID, "err", err)
		o.recordErr(fmt.Errorf("publish plan.done: %w", err))
	}
}

// planDoneSummary is the human-readable half of plan.done — the line that ends
// up on the TUI board and in the transcript. It always states how much of the
// plan actually landed and NAMES the todos that did not: "merged" over a plan
// missing a todo's work is the failure this whole path exists to stop.
func (o *Orchestrator) planDoneSummary(reason termination, mp MergedPatch, mergeErr error, failed []string) string {
	head := fmt.Sprintf("%d/%d todos done", mp.Summary.Done, len(o.plan.Todos))
	switch reason {
	case termCanceled:
		head = "canceled: " + head
	case termBusClosed:
		head = "bus closed before the plan completed: " + head
	}
	parts := []string{head}
	if len(failed) > 0 {
		parts = append(parts, "failed: "+strings.Join(failed, ", "))
	}
	switch {
	case mergeErr != nil:
		parts = append(parts, "merge: "+mergeErr.Error())
	case reason == termCanceled:
		parts = append(parts, "combined without re-verification (canceled)")
	case o.Verifier == nil:
		parts = append(parts, "merged, no verifier configured")
	case !mp.Verified:
		parts = append(parts, "merged patch not verified")
	default:
		parts = append(parts, "merged and verified")
	}
	return strings.Join(parts, "; ")
}

// Merged reports whether the merge produced an actual patch. It asks the
// combined diff, not the todo count: coders that finish without emitting a diff
// (every coder today, since withPatches is unwired) leave Summary.Done > 0 with
// nothing merged, and plan.done would announce Merged: true over zero bytes.
func (mp MergedPatch) Merged() bool { return len(mp.CombinedDiff) > 0 }

// publishPlan marshals the typed Plan to RawMessage and publishes plan.ready.
func (o *Orchestrator) publishPlan(ctx context.Context) error {
	raw, err := json.Marshal(o.plan)
	if err != nil {
		return err
	}
	return o.pub.Publish(ctx, &event.PlanReadyEvent{PlanID: o.plan.ID, Plan: raw})
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
