// Tests for the L8-003 FSM wiring (File 04 §4.5 + File 09 §10.5.4): the
// PATCH→VERIFY→(fail)→rollback loop driven end-to-end. The drive loop gains
// EXECUTE/WAIT_TOOL/VERIFY/PATCH arms; a verify failure rolls the task back to
// the patch checkpoint via a Restorer seam and publishes verification.failed;
// the fail→Reflection handoff decides Replan/Patch/Abort; a retry cap stops a
// spinning loop. These tests stub every port and assert the observable state
// sequence, the rollback, and the "task not marked done" invariant (Sprint 6
// §15.9.2 exit bar).

package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// ----- stub ports for the end-to-end loop -----

// patchCognitive is a CognitiveCore that emits one tool call (a patch) on the
// first Think, then answers Final on later turns (Reflection returns Abort so
// the verify-fail loop stops at the retry cap rather than spinning).
type patchCognitive struct {
	mu       sync.Mutex
	emitted  bool
	abort    bool // when true, Reflect aborts
	reflects int
}

func (c *patchCognitive) Think(_ context.Context, _ Prompt) (CognitiveTurn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.emitted {
		c.emitted = true
		return CognitiveTurn{ToolCalls: []ToolCall{{Tool: "patch", Reason: "edit a.go"}}}, nil
	}
	return CognitiveTurn{Final: true, Text: "done after patch"}, nil
}

func (c *patchCognitive) HasMore(*session.Task) bool { return false }

func (c *patchCognitive) Reflect(_ context.Context, _ *session.Task, _ Verdict, _ Observation) ReflectionDecision {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reflects++
	if c.abort {
		return ReflectionDecision{Abort: true, Note: "reflection abort"}
	}
	// Ask for a corrective patch → the runtime drives PATCH.
	return ReflectionDecision{Patch: PatchOp{Body: []byte("corrective")}}
}

func (*patchCognitive) RecordToolResult(string, string, string) {}
func (*patchCognitive) Reset()                                  {}

// applyExecutor is an Executor that always dispatches (no approval needed) and
// records the observation as from-a-patch so VERIFY inspects the touched files.
type applyExecutor struct {
	approved bool
	dispatch int
	obs      Observation
}

func (e *applyExecutor) NeedsApproval(ToolCall) bool { return false }

func (e *applyExecutor) Dispatch(_ context.Context, _ ToolCall) (Observation, error) {
	e.dispatch++
	return e.obs, nil
}

// failVerifier always returns a fail Verdict (the patch broke a test).
type failVerifier struct {
	calls int
}

func (v *failVerifier) Verify(_ context.Context, _ Observation, _ *session.Task, _ VerifyPolicy) (Verdict, error) {
	v.calls++
	return Verdict{Pass: false, Stage: "tests", Severity: "fail", Reason: "test failed"}, nil
}

// recordPatcher is a Patcher that accepts every patch and reports a checkpoint
// name the runtime can Restore on a verify failure.
type recordPatcher struct {
	calls int
}

func (p *recordPatcher) Apply(_ context.Context, _ PatchOp) (PatchResult, error) {
	p.calls++
	return PatchResult{Accepted: true, Checkpoint: "patch_1", Snapshot: []byte(`{"sha":"abc"}`)}, nil
}

// recordRestorer is a Restorer seam the runtime calls on a verify failure; it
// records every Restore so the test can assert the rollback happened with the
// right checkpoint name.
type recordRestorer struct {
	mu     sync.Mutex
	called []string
}

func (r *recordRestorer) Restore(_ context.Context, _ session.TaskID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, name)
	return nil
}

func (r *recordRestorer) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.called...)
}

// newLoopCore wires a runtime Core over a fresh store + bus with the loop ports
// set (Executor, Verifier, Patcher, Restorer) and a patch-emitting cognitive
// core. Returns the core, the bus, the session id, and the recorders.
func newLoopCore(t *testing.T) (*Core, *event.Bus, session.ID, *applyExecutor, *failVerifier, *recordPatcher, *recordRestorer) {
	t.Helper()
	store := session.NewFileStore(t.TempDir())
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	exec := &applyExecutor{obs: Observation{FromPatch: true, Files: []string{"a.go"}, Checkpoint: "patch_1"}}
	ver := &failVerifier{}
	patcher := &recordPatcher{}
	restorer := &recordRestorer{}
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: &patchCognitive{abort: true}, // abort on reflection so the loop stops at the cap
		Exec:      exec,
		Verify:    ver,
		Patch:     patcher,
		Restore:   restorer,
	})
	return core, bus, sid, exec, ver, patcher, restorer
}

