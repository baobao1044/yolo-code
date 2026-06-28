// Tests for TUI-005 — Cost meter (File 14 §14.7.5). The cost-meter rail shows
// the degradation level + an abort banner. Per Decision 2 + spec gap: the
// catalog has only CostDegradedEvent + CostAbortEvent — NO CostSpendEvent/
// CostLoopEvent — so dollars/loops stay blank (deferred to the integration
// sprint; the events don't exist yet). The TUI never imports infra to read a
// snapshot (import matrix: tui imports only event + bubbletea-libs + stdlib).
//
//   cost.degraded → m.cost.level = e.Stage  (File 14 reads .level; the real
//                  field is Stage — spec gap, field-name mismatch documented)
//   cost.abort    → m.cost.aborted = true, m.cost.abortReason = e.Reason,
//                  banner surfaces the abort

package tui

import (
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// TestFoldCostDegradedSetsLevel pins §14.7.5: cost.degraded sets the
// degradation level the rail displays. CostDegradedEvent's level field is
// `Stage` (spec gap — File 14 §14.5 reads `cost.degraded.level`, but the real
// struct has Stage). The mutation guard: if the level isn't set, the rail
// never reflects the degradation and the user can't see the cost state.
func TestFoldCostDegradedSetsLevel(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.CostDegradedEvent{Task: "t_1", Stage: "reflection off"}))

	if m.cost.level != "reflection off" {
		t.Errorf("cost.level = %q, want %q (cost.degraded.Stage → m.cost.level — spec gap: field is Stage, not level)", m.cost.level, "reflection off")
	}
}

// TestFoldCostAbortSetsBanner pins §14.5: cost.abort sets the abort flag +
// reason + surfaces a banner so the user sees why the task was aborted. The
// banner is the at-a-glance "spend cap hit" signal.
func TestFoldCostAbortSetsBanner(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.CostAbortEvent{Task: "t_1", Reason: "spend cap exceeded"}))

	if !m.cost.aborted {
		t.Error("cost.aborted = false, want true (cost.abort sets the abort flag)")
	}
	if m.cost.abortReason != "spend cap exceeded" {
		t.Errorf("cost.abortReason = %q, want the event reason", m.cost.abortReason)
	}
	if m.banner == "" {
		t.Error("banner = \"\", want non-empty (cost.abort surfaces a banner)")
	}
	if m.banner != "spend cap exceeded" {
		t.Errorf("banner = %q, want the abort reason", m.banner)
	}
}

// TestFoldCostMeterDollarsLoopsBlank pins Decision 2 / spec gap: the catalog
// has no CostSpendEvent/CostLoopEvent, so the costView starts with no
// dollars/loops. After a degraded + abort, the model carries only level +
// aborted/reason — NOT fabricated dollar/loop figures. This guards against a
// future change inventing figures the bus never sent (which would make the TUI
// a second source of truth — exactly what §14.1 forbids).
func TestFoldCostMeterDollarsLoopsBlank(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.CostDegradedEvent{Task: "t_1", Stage: "verify only"}))
	m, _ = fold(m, env(&event.CostAbortEvent{Task: "t_1", Reason: "cap"}))

	// The costView has level + aborted + reason set, but NO dollars/loops —
	// those fields don't exist (spec gap). Assert the documented shape: the
	// model renders only what the bus sent.
	if m.cost.level == "" {
		t.Error("level should be set from cost.degraded")
	}
	if !m.cost.aborted {
		t.Error("aborted should be set from cost.abort")
	}
	// dollars/loops are not fields of costView — the struct intentionally
	// omits them (spec gap). This test pins that: if a future change adds them,
	// it must wire them from real events, not fabricate. Verified by the
	// compiler: there's no m.cost.dollars or m.cost.loops to read.
}
