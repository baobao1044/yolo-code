// Guards on the drive loop that a green suite was not exercising.
//
// Each test here corresponds to something the loop does that had no coverage at
// all, and in three cases the uncovered line was load-bearing in production and
// deletable without a single failure: the cost cap's second sampling point, the
// observation-to-call pairing fallback, and the file list a corrective patch is
// verified against. The fourth is a startup ordering that only fails under
// scheduler pressure, so it is pinned structurally rather than by racing it.

package runtime

import (
	"context"
	"encoding/json"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// --- shared harness -------------------------------------------------------

// recorder drains the bus into a slice from its own goroutine. Collecting
// events after the fact instead would risk the 64-slot subscriber buffer
// filling mid-task and wedging the drive loop against its own trace.
type recorder struct {
	mu   sync.Mutex
	evts []event.Event
	done chan struct{}
}

func record(bus *event.Bus) *recorder {
	r := &recorder{done: make(chan struct{})}
	ch := bus.Subscribe(">")
	go func() {
		defer close(r.done)
		for env := range ch {
			r.mu.Lock()
			r.evts = append(r.evts, env.Evt)
			r.mu.Unlock()
		}
	}()
	return r
}

// stop closes the bus, waits for the drain to finish and returns everything.
func (r *recorder) stop(bus *event.Bus) []event.Event {
	_ = bus.Close()
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event.Event(nil), r.evts...)
}

func statesReached(evts []event.Event) []string {
	var out []string
	for _, e := range evts {
		if sc, ok := e.(*event.StateChangeEvent); ok {
			out = append(out, sc.To)
		}
	}
	return out
}

func sawState(evts []event.Event, want State) bool {
	for _, s := range statesReached(evts) {
		if s == string(want) {
			return true
		}
	}
	return false
}

func errorCodes(evts []event.Event) []string {
	var out []string
	for _, e := range evts {
		if ee, ok := e.(*event.ErrorEvent); ok {
			out = append(out, ee.Code)
		}
	}
	return out
}

func newGuardCore(t *testing.T, d Deps) (*Core, *event.Bus, session.ID) {
	t.Helper()
	bus := event.New()
	store := session.NewFileStore(t.TempDir())
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	d.Bus, d.Session = bus, smgr
	return New(d), bus, sid
}

// scriptCog plays a fixed script of turns and a fixed script of reflections.
// Both run off the end by repeating their last entry, so a loop that takes an
// unexpected extra turn still terminates instead of blocking the suite.
type scriptCog struct {
	mu          sync.Mutex
	turns       []CognitiveTurn
	reflections []ReflectionDecision
	turnN       int
	reflectN    int
	results     []string // callIDs handed to RecordToolResult, in order
}

func (s *scriptCog) Think(context.Context, Prompt) (CognitiveTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.turnN
	if i >= len(s.turns) {
		i = len(s.turns) - 1
	}
	s.turnN++
	return s.turns[i], nil
}

func (*scriptCog) HasMore(*session.Task) bool { return false }

func (s *scriptCog) Reflect(context.Context, *session.Task, Verdict, Observation) ReflectionDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.reflectN
	if len(s.reflections) == 0 {
		return ReflectionDecision{Abort: true}
	}
	if i >= len(s.reflections) {
		i = len(s.reflections) - 1
	}
	s.reflectN++
	return s.reflections[i]
}

func (s *scriptCog) RecordToolResult(callID, _, _ string) {
	s.mu.Lock()
	s.results = append(s.results, callID)
	s.mu.Unlock()
}

func (*scriptCog) Reset() {}

func (s *scriptCog) recordedCallIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.results...)
}

func (s *scriptCog) thinkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnN
}

// recordingExec dispatches without asking and records what it ran. obs is the
// observation every dispatch returns; hook, when set, runs after recording (the
// cost test uses it to trip the ledger mid-turn).
type recordingExec struct {
	mu   sync.Mutex
	got  []ToolCall
	obs  Observation
	hook func()
}

func (*recordingExec) NeedsApproval(ToolCall) bool { return false }

func (e *recordingExec) Dispatch(_ context.Context, call ToolCall) (Observation, error) {
	e.mu.Lock()
	e.got = append(e.got, call)
	hook := e.hook
	obs := e.obs
	e.mu.Unlock()
	if hook != nil {
		hook()
	}
	return obs, nil
}