func TestDrivePatchVerifyFailRollsBack(t *testing.T) {
	core, bus, sid, _, ver, _, restorer := newLoopCore(t)
	ch := bus.Subscribe(">")
	tid, _ := core.Submit(context.Background(), sid, "edit a.go and add a test")

	// The loop should reach a terminal state (abort on reflection → cancelled).
	// Drain until quiet.
	envs := drainUntilQuiet(t, ch, 2*time.Second)

	// 1. The state sequence includes EXECUTE, WAIT_TOOL, VERIFY (and the
	//    PATCH→VERIFY→(fail)→rollback). Find VERIFY→PATCH in the trace.
	var toStates []string
	for _, env := range envs {
		if ce, ok := env.Evt.(*event.StateChangeEvent); ok {
			toStates = append(toStates, ce.To)
		}
	}
	requireState(t, toStates, "EXECUTE")
	requireState(t, toStates, "WAIT_TOOL")
	requireState(t, toStates, "VERIFY")

	// 2. verification.failed was published.
	var sawFailed bool
	for _, env := range envs {
		if _, ok := env.Evt.(*event.VerificationFailedEvent); ok {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("no verification.failed event in the trace (verify failure didn't surface)")
	}

	// 3. The Restorer was called (the checkpoint was rolled back).
	if names := restorer.names(); len(names) == 0 {
		t.Error("Restorer.Restore was never called (verify failure didn't roll back)")
	}

	// 4. The Verifier ran (VERIFY actually drove the pipeline).
	if ver.calls == 0 {
		t.Error("Verifier.Verify never ran (VERIFY arm didn't drive the port)")
	}

	// 5. The task is NOT marked DONE — a failed verify must not complete the task.
	task := core.session.LoadTaskPublic(tid)
	if task == nil {
		t.Fatal("task vanished")
	}
	if task.Status == session.StatusDone {
		t.Errorf("task status = DONE, want NOT done (a failed verify must not complete the task)")
	}
}

func TestDriveVerifyFailCorrectivePatchReEntersVerify(t *testing.T) {
	// Reflection returns Patch (not Abort) → the runtime drives PATCH→VERIFY again.
	// The retry cap stops the loop so it can't spin forever (File 07 §7.3.2).
	store := session.NewFileStore(t.TempDir())
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	exec := &applyExecutor{obs: Observation{FromPatch: true, Files: []string{"a.go"}, Checkpoint: "patch_1"}}
	ver := &failVerifier{}
	patcher := &recordPatcher{}
	restorer := &recordRestorer{}
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: &patchCognitive{abort: false}, // Reflect → Patch (corrective)
		Exec:      exec,
		Verify:    ver,
		Patch:     patcher,
		Restore:   restorer,
	})
	ch := bus.Subscribe(">")
	core.Submit(context.Background(), sid, "edit a.go and fix the test")

	drainUntilQuiet(t, ch, 2*time.Second)

	// Reflection chose Patch → the PATCH arm ran, re-entering VERIFY each time,
	// until the retry cap (maxVerifyRetries) cancelled the task.
	if patcher.calls == 0 {
		t.Error("Patcher.Apply never ran (Reflection chose Patch but PATCH arm didn't drive)")
	}
	// The Verifier ran more than once (VERIFY re-entered after PATCH→VERIFY).
	if ver.calls < 2 {
		t.Errorf("Verifier.Verify calls = %d, want >=2 (PATCH→VERIFY re-enters verify)", ver.calls)
	}
	// The retry cap kept it bounded: Patcher ran at most maxVerifyRetries times.
	if patcher.calls > maxVerifyRetries {
		t.Errorf("Patcher.Apply calls = %d, want <= %d (retry cap not enforced)", patcher.calls, maxVerifyRetries)
	}
}

