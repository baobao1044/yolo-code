package scope

import (
	"context"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Controller drives the scope loop (File 15 §15.x). It owns the current Level,
// the LIFO history of levels entered, the working State, and the anti-loop
// Memory. It consults the W2 permission table to gate tool access and the W3
// rules to suggest expansions and contractions. All bus interaction is
// best-effort: a nil bus is safe — Enter never panics and simply skips the
// publish.
type Controller struct {
	current Level
	history []Level
	state   State
	memory  *Memory
	bus     *event.Bus // nil-safe: best-effort publish
}

// New returns a Controller ready to drive a scope loop, starting at LevelTask.
// The bus may be nil; the controller then skips publishing and never panics.
func New(bus *event.Bus) *Controller {
	return &Controller{
		current: LevelTask,
		state:   State{Level: LevelTask},
		memory:  NewMemory(),
		bus:     bus,
	}
}

// Current returns the level the controller is operating at right now.
func (c *Controller) Current() Level { return c.current }

// State returns the controller's working state. The returned value copies the
// level; slice fields share backing storage and are read-only by contract.
func (c *Controller) State() State { return c.state }

// Memory returns the controller's anti-loop memory.
func (c *Controller) Memory() *Memory { return c.memory }

// Enter moves the controller to level, records the previous level in the
// history, and publishes a scope.enter event when a bus is wired. Publishing is
// best-effort and nil-bus-safe: a nil bus is a no-op and never panics. The
// event's Task is empty today; the runtime threads the real causal id when it
// owns the controller.
func (c *Controller) Enter(level Level, reason string) {
	c.history = append(c.history, c.current)
	c.current = level
	c.state.Level = level
	if c.bus != nil {
		_ = c.bus.Publish(context.Background(), &event.ScopeEnterEvent{
			Task:   "",
			Level:  level.String(),
			Reason: reason,
		})
	}
}

// Exit pops the most recent level from the history and returns the new current
// level. If the history is empty, Exit returns the current level unchanged.
func (c *Controller) Exit() Level {
	if len(c.history) == 0 {
		return c.current
	}
	last := c.history[len(c.history)-1]
	c.history = c.history[:len(c.history)-1]
	c.current = last
	c.state.Level = last
	return c.current
}

// CanUseTool reports whether tool is permitted at the current level, delegating
// to the W2 permission table.
func (c *Controller) CanUseTool(tool string) bool {
	return LevelAllowsTool(c.current, tool)
}

// SuggestTransition wraps the W3 rules. It records the verdict's reason as a
// confirmed fact (on pass) or a failed hypothesis (on fail) before returning the
// recommended transition.
func (c *Controller) SuggestTransition(v Verdict) Transition {
	if v.Pass {
		if v.Reason != "" {
			c.memory.RecordFact(v.Reason)
		}
	} else if v.Reason != "" {
		c.memory.RecordFailedHypothesis(v.Reason)
	}
	return SuggestTransition(c.current, v)
}

// RecordFact records a confirmed fact in the controller's memory.
func (c *Controller) RecordFact(f string) { c.memory.RecordFact(f) }

// RecordFailedHypothesis records a hypothesis that did not pan out.
func (c *Controller) RecordFailedHypothesis(h string) { c.memory.RecordFailedHypothesis(h) }

// RecordPatch records a patch attempt in the controller's memory.
func (c *Controller) RecordPatch(seq int, summary string, accepted bool) {
	c.memory.RecordPatch(seq, summary, accepted)
}
