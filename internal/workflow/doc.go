// Package workflow implements Dynamic Workflow dispatch — a sibling of the
// runtime layer (Layer 2) in the import matrix (File 15 §15.15.2). It selects a
// workflow per task type — bugfix, feature, or refactor — then drives it with
// conditional branching, multi-hypothesis exploration, repair loops, and scope
// contraction. An Engine classifies the goal, looks up the matching Workflow in
// a registry (falling back to a default), and asks it for the next Action given
// the current State and the latest event. The package depends only on the event
// bus and the standard library: like scope, it owns no goroutine and does no I/O,
// so the per-type state machines are unit-testable in isolation.
package workflow