func TestDriveVerifyFailReflectionAbortStopsTheLoop(t *testing.T) {
	// Reflection returns Abort → the runtime must stop (not spin forever
	// retrying the patch). The retry cap is the safety valve.
	core, bus, sid, _, _, _, _ := newLoopCore(t)
	ch := bus.Subscribe(">")
	tid, _ := core.Submit(context.Background(), sid, "edit a.go")

	// Drain until quiet — the loop must terminate, not hang.
	envs := drainUntilQuiet(t, ch, 2*time.Second)

	// The loop terminated: confirm a terminal state was reached.
	var lastTo string
	for _, env := range envs {
		if ce, ok := env.Evt.(*event.StateChangeEvent); ok {
			lastTo = ce.To
		}
	}
	if lastTo != "CANCELLED" && lastTo != "DONE" && lastTo != "ERROR" {
		t.Errorf("loop did not reach a terminal state; last = %q (reflection abort should stop it)", lastTo)
	}
	task := core.session.LoadTaskPublic(tid)
	if task == nil {
		t.Fatal("task vanished")
	}
}

// loopingCognitive never converges: every Think emits another tool call and
// HasMore always says there is more, so the FSM cycles PLAN→EXECUTE→WAIT_TOOL
// →VERIFY→PLAN indefinitely. giveUpAfter is a test-only escape hatch so an
// uncapped run ends the test instead of hanging it.
type loopingCognitive struct {
	mu          sync.Mutex
	turns       int
	giveUpAfter int
}

func (c *loopingCognitive) Think(context.Context, Prompt) (CognitiveTurn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns++
	if c.turns >= c.giveUpAfter {
		return CognitiveTurn{Final: true, Text: "gave up"}, nil
	}
	return CognitiveTurn{ToolCalls: []ToolCall{{Tool: "bash", Reason: "keep going"}}}, nil
}

func (c *loopingCognitive) HasMore(*session.Task) bool { return true }
func (c *loopingCognitive) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	return ReflectionDecision{Abort: true, Note: "looping cognitive has no reflection"}
}
func (*loopingCognitive) RecordToolResult(string, string, string) {}
func (*loopingCognitive) Reset()                                  {}

func (c *loopingCognitive) turnCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turns
}

// capLedger is a CostLedger whose hard cap trips after `allow` loops — the
// shape *cognitive.Cost takes once MaxTime elapses on a task that keeps
// planning, expressed in loop counts so the test needs no sleep and the runtime
// needs no import of cognitive. It also records the token pairs it was given,
// so a test can assert the drive loop bills a turn only when the provider
// actually reported usage.
type capLedger struct {
	mu         sync.Mutex
	allow      int
	loops      int
	registered []session.TaskID
	tokens     [][2]int
}

func (l *capLedger) RegisterTask(id session.TaskID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.registered = append(l.registered, id)
}

func (l *capLedger) IncLoop(session.TaskID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loops++
}

func (l *capLedger) AddTokens(_ session.TaskID, in, out int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tokens = append(l.tokens, [2]int{in, out})
}

func (l *capLedger) HardCapExceeded(session.TaskID) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loops > l.allow, "time cap"
}

func (l *capLedger) counts() (loops int, registered []session.TaskID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loops, append([]session.TaskID(nil), l.registered...)
}

func (l *capLedger) billed() [][2]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][2]int(nil), l.tokens...)
}

// TestDriveCostCapAbortsRunawayLoop is the runaway-guard headline: a task that
// never converges must be stopped BY THE DRIVE LOOP, visibly. The ledger is
// exercised through the real PLAN arm (not called directly), the abort lands in
// the terminal CANCELLED state, and cost.abort states the cause.
func TestDriveCostCapAbortsRunawayLoop(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "proj", "demo")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	cog := &loopingCognitive{giveUpAfter: 50}
	ledger := &capLedger{allow: 3}
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: cog,
		Exec:      &applyExecutor{},
		Cost:      ledger,
	})

	// Drain concurrently: an uncapped run outruns the subscriber buffer and
	// would deadlock on backpressure instead of failing.
	ch := bus.Subscribe(">")
	done := make(chan session.TaskID, 1)
	go func() {
		tid, _ := core.Submit(context.Background(), sid, "never converge")
		done <- tid
	}()

	var sawAbort bool
	var cancelWhy string
	record := func(env event.Envelope) {
		switch e := env.Evt.(type) {
		case *event.CostAbortEvent:
			sawAbort = true
		case *event.StateChangeEvent:
			if e.To == string(StateCancelled) {
				cancelWhy = e.Why
			}
		}
	}

	var tid session.TaskID
	for returned := false; !returned; {
		select {
		case env := <-ch:
			record(env)
		case tid = <-done:
			returned = true
		case <-time.After(10 * time.Second):
			t.Fatal("drive loop never returned")
		}
	}
	for draining := true; draining; {
		select {
		case env := <-ch:
			record(env)
		default:
			draining = false
		}
	}

	loops, registered := ledger.counts()
	if len(registered) != 1 || registered[0] != tid {
		t.Errorf("ledger.RegisterTask calls = %v, want [%s] (the deadline must start at task start)", registered, tid)
	}
	if loops == 0 {
		t.Error("ledger.IncLoop never ran — the drive loop does not count its own loops, so the degradation ladder can never advance")
	}
	// The looping planner reports no usage, so the ledger must have been billed
	// nothing. Billing 0/0 per turn is what would let a provider that reports
	// no tokens look free forever.
	if billed := ledger.billed(); len(billed) != 0 {
		t.Errorf("ledger.AddTokens calls = %v, want none (the planner reported no usage)", billed)
	}
	if got := cog.turnCount(); got != ledger.allow {
		t.Errorf("planner turns = %d, want %d (the cap must stop the run, not the planner giving up)", got, ledger.allow)
	}
	if !sawAbort {
		t.Error("no cost.abort event — the run stopped without telling the user why")
	}
	if cancelWhy != "cost_cap" {
		t.Errorf("CANCELLED why = %q, want cost_cap (the cap must reach a terminal state)", cancelWhy)
	}
	if task := smgr.LoadTaskPublic(tid); task == nil || task.Status != session.StatusCancelled {
		t.Errorf("task status = %v, want CANCELLED", task)
	}
}

