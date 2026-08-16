// Backpressure test for publishUpdate. The listener publishes memory.update
// from inside its own drain loop; with a blocking Publish that is a park on a
// subscriber the drain goroutine is itself responsible for relieving, which
// turns a slow consumer of a cosmetic event into a stalled agent.

package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestSlowMemoryUpdateSubscriberDoesNotStallTheSpine is the live-hang driver.
//
// A subscriber to memory.update that stops reading fills its 64-slot buffer.
// With a blocking Publish, the memory listener parks on delivery number 65 —
// and the goroutine parked there is the one draining the listener's own
// channel, so that channel fills too and the *publisher* (here the test, in
// production the drive loop) blocks behind it. Nothing breaks the cycle short
// of closing the bus.
//
// The claim that this was latent because nothing subscribes to memory.update is
// wrong: tui/bus.go lists it in renderTopics and tui/fold.go renders it, and
// three wildcard Subscribe(">") subscribers see it as well. A TUI fan-in 64
// events behind is enough.
func TestSlowMemoryUpdateSubscriberDoesNotStallTheSpine(t *testing.T) {
	bus := event.New()
	// Subscribed and never drained: the TUI whose render loop fell behind.
	_ = bus.Subscribe(event.Topic("memory.update"))

	s, err := Open(Deps{Root: t.TempDir(), Bus: bus})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = bus.Close()
		_ = s.Close()
	})

	// 200 is comfortably past both 64-slot buffers: the memory.update buffer
	// fills first, then the listener's own if it parked.
	const n = 200
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < n; i++ {
		if err := bus.Publish(ctx, &event.StateChangeEvent{
			Task: "t_1", From: "THINK", To: "ACT", Why: "step",
		}); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("the spine stalled after %d/%d publishes: a memory.update "+
					"subscriber that stopped reading blocked the memory listener, "+
					"which blocked the publisher", i, n)
			}
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// The listener must have kept up: every event applied, none still queued.
	waitFor(t, "the listener to drain all 200 events", func() bool {
		return s.working.State() == "ACT" && len(s.ch) == 0
	})

	// The loss is accounted for, not silent — 200 updates into a 64-slot buffer
	// that nobody reads has to show up somewhere.
	if dropped := bus.Stats().Dropped; dropped == 0 {
		t.Errorf("Stats().Dropped = 0; a dropped memory.update must be counted")
	}
}

// TestStoreCloseReturnsAfterASlowUpdateSubscriber pins the shutdown half:
// Store.Close waits on the drain goroutine, so a drain parked forever in
// publishUpdate takes Close down with it.
func TestStoreCloseReturnsAfterASlowUpdateSubscriber(t *testing.T) {
	bus := event.New()
	_ = bus.Subscribe(event.Topic("memory.update"))

	s, err := Open(Deps{Root: t.TempDir(), Bus: bus})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// A short budget: whether the publishes stall is the other test's
	// assertion, and this one only needs the listener to have plenty queued.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < 200; i++ {
		if err := bus.Publish(ctx, &event.StateChangeEvent{Task: "t_1", To: "ACT"}); err != nil {
			break
		}
	}

	_ = bus.Close()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Store.Close hung: the drain goroutine never exited")
	}
}
