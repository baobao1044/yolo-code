package event

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// testEvent is a minimal Event used across bus tests. It lives in the test
// package because the real event catalog (L3-006) defines concrete types; the
// bus itself must be agnostic to them. It carries custom JSON so the durability
// log (L3-004) can round-trip it.
type testEvent struct {
	task TaskID
	typ  Topic
}

func (e testEvent) Type() Topic      { return e.typ }
func (e testEvent) CausalID() TaskID { return e.task }

// MarshalJSON/UnmarshalJSON let testEvent survive a durability round-trip
// (its fields are unexported, so encoding/json would otherwise drop them).
func (e testEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Task TaskID `json:"task"`
		Typ  Topic  `json:"typ"`
	}{Task: e.task, Typ: e.typ})
}

func (e *testEvent) UnmarshalJSON(data []byte) error {
	var v struct {
		Task TaskID `json:"task"`
		Typ  Topic  `json:"typ"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.task, e.typ = v.Task, v.Typ
	return nil
}

func ping(task string) testEvent      { return testEvent{task: TaskID(task), typ: "test.ping"} }
func pingAt(typ string) testEvent     { return testEvent{task: "t", typ: Topic(typ)} }
func typed(task, typ string) testEvent { return testEvent{task: TaskID(task), typ: Topic(typ)} }

// recvIn reads one envelope with a timeout of d. ok=false means nothing
// arrived, which is how "no delivery" assertions are expressed without flaky
// sleeps.
func recvIn(t *testing.T, ch <-chan Envelope, d time.Duration) (Envelope, bool) {
	t.Helper()
	select {
	case env, ok := <-ch:
		return env, ok
	case <-time.After(d):
		return Envelope{}, false
	}
}

// recv is the common "did anything arrive?" probe with a short default timeout.
func recv(t *testing.T, ch <-chan Envelope) (Envelope, bool) {
	return recvIn(t, ch, 200*time.Millisecond)
}

func TestPublishDeliversToMatchingSubscriber(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.ping")
	if err := bus.Publish(context.Background(), ping("t1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	env, ok := recv(t, ch)
	if !ok {
		t.Fatal("expected an envelope, got none")
	}
	if env.Seq != 1 {
		t.Errorf("Seq = %d, want 1 (first event in the session)", env.Seq)
	}
	if env.Evt.Type() != "test.ping" {
		t.Errorf("Evt.Type = %q, want %q", env.Evt.Type(), "test.ping")
	}
	if env.Evt.CausalID() != "t1" {
		t.Errorf("Evt.CausalID = %q, want %q", env.Evt.CausalID(), "t1")
	}
	if env.At.IsZero() {
		t.Error("At timestamp is zero; bus must stamp every envelope")
	}
}

func TestPublishDoesNotDeliverToNonMatchingSubscriber(t *testing.T) {
	bus := New()
	defer bus.Close()

	other := bus.Subscribe("other.topic")
	if err := bus.Publish(context.Background(), ping("t1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if _, ok := recv(t, other); ok {
		t.Fatal("non-matching subscriber received an event; topic routing leaked")
	}
}

func TestWildcardPrefixSubscription(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")
	topics := []Topic{"test.ping", "test.pong", "test.sub.deep"}
	for _, typ := range topics {
		if err := bus.Publish(context.Background(), pingAt(string(typ))); err != nil {
			t.Fatalf("publish %q: %v", typ, err)
		}
	}

	for i, want := range topics {
		env, ok := recv(t, ch)
		if !ok {
			t.Fatalf("event %d: expected %q, got none", i, want)
		}
		if env.Evt.Type() != want {
			t.Errorf("event %d: Type = %q, want %q", i, env.Evt.Type(), want)
		}
	}
}

func TestWildcardDoesNotMatchUnprefixedTopic(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")
	// "test" (no dot) and "other.ping" must not match the "test.>" prefix.
	for _, typ := range []string{"test", "other.ping", "tests"} {
		if err := bus.Publish(context.Background(), pingAt(typ)); err != nil {
			t.Fatalf("publish %q: %v", typ, err)
		}
	}
	if _, ok := recv(t, ch); ok {
		t.Fatal("wildcard matched an unprefixed topic; prefix boundary is wrong")
	}
}

func TestSeqIsMonotonicPerSession(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")
	for i := 1; i <= 3; i++ {
		if err := bus.Publish(context.Background(), ping("t")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	for want := uint64(1); want <= 3; want++ {
		env, ok := recv(t, ch)
		if !ok {
			t.Fatalf("expected Seq %d, got none", want)
		}
		if env.Seq != want {
			t.Errorf("Seq = %d, want %d", env.Seq, want)
		}
	}
}

func TestMultipleSubscribersAllReceiveMatchingEvent(t *testing.T) {
	bus := New()
	defer bus.Close()

	a := bus.Subscribe("test.ping")
	b := bus.Subscribe("test.>")

	if err := bus.Publish(context.Background(), typed("t", "test.ping")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	for _, name := range []string{"a", "b"} {
		var ch <-chan Envelope
		if name == "a" {
			ch = a
		} else {
			ch = b
		}
		if _, ok := recv(t, ch); !ok {
			t.Errorf("subscriber %q received nothing", name)
		}
	}
}

func TestPublishOnClosedBusReturnsError(t *testing.T) {
	bus := New()
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := bus.Publish(context.Background(), ping("t")); err != ErrBusClosed {
		t.Errorf("err = %v, want ErrBusClosed", err)
	}
}

func TestCloseClosesSubscriberChannels(t *testing.T) {
	bus := New()
	ch := bus.Subscribe("test.ping")
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, ok := <-ch; ok {
		t.Fatal("subscriber channel not closed after Close")
	}
}

func TestPublishContextCancelStopsFanOut(t *testing.T) {
	bus := New()
	defer bus.Close()

	// Subscriber that never drains — its buffer fills, then the next publish
	// blocks. A canceled context must abort that publish rather than deadlock.
	ch := bus.Subscribe("test.>")
	for i := 0; i < 64; i++ {
		_ = bus.Publish(context.Background(), ping("t")) // fill the buffer
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := bus.Publish(ctx, ping("t")); err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	_ = ch
}

// --- L3-003: per-subscriber FIFO, bounded channels, backpressure ---

func TestSubscriberChannelIsBoundedTo64(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")
	if cap(ch) != 64 {
		t.Errorf("subscriber channel cap = %d, want 64 (File 05 §5.6 bound)", cap(ch))
	}
}

// TestFanoutIsSerializedPerSubscriberFIFO is the L3-003 RED driver: under
// concurrent publishers, every subscriber must receive envelopes in strictly
// increasing Seq order. The inline fan-out (no serialization) lets two
// publishers' channel sends interleave, so this fails until stamp+fan-out are
// serialized onto one writer.
func TestFanoutIsSerializedPerSubscriberFIFO(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")

	const pubs, perPub = 8, 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(pubs)
	for p := 0; p < pubs; p++ {
		go func() {
			defer wg.Done()
			<-start // release all publishers at once to maximise interleaving
			for i := 0; i < perPub; i++ {
				if err := bus.Publish(context.Background(), ping("t")); err != nil {
					// ErrBusClosed is expected only if the test is tearing down
					// (e.g. a prior assertion failed and Close ran mid-flight).
					if err != ErrBusClosed {
						t.Errorf("publish: %v", err)
					}
					return
				}
			}
		}()
	}
	close(start)

	// Collect every envelope; assert strict monotonic increase of Seq.
	var last uint64
	for i := 0; i < pubs*perPub; i++ {
		env, ok := recvIn(t, ch, time.Second)
		if !ok {
			t.Fatalf("event %d/%d: timed out waiting for delivery", i, pubs*perPub)
		}
		if env.Seq <= last {
			t.Fatalf("FIFO violated at index %d: Seq %d after Seq %d (subscribers must receive in seq order)",
				i, env.Seq, last)
		}
		last = env.Seq
	}
	wg.Wait()
}

func TestSlowSubscriberBlocksPublisherNotDrops(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")

	// Fill the 64-deep buffer so the next publish must block.
	for i := 0; i < 64; i++ {
		if err := bus.Publish(context.Background(), ping("t")); err != nil {
			t.Fatalf("fill publish %d: %v", i, err)
		}
	}

	// The 65th publish should block (backpressure), not drop the event.
	done := make(chan error, 1)
	go func() { done <- bus.Publish(context.Background(), ping("t")) }()
	select {
	case err := <-done:
		t.Fatalf("publish did not block on full subscriber (err=%v); backpressure should stall, not drop", err)
	case <-time.After(50 * time.Millisecond):
		// good: still blocked
	}

	// Draining one slot must let the blocked publish complete.
	<-ch
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked publish completed with error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("blocked publish did not complete after draining one slot")
	}
}