// usageCognitive answers directly and reports the usage it was given, so a test
// can drive the billing path with and without a measurement.
type usageCognitive struct {
	in, out int
	known   bool
}

func (c usageCognitive) Think(context.Context, Prompt) (CognitiveTurn, error) {
	return CognitiveTurn{
		Final: true, Text: "done",
		TokensIn: c.in, TokensOut: c.out, UsageKnown: c.known,
	}, nil
}

func (usageCognitive) HasMore(*session.Task) bool { return false }
func (usageCognitive) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	return ReflectionDecision{Abort: true}
}
func (usageCognitive) RecordToolResult(string, string, string) {}
func (usageCognitive) Reset()                                  {}

// TestDriveBillsOnlyMeasuredTurns is the other half of the honesty rule the
// whole cost pass turns on. A provider that reports usage must be billed the
// real counts; a provider that reports nothing must be billed nothing at all,
// NOT zero. Billing 0/0 is what let the spend cap sit permanently at $0.00 for
// exactly the providers whose cost nobody could see.
func TestDriveBillsOnlyMeasuredTurns(t *testing.T) {
	for _, tc := range []struct {
		name string
		cog  usageCognitive
		want [][2]int
	}{
		{"measured", usageCognitive{in: 1200, out: 340, known: true}, [][2]int{{1200, 340}}},
		{"unmeasured", usageCognitive{known: false}, nil},
		{"measured zero output", usageCognitive{in: 7, out: 0, known: true}, [][2]int{{7, 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := event.New()
			t.Cleanup(func() { _ = bus.Close() })
			smgr := session.New(session.Deps{
				Store: session.NewFileStore(t.TempDir()), Bus: bus,
				Git: session.NewInMemCheckpointer(),
			})
			sid, err := smgr.OpenSession(context.Background(), "proj", "demo")
			if err != nil {
				t.Fatalf("OpenSession: %v", err)
			}
			ledger := &capLedger{allow: 100} // never trips; we are watching the billing
			core := New(Deps{Bus: bus, Session: smgr, Cognitive: tc.cog, Cost: ledger})
			if _, err := core.Submit(context.Background(), sid, "answer"); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			got := ledger.billed()
			if len(got) != len(tc.want) {
				t.Fatalf("AddTokens calls = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("AddTokens[%d] = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ----- helpers -----

// drainUntilQuiet drains the channel until `quiet` elapses with no new event.
func drainUntilQuiet(t *testing.T, ch <-chan event.Envelope, quiet time.Duration) []event.Envelope {
	t.Helper()
	var out []event.Envelope
	for {
		select {
		case env := <-ch:
			out = append(out, env)
		case <-time.After(quiet):
			return out
		}
	}
}

// requireState fails the test if the state isn't in the sequence.
func requireState(t *testing.T, states []string, want string) {
	t.Helper()
	for _, s := range states {
		if s == want {
			return
		}
	}
	t.Errorf("state %q not in the trace; got %s", want, strings.Join(states, "→"))
}