func (e *recordingExec) dispatched() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, c := range e.got {
		out = append(out, c.Tool)
	}
	return out
}

// --- cost caps ------------------------------------------------------------

// tripLedger is over budget from the moment trip() is called. Real caps are
// wall-clock or spend and can therefore expire at any instant, including
// halfway through draining a turn's tool calls; this makes that instant
// explicit instead of hoping a timer lands in the right window.
type tripLedger struct {
	mu      sync.Mutex
	over    bool
	queries int
}

func (l *tripLedger) RegisterTask(session.TaskID)        {}
func (l *tripLedger) IncLoop(session.TaskID)             {}
func (l *tripLedger) AddTokens(session.TaskID, int, int) {}

func (l *tripLedger) HardCapExceeded(session.TaskID) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.queries++
	if l.over {
		return true, "time cap"
	}
	return false, ""
}

func (l *tripLedger) trip() {
	l.mu.Lock()
	l.over = true
	l.mu.Unlock()
}

func (l *tripLedger) queried() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.queries
}

// TestCostCapStopsTheDrainMidTurn pins the sampling point that decides whether
// a cap is a limit or a suggestion.
//
// PLAN used to be the only place the ledger was consulted, and a multi-call
// turn never re-enters PLAN: EXECUTE → WAIT_TOOL → VERIFY → EXECUTE drains the
// queue on the T22 edge. So a cap that expired after the first dispatch was not
// noticed until every remaining call in the turn had already run — and a turn
// is exactly where the spending happens. Here the ledger goes over budget
// during the first of three dispatches; the other two must not run.
func TestCostCapStopsTheDrainMidTurn(t *testing.T) {
	ledger := &tripLedger{}
	exec := &recordingExec{obs: Observation{Stdout: "ok"}}
	exec.hook = ledger.trip

	cog := &scriptCog{turns: []CognitiveTurn{
		{ToolCalls: []ToolCall{
			{ID: "c1", Tool: "one"},
			{ID: "c2", Tool: "two"},
			{ID: "c3", Tool: "three"},
		}},
		{Final: true, Text: "done"},
	}}

	core, bus, sid := newGuardCore(t, Deps{Cognitive: cog, Exec: exec, Cost: ledger})
	rec := record(bus)
	if _, err := core.Submit(context.Background(), sid, "three tool calls"); err != nil {
		t.Fatal(err)
	}
	evts := rec.stop(bus)

	if got := exec.dispatched(); len(got) != 1 {
		t.Errorf("dispatched %v after the cap expired on the first call, want only the first: the cap is sampled on PLAN entry and a drain never re-enters PLAN", got)
	}
	if ledger.queried() < 2 {
		t.Errorf("HardCapExceeded consulted %d time(s) for a 3-call turn; one consultation per turn cannot bound a turn", ledger.queried())
	}
	var aborted bool
	for _, e := range evts {
		if ca, ok := e.(*event.CostAbortEvent); ok && ca.Reason == "time cap" {
			aborted = true
		}
	}
	if !aborted {
		t.Errorf("no cost.abort naming the cap; states=%v", statesReached(evts))
	}
	if !sawState(evts, StateCancelled) {
		t.Errorf("task did not reach CANCELLED; states=%v", statesReached(evts))
	}
}

// --- startup ordering -----------------------------------------------------

// TestUserSubscriptionExistsWhenNewReturns pins the fix for a startup race that
// no amount of -race will find: it is a lost wakeup, not a data race.
//
// The subscription used to be created inside the goroutine New spawned. `go f()`
// only makes f runnable — until the scheduler picks it up there is no
// subscriber, and a user.approve published in that window is delivered to
// nobody. The task then waits forever for an answer that was already given.
// Under CPU contention this reproduced in roughly 40% of runs.
//
// There is no honest way to make the failing interleaving deterministic from a
// test: any synchronisation the test performs is another chance for the
// scheduler to run the goroutine, and the bus exposes no subscriber count to
// poll. So the property is asserted structurally instead — the subscription
// must already exist at the instant New returns, which is only possible if New
// itself created it. GOMAXPROCS(1) makes the pre-fix version fail outright
// rather than usually: with one P, a goroutine spawned but not yet scheduled
// cannot have subscribed.
func TestUserSubscriptionExistsWhenNewReturns(t *testing.T) {
	defer goruntime.GOMAXPROCS(goruntime.GOMAXPROCS(1))

	bus := event.New()
	defer func() { _ = bus.Close() }()
	store := session.NewFileStore(t.TempDir())
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})

	core := New(Deps{Bus: bus, Session: smgr})

	if core.userEvents == nil {
		t.Fatal("New returned with no user.* subscription: every verdict published before the event loop is scheduled is dropped, and the task parked in WAIT_USER never wakes")
	}
}

