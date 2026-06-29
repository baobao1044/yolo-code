// Tests for TUI-007 — High-frequency coalescing (File 14 §14.9.3). A fast
// stream (llm.token/llm.thinking at hundreds/sec) must NOT peg the render
// thread. fold accumulates every delta into the live bubble (TUI-002), and a
// 60 Hz tickMsg drives the repaint + spinner — so 200 tokens/sec become 60
// repaints/sec while every delta is preserved (the accumulated text is always
// correct; only the repaint is throttled).
//
// Two invariants:
//  1. No deltas dropped — feed 200 tokens, the live bubble contains all 200.
//  2. tick advances the spinner frame + schedules the next tick (60 Hz).

package tui

import (
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestCoalesceNoDeltasDropped pins §14.9.3: a burst of 200 llm.token deltas
// folds into the live bubble with ALL 200 present (no drops). The repaint is
// throttled separately (60 Hz); the accumulation is the correctness invariant.
// This guards against a future change that overwrites instead of appends (which
// would lose deltas — the mutation).
func TestCoalesceNoDeltasDropped(t *testing.T) {
	m := newModelForTest()
	for i := 0; i < 200; i++ {
		m, _ = fold(m, env(&event.TokenEvent{Task: "t_1", Delta: "x"}))
	}
	if len(m.liveAssistant) != 200 {
		t.Errorf("liveAssistant length = %d, want 200 (no deltas dropped — all 200 fold in)", len(m.liveAssistant))
	}
}

// TestCoalesceThinkingNoDeltasDropped is the same invariant for llm.thinking.
func TestCoalesceThinkingNoDeltasDropped(t *testing.T) {
	m := newModelForTest()
	for i := 0; i < 300; i++ {
		m, _ = fold(m, env(&event.ThinkingEvent{Task: "t_1", Delta: "y"}))
	}
	if len(m.thinking) != 300 {
		t.Errorf("thinking length = %d, want 300 (no deltas dropped)", len(m.thinking))
	}
}

// TestTickAdvancesSpinnerFrame pins §14.9.3: a tickMsg advances the spinner
// frame (so the spinner animates at 60 Hz, not per-event). The frame must move
// forward by exactly one per tick — a frozen frame is a visible bug.
func TestTickAdvancesSpinnerFrame(t *testing.T) {
	m := newModelForTest()
	m.spinnerFrame = 3

	m2, cmd := tick(m)

	if m2.spinnerFrame != 4 {
		t.Errorf("spinnerFrame = %d after tick, want 4 (advanced by one)", m2.spinnerFrame)
	}
	_ = cmd
}

// TestTickSchedulesNextTick pins §14.9.3: tick returns a tea.Cmd that schedules
// the next tick at ~60 Hz (≈16ms). The Cmd must be non-nil so the repaint loop
// continues; a nil Cmd would freeze the spinner. We can't assert the exact
// interval without flakiness, but non-nil + a tickMsg return type pins the
// scheduler exists.
func TestTickSchedulesNextTick(t *testing.T) {
	m := newModelForTest()
	_, cmd := tick(m)
	if cmd == nil {
		t.Fatal("tick returned nil Cmd — the 60 Hz repaint loop would freeze (no next tick scheduled)")
	}
	msg := cmd()
	if _, ok := msg.(tickMsg); !ok {
		t.Errorf("next-tick Cmd produced %T, want tickMsg (the scheduler must re-arm)", msg)
	}
}

// TestTickIntervalIsSixtyHz pins the §14.9.3 interval: the next-tick Cmd fires
// at ~16ms (60 Hz). Asserting the exact delay would be flaky, so this checks
// the configured interval constant is in the 60 Hz band (10–25ms — fast enough
// to feel live, slow enough to not peg the render thread).
func TestTickIntervalIsSixtyHz(t *testing.T) {
	if tickInterval < 10*time.Millisecond || tickInterval > 25*time.Millisecond {
		t.Errorf("tickInterval = %v, want 10–25ms (60 Hz band, §14.9.3)", tickInterval)
	}
}
