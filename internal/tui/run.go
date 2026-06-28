// TUI-001 run — the bubbletea program surface (File 14 §14.2, §14.11). Update
// folds messages into the model; View renders; Run is the entry point.
//
// Run() (tea.NewProgram(...).Run()) is the one untested surface: it needs a
// TTY, so it can't be unit-tested. It's a thin driver — accepted, like
// infra.Stop. Every ticket's logic lives in the pure fold/handleInput/tick/
// View functions, which ARE tested directly. Update just dispatches to them.

package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// Update is the Elm-architecture transition (File 14 §14.2.3): it dispatches
// a message to the right pure function. Update itself does NO I/O — it only
// folds a message into the model and possibly returns a command. The actual
// work happens in fold/handleInput/tick (later tickets), which are pure.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case busMsg:
		// A bus event folds into render state + re-launches the watcher. TUI-007
		// coalesces the repaint: fold accumulates every delta, and the 60 Hz
		// tick (armed once in Init, re-arming itself in the tickMsg case) drives
		// the actual repaint — so a fast token stream doesn't paint per-event.
		return fold(m, msg.env)
	case tickMsg:
		// 60 Hz repaint + spinner advance (TUI-007). Re-arms itself via nextTick
		// so the loop continues without a per-event re-arm (which would leak
		// a goroutine per event).
		return tick(m)
	case quitMsg:
		return m, tea.Quit
	}
	return m, nil
}

// View is pure: a string from the model, no I/O (File 14 §14.11). TUI-001
// renders a minimal header line; later tickets (TUI-002→009) append the chat
// pane, status bar, diff viewer, cost rail, and board. The full layout lands
// incrementally so each ticket's View is testable in isolation.
func (m Model) View() string {
	// Minimal header for TUI-001 (File 14 §14.7.1 header): task + goal + state.
	// The lipgloss layout (rail, borders) is added in later tickets.
	if m.taskID == "" {
		return "yolo — awaiting task"
	}
	out := "task " + m.taskID
	if m.goal != "" {
		out += " · " + m.goal
	}
	if m.state != "" {
		out += "  " + m.state
	}
	return out
}

// Run is the TUI entry point (File 14 §14.11): subscribe the rendering topics,
// build the model, and block on tea.Program.Run() until tea.Quit (the
// busWatcher returns quitMsg when the bus closes or the user quits). The
// caller owns the bus + publisher (the composition root passes the real
// *event.Bus). Untested — needs a TTY; the pure surface is unit-tested.
func Run(ctx context.Context, bus Subscribable, pub EventPublisher) error {
	sub := subscribe(bus)
	cancel := make(chan struct{})
	defer close(cancel)

	m := newModel(sub, pub)
	m.cancel = cancel

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}
