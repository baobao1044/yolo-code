// TUI-001 seams — the minimal interfaces that let the TUI subscribe + publish
// without importing the bus concretely, so tests pass fakes (File 14 §14.3,
// §14.8). *event.Bus satisfies both. This is the same seam-first discipline as
// infra's logRedactor / sentryRedactor / CostLedger: define the surface where
// it's consumed, satisfy it at the composition root.
//
// The TUI may import ONLY `event` + bubbletea/lipgloss/bubbles + stdlib
// (File 14 §14.1.1, enforced by TUI-008). These seams take event types so the
// import matrix stays clean.

package tui

import (
	"context"

	"github.com/yolo-code/yolo/internal/event"
)

// Subscribable is the bus-seam the TUI reads events from. *event.Bus satisfies
// it (Subscribe is variadic + supports "prefix.>" wildcards). A test fake
// records the topic list; the composition root passes the real bus.
type Subscribable interface {
	Subscribe(topics ...event.Topic) <-chan event.Envelope
}

// EventPublisher is the bus-seam the TUI writes user.* events through.
// *event.Bus satisfies it (Publish returns an error on a closed bus). A test
// fake captures the published event so input tests assert exactly which user.*
// was emitted — no real-bus read, no timing flakiness.
type EventPublisher interface {
	Publish(ctx context.Context, e event.Event) error
}
