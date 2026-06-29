// Tests for L12-008 — Cost ledger wrap: snapshot API (File 13 §13.10.1).
// infra.Cost is the §13.10.1 split's accounting-side handle: it owns NO
// accounting of its own — dollars/loops are delegated to a CostLedger seam
// (the cognitive controller, single source of truth), so there is one ledger,
// not two. infra.Cost owns only the task-id registry (for Snapshot's ok) and
// the per-task deadline recorded at NewTask (task-start metadata, not
// accounting).
//
// The seam (CostLedger, defined in cost.go) takes event.TaskID so infra never
// imports cognitive/session (import matrix, §13.1.2 lint gate). The composition
// root (L12-009) supplies a *cognitive.Cost via a type-converting adapter,
// exactly as L12-005 wired *Secrets into exec. These tests use a fake ledger.
//
// Spec gaps documented in code:
//   - tokens: cognitive.Cost has no public token accessor (tokensIn/tokensOut
//     are unexported); Snapshot returns 0 until L6 exposes a read path. Not
//     exit-bar-testable — the exit bar (§13.10.1) checks dollars/loops only.
//   - EndTask: cognitive.Cost exposes no task-deletion API, so EndTask is a
//     documented no-op (a hardening sprint that adds deletion to L6 wires it).

package infra

import (
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// fakeLedger is the test double for the CostLedger seam. It records every call
// so tests can assert delegation (the right id was forwarded) AND purity
// (Snapshot on an unknown id never calls Dollars/Loops — the cognitive
// controller auto-creates entries on read, which a snapshot must not trigger).
type fakeLedger struct {
	mu          sync.Mutex
	registered  []event.TaskID
	dollarCalls []event.TaskID
	loopCalls   []event.TaskID
	dollars     map[event.TaskID]float64
	loops       map[event.TaskID]int
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{
		dollars: map[event.TaskID]float64{},
		loops:   map[event.TaskID]int{},
	}
}

func (f *fakeLedger) set(id event.TaskID, dollars float64, loops int) {
	f.dollars[id] = dollars
	f.loops[id] = loops
}

func (f *fakeLedger) RegisterTask(id event.TaskID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, id)
}

func (f *fakeLedger) Dollars(id event.TaskID) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dollarCalls = append(f.dollarCalls, id)
	return f.dollars[id]
}

func (f *fakeLedger) Loops(id event.TaskID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loopCalls = append(f.loopCalls, id)
	return f.loops[id]
}

func (f *fakeLedger) dollarCalled(id event.TaskID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.dollarCalls {
		if c == id {
			return true
		}
	}
	return false
}

func (f *fakeLedger) registerCount(id event.TaskID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.registered {
		if c == id {
			n++
		}
	}
	return n
}

// TestCostSnapshotDelegatesDollarsLoops is the §13.10.1 exit bar: Snapshot
// returns the SAME dollars/loops the ledger (cognitive controller) accrues —
// infra.Cost holds no duplicate accounting. A fake ledger seeded with
// dollars=0.42, loops=3 for "t_1" must surface those values verbatim.
func TestCostSnapshotDelegatesDollarsLoops(t *testing.T) {
	ledger := newFakeLedger()
	ledger.set("t_1", 0.42, 3)
	c := NewCost(testConfig(), ledger)
	c.NewTask("t_1")

	dollars, loops, tokens, _, ok := c.Snapshot("t_1")
	if !ok {
		t.Fatalf("Snapshot(t_1): ok=false, want true (NewTask was called)")
	}
	if dollars != 0.42 {
		t.Errorf("dollars = %v, want 0.42 (delegated from ledger, single source of truth)", dollars)
	}
	if loops != 3 {
		t.Errorf("loops = %d, want 3 (delegated from ledger, single source of truth)", loops)
	}
	// tokens is a documented spec gap (§13.10.1): L6 has no public token accessor,
	// so the wrapper reports 0 until a hardening sprint exposes one. The assertion
	// pins the gap so a future fix updates this test deliberately.
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 (spec gap: tokens not yet projected)", tokens)
	}
}

// TestCostSnapshotUnknownTaskReturnsFalseWithoutTouchingLedger pins purity
// (§13.10.1): Snapshot on an id NewTask was never called for returns ok=false
// + zeros, AND never calls the ledger's Dollars/Loops. The cognitive controller
// auto-creates an entry on read (cost.go task()); a snapshot that triggered that
// side effect would silently invent a zero-dollar ledger row, so the wrapper
// must short-circuit before delegating.
func TestCostSnapshotUnknownTaskReturnsFalseWithoutTouchingLedger(t *testing.T) {
	ledger := newFakeLedger()
	c := NewCost(testConfig(), ledger)
	c.NewTask("t_1")

	dollars, loops, _, _, ok := c.Snapshot("never")
	if ok {
		t.Fatalf("Snapshot(never): ok=true, want false (NewTask not called for this id)")
	}
	if dollars != 0 || loops != 0 {
		t.Errorf("Snapshot(never): dollars=%v loops=%d, want 0/0 (unknown task)", dollars, loops)
	}
	if ledger.dollarCalled("never") {
		t.Error("Snapshot(never) called ledger.Dollars — must not (the ledger auto-creates on read)")
	}
}

