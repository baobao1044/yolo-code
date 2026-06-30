// Tests for L11-006 — shared cost budget (File 12 + L11-006, backed by
// infra.Cost L12-008).
//
// The orchestrator registers the plan once (NewTask) and checks the deadline
// (Snapshot) before each dispatch; every agent event shares the PlanID so
// infra.Cost aggregates spend across agents. Sprint 10's Budget wraps the
// CostLedger seam: it registers the plan idempotently, exposes
// CheckBeforeDispatch (returns false — abort — when the deadline is past),
// and EndTask at the end. Real accrual happens in infra.Cost via the cost.*
// subscription (L12-008); the Budget registers + checks only.

package coord

import (
	"sync"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// fakeCostLedger is a CostLedger seam that records calls + returns a canned
// deadline/snapshot. It mirrors infra.Cost's Snapshot shape (dollars, loops,
// tokens, deadline, ok).
type fakeCostLedger struct {
	mu           sync.Mutex
	newTaskCalls int
	newTaskIDs   []event.TaskID
	endTaskCalls int
	// deadline returns from Snapshot; ok returns true if deadlineIsSet.
	deadline      time.Time
	deadlineIsSet bool
	snapshotCalls int
}

func (f *fakeCostLedger) NewTask(id event.TaskID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newTaskCalls++
	f.newTaskIDs = append(f.newTaskIDs, id)
}

func (f *fakeCostLedger) Snapshot(id event.TaskID) (dollars float64, loops int, tokens int, deadline time.Time, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotCalls++
	return 0, 0, 0, f.deadline, f.deadlineIsSet
}

func (f *fakeCostLedger) EndTask(id event.TaskID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endTaskCalls++
}

// TestBudgetRegistersPlanOnce: NewBudget(planID) calls NewTask exactly once —
// re-registering the same plan is idempotent (the §12.6 shared-budget
// invariant: one ledger entry per plan, not per agent).
func TestBudgetRegistersPlanOnce(t *testing.T) {
	ledger := &fakeCostLedger{}
	b := NewBudget(event.TaskID("plan_1"), ledger)
	b.Register() // first
	b.Register() // duplicate — must not re-call NewTask
	if ledger.newTaskCalls != 1 {
		t.Errorf("NewTask called %d times, want 1 (idempotent per plan)", ledger.newTaskCalls)
	}
}

// TestBudgetCheckUnderDeadline: when the deadline is in the future,
// CheckBeforeDispatch returns true (continue dispatching).
func TestBudgetCheckUnderDeadline(t *testing.T) {
	ledger := &fakeCostLedger{
		deadline:      time.Now().Add(1 * time.Hour),
		deadlineIsSet: true,
	}
	b := NewBudget(event.TaskID("plan_1"), ledger)
	if !b.CheckBeforeDispatch() {
		t.Errorf("CheckBeforeDispatch = false, want true (deadline in the future)")
	}
}

// TestBudgetCheckDeadlineExceeded: when the deadline is in the past,
// CheckBeforeDispatch returns false (abort — the orchestrator must not
// dispatch further agent turns).
func TestBudgetCheckDeadlineExceeded(t *testing.T) {
	ledger := &fakeCostLedger{
		deadline:      time.Now().Add(-1 * time.Minute),
		deadlineIsSet: true,
	}
	b := NewBudget(event.TaskID("plan_1"), ledger)
	if b.CheckBeforeDispatch() {
		t.Errorf("CheckBeforeDispatch = true, want false (deadline exceeded → abort)")
	}
}

// TestBudgetCheckUnknownTask: when the ledger has no deadline for the task
// (ok=false), CheckBeforeDispatch returns true (no deadline set → no
// budget enforcement; the orchestrator proceeds).
func TestBudgetCheckUnknownTask(t *testing.T) {
	ledger := &fakeCostLedger{deadlineIsSet: false}
	b := NewBudget(event.TaskID("plan_1"), ledger)
	if !b.CheckBeforeDispatch() {
		t.Errorf("CheckBeforeDispatch = false, want true (no deadline set → proceed)")
	}
}

// TestBudgetEndTask: End calls the ledger's EndTask exactly once.
func TestBudgetEndTask(t *testing.T) {
	ledger := &fakeCostLedger{}
	b := NewBudget(event.TaskID("plan_1"), ledger)
	b.Register()
	b.End()
	if ledger.endTaskCalls != 1 {
		t.Errorf("EndTask called %d times, want 1", ledger.endTaskCalls)
	}
}

// TestBudgetCheckReadsSnapshot: CheckBeforeDispatch consults the ledger's
// Snapshot (the deadline comes from the ledger, not the Budget's own state).
func TestBudgetCheckReadsSnapshot(t *testing.T) {
	ledger := &fakeCostLedger{
		deadline:      time.Now().Add(1 * time.Hour),
		deadlineIsSet: true,
	}
	b := NewBudget(event.TaskID("plan_1"), ledger)
	b.CheckBeforeDispatch()
	b.CheckBeforeDispatch()
	if ledger.snapshotCalls != 2 {
		t.Errorf("Snapshot called %d times, want 2 (one per check)", ledger.snapshotCalls)
	}
}
