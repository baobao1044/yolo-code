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
//   cost.incurred → m.cost.calls++ (exact) and m.cost.dollars += e.Dollars,
//                  which is 0 unless the operator set YOLO_COST_RATES
//   llm.token     → m.cost.tokensEst += len(e.Delta)/4 (rough est.)

package tui

import (
	"strings"
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

// TestCostRailNeverShowsAnUnpricedZeroAsMeasured pins the honesty rule for the
// rail. The cost publisher emits Dollars: 0 unless the operator supplied
// YOLO_COST_RATES, so the common case is real tool calls at an unknown price.
// Rendering "$0.00" there states a measured spend of zero, which is a stronger
// and different claim than "we don't have your rates". The rail must lead with
// the call count — the one exact number it has — and name money only when the
// operator asked for it, attributed to their own rate table.
func TestCostRailNeverShowsAnUnpricedZeroAsMeasured(t *testing.T) {
	m := newModelForTest()
	for range 3 {
		m, _ = fold(m, env(&event.CostIncurredEvent{Task: "t_1", Dollars: 0}))
	}
	m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "12345678"}))

	rail := railView(m, 80, 20)
	if strings.Contains(rail, "$0.00") {
		t.Errorf("rail renders an unpriced run as a measured $0.00:\n%s", rail)
	}
	if !strings.Contains(rail, "3 tool calls") {
		t.Errorf("rail omits the call count, its only exact number:\n%s", rail)
	}

	// With rates configured the money reappears, marked as an estimate and
	// attributed — the fix must not amount to hiding the number.
	m, _ = fold(m, env(&event.CostIncurredEvent{Task: "t_1", Dollars: 0.25}))
	rail = railView(m, 80, 20)
	if !strings.Contains(rail, "~$0.25 (your rates)") {
		t.Errorf("rail drops operator-priced spend instead of attributing it:\n%s", rail)
	}
}

// TestFoldCostMeterDollarsAccumulate pins that CostIncurredEvent.Dollars
// accumulate into m.cost.dollars. The value is the operator's own rate (0
// unless YOLO_COST_RATES is set); what is pinned here is the summing, not any
// claim that the figure is measured. Two events sum.
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

// TestFoldCostMeterTokensEstimate pins Phase D's token estimate the way the
// stream actually arrives: one llm.token event per provider chunk, i.e. per
// token, each a handful of characters. The estimate is chars/4, so the division
// belongs at the END of the accumulation — dividing every delta throws away a
// remainder per token, and on a stream of short deltas the whole figure is lost.
//
// The old version of this test used only an 11-char and an 8-char delta, sizes
// where truncate-per-delta and divide-once agree, and it read the truncation as
// intentional ("11/4 = 2 (integer division)"). A test that passes under both
// implementations pins nothing; these deltas are chosen so the two disagree.
func TestFoldCostMeterTokensEstimate(t *testing.T) {
	m := newModelForTest()
	// A realistic short-token stream: 28 deltas, 127 characters total.
	stream := []string{
		"The", " quick", " brown", " fox", " jumps", " over", " the", " lazy",
		" dog", ".", " It", " then", " turns", " around", " and", " walks",
		" back", " to", " the", " spot", " it", " started", " from", ",",
		" twice", " over", " again", ".",
	}
	chars := 0
	for _, d := range stream {
		chars += len(d)
		m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: d}))
	}
	if chars != 127 {
		t.Fatalf("test fixture drifted: stream is %d chars, want 127", chars)
	}
	want := chars / 4 // 31 — divide once, at the end
	rail := railView(m, 80, 20)
	if !strings.Contains(rail, "~31 tok") {
		t.Errorf("rail shows a truncated-per-delta token count, want ~%d tok (%d chars / 4):\n%s", want, chars, rail)
	}
}

// TestCostRailNeverShowsAZeroTokenCountForAWholeStream is the pathological end
// of the same bug: a provider that streams one character at a time made every
// delta truncate to zero, so the rail rendered a measured-looking "~0 tok" after
// five thousand tokens. Same honesty rule as the dollars half above — the rail
// must not present a number it did not measure, and zero is a number.
func TestCostRailNeverShowsAZeroTokenCountForAWholeStream(t *testing.T) {
	m := newModelForTest()
	for range 3 {
		m, _ = fold(m, env(&event.CostIncurredEvent{Task: "t_1"}))
	}
	for range 5000 {
		m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "a"}))
	}
	rail := railView(m, 80, 20)
	if strings.Contains(rail, "~0 tok") {
		t.Errorf("rail reports ~0 tok after 5000 one-character deltas:\n%s", rail)
	}
	if !strings.Contains(rail, "~1250 tok") {
		t.Errorf("rail token estimate = wrong for 5000 chars, want ~1250 tok (5000/4):\n%s", rail)
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