// --- observation pairing --------------------------------------------------

// blankIDExec is the shape of the shipped executor adapter: cmd/yolo's
// execToRuntimeObs builds the runtime Observation field by field and never sets
// CallID, because the exec engine does not carry one. Every existing test used
// a double that filled CallID in itself, which is why deleting the runtime's
// backfill left the suite green while breaking the real wiring.
type blankIDExec struct{}

func (blankIDExec) NeedsApproval(ToolCall) bool { return false }
func (blankIDExec) Dispatch(_ context.Context, call ToolCall) (Observation, error) {
	return Observation{Stdout: "ran " + call.Tool, Tool: call.Tool}, nil
}

// TestObservationWithBlankCallIDIsPairedToItsCall pins the backfill.
//
// The conversation history is keyed by call id: a turn that calls one tool
// twice is indistinguishable by name, so a blank id makes the cognitive core
// fall back to guessing and a pair of results can be reasoned about swapped.
// Since the production adapter always leaves the id blank, the backfill is the
// only thing that ever supplies it outside of tests.
func TestObservationWithBlankCallIDIsPairedToItsCall(t *testing.T) {
	cog := &scriptCog{turns: []CognitiveTurn{
		{ToolCalls: []ToolCall{
			{ID: "call_a", Tool: "bash", Args: []byte(`{"command":"echo a"}`)},
			{ID: "call_b", Tool: "bash", Args: []byte(`{"command":"echo b"}`)},
		}},
		{Final: true, Text: "done"},
	}}

	core, bus, sid := newGuardCore(t, Deps{Cognitive: cog, Exec: blankIDExec{}})
	rec := record(bus)
	if _, err := core.Submit(context.Background(), sid, "two calls to one tool"); err != nil {
		t.Fatal(err)
	}
	rec.stop(bus)

	got := cog.recordedCallIDs()
	want := []string{"call_a", "call_b"}
	if len(got) != len(want) {
		t.Fatalf("recorded %v tool results, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool result %d recorded under call id %q, want %q: the executor left the id blank and nothing filled it in from the call being answered", i, got[i], want[i])
		}
	}
}

// --- corrective patch: what gets verified ---------------------------------

// engineLikeVerifier mimics the real verification engine on the one property
// that matters here: it checks the files it is given, and given none it has
// nothing to run and reports a pass. That is not a quirk of the double — the
// reviewer confirmed it against internal/verify with the same repo state:
// Files=[broken.go] → Pass=false, Files=nil → Pass=true with zero commands
// executed.
type engineLikeVerifier struct {
	mu   sync.Mutex
	seen [][]string
}

func (v *engineLikeVerifier) Verify(_ context.Context, obs Observation, _ *session.Task, _ VerifyPolicy) (Verdict, error) {
	v.mu.Lock()
	v.seen = append(v.seen, append([]string(nil), obs.Files...))
	v.mu.Unlock()
	if len(obs.Files) == 0 {
		return Verdict{Pass: true, Stage: "ast", Severity: "pass", Reason: "nothing to check"}, nil
	}
	return Verdict{Pass: false, Stage: "ast", Severity: "error", Reason: "stage ast failed: syntax error"}, nil
}

func (v *engineLikeVerifier) calls() [][]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([][]string(nil), v.seen...)
}

// recordingPatcher accepts everything and remembers the ops it was handed.
type recordingPatcher struct {
	mu  sync.Mutex
	ops []PatchOp
}

func (p *recordingPatcher) Apply(_ context.Context, op PatchOp) (PatchResult, error) {
	p.mu.Lock()
	p.ops = append(p.ops, op)
	n := len(p.ops)
	p.mu.Unlock()
	return PatchResult{Accepted: true, Checkpoint: "patch_" + string(rune('0'+n)), Snapshot: []byte(`"snap"`)}, nil
}

func (p *recordingPatcher) applied() []PatchOp {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PatchOp(nil), p.ops...)
}

