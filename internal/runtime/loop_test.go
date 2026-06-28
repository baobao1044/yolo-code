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

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/session"
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
