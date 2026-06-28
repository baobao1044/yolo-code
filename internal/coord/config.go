// config.go — orchestrator configuration (File 12 §12.4.1, §12.5.1).

package coord

// Config tunes the orchestrator. Defaults (used when zero) are applied in
// NewOrchestrator.
type Config struct {
	// MaxReworkCycles caps re-review/re-implement cycles per todo (File 12
	// §12.4.1, default 3). On exceedance the todo is Failed and surfaced in
	// the final summary — no silent infinite retry.
	MaxReworkCycles int

	// Concurrency bounds the number of inflight agent turns (File 12 §12.5.1,
	// default 1). The real default is runtime.NumCPU at the composition root;
	// Sprint 10 tests pass 1 for determinism (one todo inflight at a time).
	Concurrency int
}

// defaultConfig fills zero fields with the spec defaults.
func defaultConfig(c Config) Config {
	if c.MaxReworkCycles <= 0 {
		c.MaxReworkCycles = 3
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	return c
}
