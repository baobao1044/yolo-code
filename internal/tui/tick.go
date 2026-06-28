// TUI-007 — High-frequency coalescing (File 14 §14.9.3). A fast stream
// (llm.token/llm.thinking at hundreds/sec) must NOT peg the render thread.
// fold already accumulates every delta into the live bubble (TUI-002 — the
// accumulated text is always correct). tick drives the repaint + spinner at
// 60 Hz instead of per-event, so 200 tokens/sec become 60 repaints/sec while
// every delta is preserved (only the repaint is throttled).
//
// tick is a pure (Model, Cmd) transition: it advances the spinner frame and
// returns a tea.Cmd that fires the next tickMsg at ~16ms (60 Hz). The re-arm
// keeps the spinner animating and the live bubble repainting without a
// per-event paint.

package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tickInterval is the repaint cadence (File 14 §14.9.3): 60 Hz ≈ 16ms. Fast
// enough to feel live, slow enough that a fast token stream doesn't peg the
// render thread (200 tokens/sec → 60 repaints/sec). Exported-by-name (lowercase)
// so the interval-band test can pin it without exposing a setter.
const tickInterval = 16 * time.Millisecond

// tickMsg is the periodic repaint signal. Produced by the next-tick Cmd; Update
// folds it via tick() to advance the spinner + re-arm the scheduler.
type tickMsg struct{}

// tick advances the spinner frame by one and returns a tea.Cmd that fires the
// next tickMsg after tickInterval (60 Hz). Pure — no I/O. The re-arm keeps the
// spinner animating; the live bubble repaints at this cadence, not per delta.
func tick(m Model) (Model, tea.Cmd) {
	m.spinnerFrame++
	return m, nextTick
}

// nextTick is the tea.Cmd that sleeps tickInterval then emits a tickMsg, so the
// 60 Hz loop re-arms itself. Runs off the render thread (it blocks on a timer),
// so Update stays free to repaint + read stdin between ticks.
func nextTick() tea.Msg {
	time.Sleep(tickInterval)
	return tickMsg{}
}