// TestCorrectivePatchIsVerifiedAgainstTheFilesItTouched covers the worst of
// these: the path where verification matters most was the one path guaranteed
// not to run it.
//
// After PATCH applies, T15 hands an observation straight to VERIFY, and the
// composition root copies Observation.Files into the verify Change verbatim.
// The runtime used to build that observation with Files derived from the patch
// engine's snapshot ref — an opaque restore handle, from which the helper
// could only ever return nil. So the corrective patch was verified against an
// empty change: zero stages ran, the verdict came back Pass, and the task
// completed over a repo the patch had just broken.
func TestCorrectivePatchIsVerifiedAgainstTheFilesItTouched(t *testing.T) {
	verifier := &engineLikeVerifier{}
	patcher := &recordingPatcher{}
	cog := &scriptCog{
		turns: []CognitiveTurn{
			{ToolCalls: []ToolCall{{ID: "c1", Tool: "edit_file", Args: []byte(`{"path":"broken.go"}`)}}},
			{Final: true, Text: "done"},
		},
		// First failure: try a corrective patch. Second: give up, so the test
		// terminates instead of patching forever.
		reflections: []ReflectionDecision{
			{Patch: PatchOp{Body: []byte("still broken")}},
			{Abort: true, Note: "no"},
		},
	}
	exec := &recordingExec{obs: Observation{
		Files: []string{"broken.go"}, Checkpoint: "ckpt_1", Tool: "edit_file",
	}}

	core, bus, sid := newGuardCore(t, Deps{
		Cognitive: cog, Exec: exec, Verify: verifier, Patch: patcher,
	})
	rec := record(bus)
	if _, err := core.Submit(context.Background(), sid, "edit a file"); err != nil {
		t.Fatal(err)
	}
	evts := rec.stop(bus)

	if len(patcher.applied()) == 0 {
		t.Fatal("the corrective patch never reached the patcher; this test is not exercising the path it claims to")
	}
	calls := verifier.calls()
	if len(calls) < 2 {
		t.Fatalf("the verifier ran %d time(s), want a second run after the patch; verdicts=%v", len(calls), calls)
	}
	if len(calls[1]) == 0 {
		t.Errorf("the post-patch verify was handed no files, so the pipeline runs zero stages and certifies the patch by default")
	}
	if sawState(evts, StateDone) {
		t.Errorf("task completed over a patch that never verified; states=%v", statesReached(evts))
	}
}

