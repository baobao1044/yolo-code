// config.go — orchestrator configuration (File 12 §12.4.1, §12.5.1).

package coord

import "time"

// DefaultAgentTimeout is the per-turn cap applied when Config.AgentTimeout is
// left zero. It exists because the alternative default is "wait forever": an
// agent that returns without publishing — or a provider connection that never
// answers — used to stall the whole plan until the *parent* context expired,
// and the parent context in production (`--plan`, the TUI) has no deadline at
// all. Ten minutes is long enough that no honest turn hits it and short enough
// that a hung one is a diagnosable failure rather than a frozen process.
const DefaultAgentTimeout = 10 * time.Minute

// Config tunes the orchestrator. Defaults (used when zero) are applied in
// NewOrchestrator.
type Config struct {
	// MaxReworkCycles caps re-review/re-implement cycles per todo (File 12
	// §12.4.1, default 3). On exceedance the todo is Failed and surfaced in
	// the final summary — no silent infinite retry.
	MaxReworkCycles int

	// Concurrency bounds the number of inflight agent turns (File 12 §12.5.1,
	// default 1). Todos whose DependsOn are all terminal dispatch in parallel,
	// each on its own goroutine, up to this bound.
	//
	// A value above 1 means the AgentRunner is called from several goroutines
	// at once, so it must be goroutine-safe (see AgentRunner in seam.go). The
	// composition roots pass 1 today for exactly that reason.
	Concurrency int

	// AgentTimeout caps one agent turn: the runner's context carries this
	// deadline, and a turn that neither errors nor produces its event before
	// the deadline fails its todo instead of stalling the plan.
	//
	// Zero selects DefaultAgentTimeout — zero is "unset", never "already
	// elapsed". A NEGATIVE value is the explicit opt-out: no cap at all, which
	// restores the old behaviour of waiting for the parent context.
	AgentTimeout time.Duration

	// RoleTimeouts overrides AgentTimeout for individual roles (a tester's
	// suite run and a reviewer's read are not the same kind of wait). A role
	// with an entry uses it verbatim, with the same sign convention: ≤ 0 means
	// no cap for that role. A role with no entry uses AgentTimeout.
	//
	// Read-only after NewOrchestrator: the orchestrator consults it from every
	// agent goroutine and never writes it.
	RoleTimeouts map[Role]time.Duration
}

// defaultConfig fills zero fields with the spec defaults.
func defaultConfig(c Config) Config {
	if c.MaxReworkCycles <= 0 {
		c.MaxReworkCycles = 3
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	if c.AgentTimeout == 0 {
		c.AgentTimeout = DefaultAgentTimeout
	}
	return c
}

// turnTimeout resolves the cap for one role's turn: the role's own override
// when it has one, otherwise the plan-wide AgentTimeout. A non-positive result
// means "no cap" and the caller must not build a deadline from it — a zero
// duration handed to context.WithTimeout is a context that is already expired,
// which is the opposite of what an unset limit means.
func (c Config) turnTimeout(role Role) time.Duration {
	if d, ok := c.RoleTimeouts[role]; ok {
		return d
	}
	return c.AgentTimeout
}
