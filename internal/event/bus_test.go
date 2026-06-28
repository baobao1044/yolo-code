package event

import (
	"context"
	"testing"
	"time"
)

// testEvent is a minimal Event used across bus tests. It lives in the test
// package because the real event catalog (L3-006) defines concrete types; the
// bus itself must be agnostic to them.
type testEvent struct {
	task TaskID
	typ  Topic
}

func (e testEvent) Type() Topic      { return e.typ }
func (e testEvent) CausalID() TaskID { return e.task }

func ping(task string) testEvent      { return testEvent{task: TaskID(task), typ: "test.ping"} }
func pingAt(typ string) testEvent     { return testEvent{task: "t", typ: Topic(typ)} }
func typed(task, typ string) testEvent { return testEvent{task: TaskID(task), typ: Topic(typ)} }

// recv reads one envelope with a short timeout. ok=false means nothing
// arrived, which is how "no delivery" assertions are expressed without flaky
// sleeps.
func recv(t *testing.T, ch <-chan Envelope) (Envelope, bool) {
	t.Helper()
	select {
	case env, ok := <-ch:
		return env, ok
	case <-time.After(200 * time.Millisecond):
		return Envelope{}, false
	}
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