// TestCorrectivePatchOutsideTheFailingChangeIsRefused is the second ungated
// route to a model-authored write.
//
// EXECUTE asks the Executor whether a call needs approval and parks in
// WAIT_USER; PATCH calls the Patcher directly and asks nobody. The body comes
// from a reflection note, and the note chooses its own target by putting a path
// in it — so a reflection on a failed verify could write to a file the task had
// never touched, with no human in the loop. AST validation and the checkpoint
// do not close that: they make a bad write undoable, not unasked-for.
//
// The gate is narrow on purpose. Re-editing a file the failed verdict was
// rendered on continues work the run already authorised. Naming a different
// file is a new write, and there is already a door for that with a lock on it,
// so it is refused here and the task replans — the write is redirected to the
// tool-call path, not forbidden.
func TestCorrectivePatchOutsideTheFailingChangeIsRefused(t *testing.T) {
	patcher := &recordingPatcher{}
	body, err := json.Marshal(map[string]string{
		"path": "hooks/install.go",
		"body": "package hooks\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	cog := &scriptCog{
		turns: []CognitiveTurn{
			{ToolCalls: []ToolCall{{ID: "c1", Tool: "edit_file", Args: []byte(`{"path":"a.go"}`)}}},
			{Final: true, Text: "done"},
		},
		reflections: []ReflectionDecision{{Patch: PatchOp{Body: body}}},
	}
	exec := &recordingExec{obs: Observation{Files: []string{"a.go"}, Checkpoint: "ckpt_1"}}

	core, bus, sid := newGuardCore(t, Deps{
		Cognitive: cog, Exec: exec, Verify: &engineLikeVerifier{}, Patch: patcher,
	})
	rec := record(bus)
	if _, err := core.Submit(context.Background(), sid, "edit a.go"); err != nil {
		t.Fatal(err)
	}
	evts := rec.stop(bus)

	if ops := patcher.applied(); len(ops) != 0 {
		t.Errorf("patch applied to %q, a file the failing verdict never covered — reached disk without anyone being asked", ops[0].Path)
	}
	if codes := errorCodes(evts); !containsStr(codes, "patch_out_of_scope") {
		t.Errorf("no patch_out_of_scope error published (codes=%v); a refusal nobody can see is a hang", codes)
	}
	if cog.thinkCount() < 2 {
		t.Errorf("Think called %d time(s): the refusal must replan so the write can be re-proposed through the gated tool-call path, not dead-end the task", cog.thinkCount())
	}
}

// TestCorrectivePatchInsideTheFailingChangeStillApplies is the other half of
// the gate, and the reason it is a scope check rather than a blanket block.
// Repairing the file whose verification just failed is the entire purpose of
// the corrective-patch path; if this test ever goes red the fix has stopped
// being a gate and started being a wall.
func TestCorrectivePatchInsideTheFailingChangeStillApplies(t *testing.T) {
	patcher := &recordingPatcher{}
	body, err := json.Marshal(map[string]string{
		"path": "a.go",
		"body": "package a\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	cog := &scriptCog{
		turns: []CognitiveTurn{
			{ToolCalls: []ToolCall{{ID: "c1", Tool: "edit_file", Args: []byte(`{"path":"a.go"}`)}}},
			{Final: true, Text: "done"},
		},
		reflections: []ReflectionDecision{
			{Patch: PatchOp{Body: body}},
			{Abort: true, Note: "no"},
		},
	}
	exec := &recordingExec{obs: Observation{Files: []string{"a.go"}, Checkpoint: "ckpt_1"}}

	core, bus, sid := newGuardCore(t, Deps{
		Cognitive: cog, Exec: exec, Verify: &engineLikeVerifier{}, Patch: patcher,
	})
	rec := record(bus)
	if _, err := core.Submit(context.Background(), sid, "edit a.go"); err != nil {
		t.Fatal(err)
	}
	evts := rec.stop(bus)

	ops := patcher.applied()
	if len(ops) == 0 {
		t.Fatalf("an in-scope corrective patch was refused; codes=%v states=%v", errorCodes(evts), statesReached(evts))
	}
	if ops[0].Path != "a.go" {
		t.Errorf("patch applied with Path %q, want %q: the target the reflection named has to reach the patch engine or the adapter cannot find the file", ops[0].Path, "a.go")
	}
}

// TestReplanDiscardsTheUndrainedRemainder documents behaviour rather than
// fixing it. PLAN's assignment of the turn's tool calls carried a comment
// claiming it could never clobber an undrained queue "because PLAN is only
// re-entered once the queue is empty". That is false: T14, verify-fail-replan,
// re-enters PLAN with calls still pending, and this is what happens to them.
//
// Losing them is correct. A replan says the plan those calls belonged to was
// wrong, and VERIFY has just rolled the checkpoint back, so the calls queued
// behind the failed one were chosen assuming a step that did not happen. The
// comment was wrong about the mechanism, not the outcome; this test pins the
// outcome so a future "fix" that preserves the remainder has to argue with it.
func TestReplanDiscardsTheUndrainedRemainder(t *testing.T) {
	cog := &scriptCog{
		turns: []CognitiveTurn{
			{ToolCalls: []ToolCall{
				{ID: "c1", Tool: "first"},
				{ID: "c2", Tool: "second"},
			}},
			{Final: true, Text: "done"},
		},
		reflections: []ReflectionDecision{{Replan: true, Note: "start over"}},
	}
	// Files non-empty so the verifier fails the first call, as the engine-like
	// double does for anything it is actually given.
	exec := &recordingExec{obs: Observation{Files: []string{"a.go"}}}

	core, bus, sid := newGuardCore(t, Deps{
		Cognitive: cog, Exec: exec, Verify: &engineLikeVerifier{},
	})
	rec := record(bus)
	if _, err := core.Submit(context.Background(), sid, "two calls, first fails"); err != nil {
		t.Fatal(err)
	}
	evts := rec.stop(bus)

	if got := exec.dispatched(); len(got) != 1 || got[0] != "first" {
		t.Errorf("dispatched %v, want just [first]: a replan abandons the rest of the turn", got)
	}
	if !sawState(evts, StatePlan) {
		t.Errorf("never re-entered PLAN; states=%v", statesReached(evts))
	}
}

func containsStr(hay []string, needle string) bool {
	for _, s := range hay {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