// TestCostNewTaskRegistersWithLedger pins delegation of registration: NewTask
// forwards to the ledger's RegisterTask so the cognitive controller has the
// entry whenever Snapshot says ok=true (one registration point, one ledger).
func TestCostNewTaskRegistersWithLedger(t *testing.T) {
	ledger := newFakeLedger()
	c := NewCost(testConfig(), ledger)
	c.NewTask("t_7")

	if n := ledger.registerCount("t_7"); n != 1 {
		t.Errorf("ledger.RegisterTask called %d time(s) for t_7, want 1", n)
	}
}

// TestCostNewTaskIsIdempotent pins the idempotency contract: a second NewTask
// for the same id is a no-op — RegisterTask is NOT called again (re-registering
// would reset a running task's deadline in the cognitive controller) and the
// deadline recorded at the first call is preserved.
func TestCostNewTaskIsIdempotent(t *testing.T) {
	cfg := testConfig()
	ledger := newFakeLedger()
	c := NewCost(cfg, ledger)

	c.NewTask("t_1")
	_, _, _, firstDeadline, ok := c.Snapshot("t_1")
	if !ok {
		t.Fatalf("first Snapshot: ok=false, want true")
	}
	c.NewTask("t_1") // second call must be a no-op

	if n := ledger.registerCount("t_1"); n != 1 {
		t.Errorf("ledger.RegisterTask called %d time(s), want 1 (NewTask idempotent)", n)
	}
	_, _, _, secondDeadline, _ := c.Snapshot("t_1")
	if !secondDeadline.Equal(firstDeadline) {
		t.Errorf("deadline changed on second NewTask: first=%v second=%v (must be preserved)", firstDeadline, secondDeadline)
	}
}

// TestCostSnapshotDeadlineFromNewTask pins the deadline field: NewTask records
// it as time.Now().Add(cfg.Cost.Deadline), so Snapshot returns a non-zero
// deadline in the near future (within the configured window + slack).
func TestCostSnapshotDeadlineFromNewTask(t *testing.T) {
	cfg := testConfig()
	cfg.Cost.Deadline = 10 * time.Minute
	ledger := newFakeLedger()
	c := NewCost(cfg, ledger)
	before := time.Now()
	c.NewTask("t_1")

	_, _, _, deadline, ok := c.Snapshot("t_1")
	if !ok {
		t.Fatalf("Snapshot: ok=false, want true")
	}
	if deadline.IsZero() {
		t.Fatal("deadline is zero, want a real time set at NewTask")
	}
	if remaining := time.Until(deadline); remaining <= 0 {
		t.Errorf("deadline is in the past (remaining=%v), want in the future", remaining)
	}
	if remaining := time.Until(deadline); remaining > cfg.Cost.Deadline {
		t.Errorf("deadline remaining=%v, want <= cfg.Cost.Deadline=%v", remaining, cfg.Cost.Deadline)
	}
	// Sanity: the recorded deadline is ~now + Deadline (allow generous slack for
	// scheduler jitter across the 3× stability run).
	elapsed := time.Since(before)
	if deadline.Sub(before) > elapsed+cfg.Cost.Deadline {
		t.Errorf("deadline = %v after start, want ~%v", deadline.Sub(before), cfg.Cost.Deadline)
	}
}

// TestCostEndTaskIsNoOp pins the documented spec gap: EndTask removes nothing
// (cognitive.Cost exposes no deletion API). After EndTask, Snapshot still
// returns ok=true; calling EndTask on an unknown id does not panic.
func TestCostEndTaskIsNoOp(t *testing.T) {
	ledger := newFakeLedger()
	c := NewCost(testConfig(), ledger)
	c.NewTask("t_1")

	c.EndTask("t_1") // documented no-op
	if _, _, _, _, ok := c.Snapshot("t_1"); !ok {
		t.Error("EndTask removed t_1 from the registry, want no-op (controller exposes no deletion)")
	}
	c.EndTask("never") // unknown id — must not panic
}

// TestCostSnapshotNilLedgerReturnsZeros pins nil-safety (matching L12-005's
// nil *Secrets discipline): a Cost built with a nil ledger does not panic —
// NewTask still records the id + deadline, and Snapshot returns ok=true with
// zero dollars/loops (the guard skips delegation).
func TestCostSnapshotNilLedgerReturnsZeros(t *testing.T) {
	c := NewCost(testConfig(), nil)
	c.NewTask("t_1")

	dollars, loops, _, _, ok := c.Snapshot("t_1")
	if !ok {
		t.Fatalf("Snapshot(t_1) with nil ledger: ok=false, want true (NewTask recorded the id)")
	}
	if dollars != 0 || loops != 0 {
		t.Errorf("nil-ledger Snapshot: dollars=%v loops=%d, want 0/0 (guard skips delegation)", dollars, loops)
	}
}
