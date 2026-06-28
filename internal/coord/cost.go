// cost.go — shared cost budget (File 12 + L11-006, backed by infra.Cost L12-008).
//
// The orchestrator registers the plan once (NewTask) and checks the deadline
// (Snapshot) before each dispatch; every agent event shares the PlanID so
// infra.Cost aggregates spend across agents. This Budget wraps the CostLedger
// seam with the §12.6 shared-budget invariants:
//
//   - Register is idempotent (one ledger entry per plan, NOT per agent).
//   - CheckBeforeDispatch returns false (abort) when the ledger's deadline is
//     past; true otherwise (including when no deadline is set → no enforcement).
//   - End closes the ledger entry (called once at run completion).
//
// Spec gap (Decision 2 + Sprint 10 design): real accrual happens in infra.Cost
// via the cost.* subscription (L12-008) as agent events flow through the bus
// (all sharing PlanID). The Budget registers + checks only; it does not itself
// accrue dollars/loops. The composition root injects the real *infra.Cost as
// the CostLedger; Sprint 10 tests use a fake.

package coord

import (
	"sync"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// Budget is the shared cost-budget handle (File 12 §12.6). One per plan; every
// agent turn consults it before dispatch so a runaway plan aborts at the
// deadline rather than spending unboundedly.
type Budget struct {
	id      event.TaskID
	ledger  CostLedger
	mu      sync.Mutex
	regOnce sync.Once
	endOnce sync.Once
}

// NewBudget wraps a CostLedger for a plan id. Does NOT register yet — call
// Register once the run starts.
func NewBudget(id event.TaskID, ledger CostLedger) *Budget {
	return &Budget{id: id, ledger: ledger}
}

// Register records the plan's ledger entry. Idempotent (sync.Once): a second
// call is a no-op — the §12.6 invariant is one entry per plan, not per agent.
func (b *Budget) Register() {
	b.regOnce.Do(func() {
		if b.ledger != nil {
			b.ledger.NewTask(b.id)
		}
	})
}

// CheckBeforeDispatch returns true if the plan is within its cost deadline
// (the orchestrator may dispatch another agent turn) or false if the deadline
// is past (abort — no further dispatch). When the ledger has no deadline for
// the task (ok=false), there is no enforcement, so the check returns true
// (proceed).
func (b *Budget) CheckBeforeDispatch() bool {
	if b.ledger == nil {
		return true
	}
	_, _, _, deadline, ok := b.ledger.Snapshot(b.id)
	if !ok {
		return true // no deadline set → no budget enforcement
	}
	return time.Now().Before(deadline)
}

// End closes the plan's ledger entry. Idempotent (sync.Once): a second call is
// a no-op. Called once at run completion (success or abort).
func (b *Budget) End() {
	b.endOnce.Do(func() {
		if b.ledger != nil {
			b.ledger.EndTask(b.id)
		}
	})
}
