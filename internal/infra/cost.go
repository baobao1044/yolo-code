// L12-008 — Cost ledger wrap: snapshot API (File 13 §13.10.1).
//
// infra.Cost is the §13.10.1 split's accounting-side handle. It owns NO
// accounting of its own — dollars/loops are delegated to a CostLedger seam
// (the cognitive controller, single source of truth), so there is one ledger,
// not two. infra.Cost owns only:
//   - the task-id registry (for Snapshot's ok), and
//   - the per-task deadline recorded at NewTask (task-start metadata, set from
//     cfg.Cost.Deadline — NOT accounting; the single-ledger invariant is about
//     dollars/loops, which stay delegated).
//
// The CostLedger seam takes event.TaskID so infra never imports cognitive or
// session (import matrix, §13.1.2 lint gate). The composition root (L12-009)
// supplies a *cognitive.Cost via a type-converting adapter (event.TaskID ↔
// session.TaskID), exactly as L12-005 wired *Secrets into exec.
//
// Spec gaps documented here:
//   - tokens: cognitive.Cost has no public token accessor (tokensIn/tokensOut
//     are unexported); Snapshot returns 0 until L6 exposes a read path. Not
//     exit-bar-testable — the exit bar (§13.10.1) checks dollars/loops only.
//   - EndTask: cognitive.Cost exposes no task-deletion API, so EndTask is a
//     documented no-op (a hardening sprint that adds deletion to L6 wires it).

package infra

import (
	"sync"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// CostLedger is the seam infra.Cost delegates accounting reads + task
// registration to. The cognitive controller (Sprint 3) is the single source of
// truth for dollars/loops; infra.Cost is the read/snapshot handle the spec wants
// L2/L6 to call. Defined here (not in cognitive) so infra never imports
// cognitive — the composition root supplies a *cognitive.Cost via a
// type-converting adapter that bridges event.TaskID ↔ session.TaskID.
type CostLedger interface {
	// RegisterTask starts the ledger entry for a task (delegates to
	// cognitive.Cost.RegisterTask). Called once per task from infra.Cost.NewTask.
	RegisterTask(id event.TaskID)
	// Dollars returns the accrued dollar spend for a task (delegates to
	// cognitive.Cost.Dollars). Only called by Snapshot when the id is known
	// (NewTask was called), so the ledger's read-auto-creates side effect never
	// fires for an unknown id.
	Dollars(id event.TaskID) float64
	// Loops returns the task's reflection-loop count (delegates to
	// cognitive.Cost.Loops). Same known-id guard as Dollars.
	Loops(id event.TaskID) int
}

// Cost is the §13.10.1 snapshot wrapper. It holds no accounting: dollars/loops
// read through `ledger` (single source of truth). It owns the task-id set (so
// Snapshot's ok is decided without a ledger read that would auto-create an
// entry) and the per-task deadline recorded at NewTask.
type Cost struct {
	mu        sync.Mutex
	ledger    CostLedger
	known     map[event.TaskID]struct{}
	deadlines map[event.TaskID]time.Time
	deadline  time.Duration // cfg.Cost.Deadline, copied at construction
}

// NewCost builds the snapshot wrapper around `ledger`. A nil ledger is allowed:
// NewTask still records the id + deadline, and Snapshot returns ok=true with
// zero dollars/loops (the delegation guard skips a nil seam), matching the
// nil-safety discipline of L12-005's *Secrets.
func NewCost(cfg Config, ledger CostLedger) *Cost {
	return &Cost{
		ledger:    ledger,
		known:     map[event.TaskID]struct{}{},
		deadlines: map[event.TaskID]time.Time{},
		deadline:  cfg.Cost.Deadline,
	}
}

// NewTask registers a task. Idempotent: a second call for the same id is a
// no-op — the task keeps its original deadline, and the ledger's RegisterTask is
// NOT called again (re-registering would reset a running task's deadline in the
// cognitive controller). Delegates the first registration to the ledger so the
// controller has the entry whenever Snapshot says ok=true.
func (c *Cost) NewTask(id event.TaskID) {
	c.mu.Lock()
	if _, seen := c.known[id]; seen {
		c.mu.Unlock()
		return
	}
	c.known[id] = struct{}{}
	c.deadlines[id] = time.Now().Add(c.deadline)
	c.mu.Unlock()
	if c.ledger != nil {
		c.ledger.RegisterTask(id)
	}
}

// Snapshot returns the task's accrued dollars/loops (delegated — single source
// of truth) + the deadline recorded at NewTask + ok=true iff NewTask was called
// for this id. For an unknown id it returns zeros + ok=false WITHOUT calling
// the ledger: the cognitive controller auto-creates an entry on read (cost.go
// task()), and a pure snapshot must not trigger that side effect.
//
// tokens is a documented spec gap (§13.10.1): cognitive.Cost has no public token
// accessor (tokensIn/tokensOut are unexported); the field returns 0 until L6
// exposes a read path. Not exit-bar-testable — the exit bar checks dollars/loops.
func (c *Cost) Snapshot(id event.TaskID) (dollars float64, loops, tokens int, deadline time.Time, ok bool) {
	c.mu.Lock()
	if _, known := c.known[id]; !known {
		c.mu.Unlock()
		return 0, 0, 0, time.Time{}, false
	}
	deadline = c.deadlines[id]
	c.mu.Unlock()
	// Delegate the accounting reads outside the lock so a slow ledger can't
	// block NewTask/EndTask. The nil guard preserves nil-safety (L12-005): a nil
	// seam yields zero dollars/loops rather than a nil-interface panic.
	if c.ledger != nil {
		dollars = c.ledger.Dollars(id)
		loops = c.ledger.Loops(id)
	}
	return dollars, loops, 0, deadline, true
}

// EndTask is a documented no-op (§13.10.1 spec gap): cognitive.Cost exposes no
// task-deletion API (the controller manages its own perTask map's lifetime), so
// the snapshot wrapper has nothing to remove. A hardening sprint that adds
// deletion to L6 wires it through here. Safe to call on an unknown id.
func (c *Cost) EndTask(id event.TaskID) {}
