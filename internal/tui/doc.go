// Package tui implements the terminal interface — a subscribe-only renderer
// built on bubbletea/lipgloss/bubbles (File 14).
//
// Architectural invariant (File 14 §14.1.1): the TUI holds no state machine
// of its own and may import ONLY `event` plus the bubbletea/lipgloss/bubbles
// libraries and the standard library. It must never import another layer.
package tui
