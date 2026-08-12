// Tests for TUI-005 — Cost meter (File 14 §14.7.5). The cost-meter rail shows
// the degradation level + an abort banner. Phase D adds dollars + a rough
// token estimate accumulated from real events (CostIncurredEvent.Dollars +
// llm.token deltas). The TUI never imports infra to read a snapshot (import
// matrix: tui imports only event + bubbletea-libs + stdlib).
//
//   cost.degraded → m.cost.level = e.Stage  (File 14 reads .level; the real
//                  field is Stage — spec gap, field-name mismatch documented)
//   cost.abort    → m.cost.aborted = true, m.cost.abortReason = e.Reason,
//                  banner surfaces the abort
//   cost.incurred → m.cost.dollars += e.Dollars (Phase D, real rate)
//   llm.token     → m.cost.tokensEst += len(e.Delta)/4 (Phase D, rough est.)

package tui

import (
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
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

// TestFoldCostMeterDollarsAccumulate pins Phase D: CostIncurredEvent.Dollars
// accumulate into m.cost.dollars (real per-tool-call rate from the cost
// publisher — NOT fabricated). Two events sum, so the rail shows total spend.
func TestFoldCostMeterDollarsAccumulate(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.CostIncurredEvent{Task: "t_1", Dollars: 0.01}))
	if m.cost.dollars != 0.01 {
		t.Errorf("dollars = %v, want 0.01 after first cost.incurred", m.cost.dollars)
	}
	m, _ = fold(m, env(&event.CostIncurredEvent{Task: "t_1", Dollars: 0.02}))
	if m.cost.dollars != 0.03 {
		t.Errorf("dollars = %v, want 0.03 after two cost.incurred (0.01+0.02)", m.cost.dollars)
	}
}

// TestFoldCostMeterTokensEstimate pins Phase D: llm.token deltas accumulate a
// rough token estimate (len(delta)/4) into m.cost.tokensEst.
func TestFoldCostMeterTokensEstimate(t *testing.T) {
	m := newModelForTest()
	// "hello world" = 11 chars → 11/4 = 2 (integer division).
	m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "hello world"}))
	if m.cost.tokensEst != 2 {
		t.Errorf("tokensEst = %d, want 2 (11 chars / 4)", m.cost.tokensEst)
	}
	// Another 8-char delta → 8/4 = 2; total 4.
	m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "12345678"}))
	if m.cost.tokensEst != 4 {
		t.Errorf("tokensEst = %d, want 4 (2+2)", m.cost.tokensEst)
	}
}

// TestFoldCostMeterLevelStillSet pins that the degraded level is still set
// alongside the new dollars/tokens accumulation (Phase D didn't break it).
func TestFoldCostMeterLevelStillSet(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.CostDegradedEvent{Task: "t_1", Stage: "verify only"}))
	m, _ = fold(m, env(&event.CostIncurredEvent{Task: "t_1", Dollars: 0.05}))
	if m.cost.level != "verify only" {
		t.Errorf("level = %q, want 'verify only'", m.cost.level)
	}
	if m.cost.dollars != 0.05 {
		t.Errorf("dollars = %v, want 0.05", m.cost.dollars)
	}
}
