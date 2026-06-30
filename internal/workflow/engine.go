package workflow

import (
	"context"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Engine is the Dynamic Workflow dispatcher. It owns a registry of Workflows
// keyed by WorkflowType, a Classifier that maps a goal to a type, a default
// workflow for when classification finds no registered match, and an event bus
// (nil-safe) it publishes selection events to. The engine itself holds no
// per-task state — that lives in the State the drive loop threads through Next.
type Engine struct {
	registry   map[WorkflowType]Workflow
	classifier Classifier
	defaultWF  Workflow
	bus        *event.Bus // nil-safe: best-effort publish
}

// New returns an Engine with the three built-in workflows (bugfix, feature,
// refactor) registered, the heuristic classifier wired, and BugFixWorkflow as the
// default. The bus may be nil; the engine then skips publishing and never
// panics.
func New(bus *event.Bus) *Engine {
	e := &Engine{
		registry:   make(map[WorkflowType]Workflow),
		classifier: NewClassifier(),
		defaultWF:  BugFixWorkflow{},
		bus:        bus,
	}
	e.Register(TypeBugfix, BugFixWorkflow{})
	e.Register(TypeFeature, FeatureWorkflow{})
	e.Register(TypeRefactor, RefactorWorkflow{})
	return e
}

// Register adds wf under type t, panicking on a duplicate (mirroring the exec
// tool registry). Workflows register once at startup; a clash means two
// workflows fighting over a type that should surface loudly, not be silently
// shadowed.
func (e *Engine) Register(t WorkflowType, wf Workflow) {
	if _, dup := e.registry[t]; dup {
		panic("workflow: duplicate workflow type: " + string(t))
	}
	e.registry[t] = wf
}

// Select classifies goal, looks the type up in the registry, and falls back to
// the default workflow when the type is unknown. It publishes a
// WorkflowSelectedEvent recording the chosen workflow and the goal; publishing
// is best-effort and nil-bus-safe (a nil bus is a no-op and never panics).
func (e *Engine) Select(goal string, state *State) Workflow {
	wf := e.selectNoPublish(goal)
	if e.bus != nil {
		_ = e.bus.Publish(context.Background(), &event.WorkflowSelectedEvent{
			Task:     "",
			Goal:     goal,
			Workflow: wf.Name(),
		})
	}
	return wf
}

// selectNoPublish is the pure classify+lookup half of Select, with no bus
// publish. Next uses it so the per-turn routing decision (called from the
// runtime's PLAN arm) never publishes on every loop iteration — only Select
// (the explicit, less-frequent entry) does. This avoids a publish storm and
// keeps Next safe to call inside a tight drive loop.
func (e *Engine) selectNoPublish(goal string) Workflow {
	wf, ok := e.registry[e.classifier.Classify(goal)]
	if !ok {
		wf = e.defaultWF
	}
	return wf
}

// Default returns the fallback workflow used when classification finds no
// registered match.
func (e *Engine) Default() Workflow { return e.defaultWF }

// Next selects the workflow for goal (classifying WITHOUT publishing — see
// selectNoPublish) and dispatches the event to it.
func (e *Engine) Next(goal string, state *State, ev WFEvent) (Action, error) {
	return e.selectNoPublish(goal).Next(state, ev)
}
