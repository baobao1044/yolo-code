package event

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeSubscriber counts how many times it "applies" an event — the effect we
// want idempotency to make safe. If the same Seq is redelivered, the counter
// must not advance.
type fakeSubscriber struct {
	seen  *Dedup
	count atomic.Int64
}

func newFakeSubscriber() *fakeSubscriber { return &fakeSubscriber{seen: NewDedup()} }

// apply returns whether the event was actually applied (true = first time,
// false = duplicate dropped). This mirrors the runtime's per-task `seen` set
// (File 05 §5.6.1).
func (s *fakeSubscriber) apply(env Envelope) bool {
	if !s.seen.Mark(env.Seq) {
		return false // already seen
	}
	s.count.Add(1)
	return true
}

func TestDedupDropsRedeliveredSeq(t *testing.T) {
	sub := newFakeSubscriber()
	env := Envelope{Seq: 7}

	if !sub.apply(env) {
		t.Error("first apply of Seq 7 was dropped; should be applied")
	}
	if sub.apply(env) {
		t.Error("redelivery of Seq 7 was applied; should be dropped")
	}
	if got := sub.count.Load(); got != 1 {
		t.Errorf("effect count = %d, want 1 (redelivery must not double-apply)", got)
	}
}

func TestDedupAllowsDistinctSeqs(t *testing.T) {
	sub := newFakeSubscriber()
	for seq := uint64(1); seq <= 5; seq++ {
		if !sub.apply(Envelope{Seq: seq}) {
			t.Errorf("distinct Seq %d was dropped", seq)
		}
	}
	if got := sub.count.Load(); got != 5 {
		t.Errorf("effect count = %d, want 5", got)
	}
}

// TestDedupConcurrentRedeliveryIsSafe proves Mark is race-free under
// concurrent redelivery of the same Seq: exactly one application wins.
func TestDedupConcurrentRedeliveryIsSafe(t *testing.T) {
	sub := newFakeSubscriber()
	env := Envelope{Seq: 42}

	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			sub.apply(env) // all workers redeliver the SAME seq
		}()
	}
	wg.Wait()

	if got := sub.count.Load(); got != 1 {
		t.Errorf("effect count = %d, want 1 (concurrent redelivery must apply exactly once)", got)
	}
}

// TestDedupDropsRedeliveredSeqAgainstLiveBus proves the round-trip: an envelope
// is delivered to a real bus subscriber, applied once, and re-applying the
// SAME envelope (not a fresh publish) is a no-op. This models at-least-once
// restart redelivery — the bus hands the same Seq to the subscriber twice, and
// the subscriber's effect count stays at 1.
func TestDedupDropsRedeliveredSeqAgainstLiveBus(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")
	sub := newFakeSubscriber()

	// Publish one event and read it from the bus.
	if err := bus.Publish(context.Background(), ping("t")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, ok := recv(t, ch)
	if !ok {
		t.Fatal("bus did not deliver the published event")
	}

	// First application applies the effect.
	if !sub.apply(got) {
		t.Fatal("first application of the delivered Seq was dropped")
	}
	// Redelivering the SAME envelope (same Seq) must NOT re-apply.
	if sub.apply(got) {
		t.Fatal("redelivery of the same Seq was applied; subscriber is not idempotent")
	}
	if c := sub.count.Load(); c != 1 {
		t.Errorf("effect count = %d, want 1 after one delivery + one redelivery", c)
	}
}
