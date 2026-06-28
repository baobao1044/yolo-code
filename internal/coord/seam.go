// seam.go — the interface seams that keep internal/coord decoupled from the
// layers it orchestrates (roadmap §15.13 import matrix). The strictest form
// is used this sprint: event + stdlib only, with every other layer behind a
// coord-local seam satisfied at the composition root. This mirrors
// runtime/ports.go, tui/seam.go, and infra/infra.go.
//
// Spec gap (Decision 2, Sprint 10 design): File 12's orchestrator references
// FindingsEvent / QuestionEvent / review.request / test.request, none of which
// are in the §5.4.7 event catalog. The orchestrator spawns the Reviewer and
// Tester DIRECTLY via the AgentRunner seam (no review.request / test.request
// events on the bus). Researcher delegation is deferred.

package coord

import (
	"context"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// Subscribable is the bus subscription seam. *event.Bus satisfies it.
// The orchestrator subscribes to "coord.>" to receive agent-produced events.
type Subscribable interface {
	Subscribe(topics ...event.Topic) <-chan event.Envelope
}

// EventPublisher is the bus publish seam. *event.Bus satisfies it. The
// orchestrator publishes plan.ready + task.assign; agents publish
// code.ready / review.verdict / test.report themselves (File 12 §12.3.1:
// agents communicate only via events).
type EventPublisher interface {
	Publish(ctx context.Context, e event.Event) error
}

// AgentRunner runs one agent turn for a role (File 12 §12.3.1 + §12.7.1). A
// real runner builds a per-role exec.Engine with a scoped tool set and drives
// the agent's loop; the agent publishes its own coord.* event(s) on
// completion. Sprint 10 uses a fake runner that publishes canned events; the
// real per-agent drive (cognitive core + exec engine + scoped tools against a
// live repo) is the integration sprint (Decision 1).
type AgentRunner interface {
	Run(ctx context.Context, role Role, task event.TaskAssignEvent) error
}

// Planner decomposes a goal into a Plan + the Mode it chose (File 12
// §12.2.1). Sprint 10 uses a heuristic (deterministic split) planner; the
// LLM-driven Planner (read-only tools) is the integration sprint.
type Planner interface {
	Plan(ctx context.Context, goal string) (Plan, Mode, error)
}

// Verifier re-verifies a merged diff (File 12 §12.6 + L11-005). The real
// verifier runs the test suite / build against the combined patch; Sprint 10
// uses a seam so merge can be tested with a fake.
type Verifier interface {
	Verify(ctx context.Context, diff string) (bool, error)
}

// CostLedger is the shared cost-budget seam (File 12 + L11-006, backed by
// infra.Cost L12-008). The orchestrator registers the plan once and checks
// the deadline before each dispatch; every agent event shares the PlanID so
// infra.Cost aggregates spend across agents. Snapshot mirrors infra.Cost's
// signature (dollars, loops, tokens, deadline, ok).
type CostLedger interface {
	NewTask(id event.TaskID)
	Snapshot(id event.TaskID) (dollars float64, loops int, tokens int, deadline time.Time, ok bool)
	EndTask(id event.TaskID)
}
