package event

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// parkPublisherOn fills sub's 64-deep buffer and leaves one more publisher
// blocked on it forever. It returns a release func that drains one slot so the
// parked publisher can finish — callers must defer it before deferring
// bus.Close, or Close waits on a fan-out that never ends.
func parkPublisherOn(t *testing.T, bus *Bus, sub <-chan Envelope, topic string) (release func()) {
	t.Helper()
	for i := 0; i < subscriberBuf; i++ {
		if err := bus.Publish(context.Background(), pingAt(topic)); err != nil {
			t.Fatalf("fill publish %d: %v", i, err)
		}
	}
	parked := make(chan error, 1)
	go func() { parked <- bus.Publish(context.Background(), pingAt(topic)) }()
	// Let the parked publisher actually reach the blocking send. There is no
	// observable edge to wait on, and a too-short sleep only makes the test
	// weaker (less blocked), never flaky-failing.
	time.Sleep(50 * time.Millisecond)
	return func() {
		<-sub
		<-parked
	}
}

// TestStalledSubscriberDoesNotBlockUnrelatedPublish is the 4.8a driver. Holding
// fanoutMu across the whole fan-out means the one publisher parked on a stalled
// subscriber owns the bus: every other publisher in the process, on every other
// topic, queues behind it. Delivery must only block publishers that actually
// target the stalled subscriber.
func TestStalledSubscriberDoesNotBlockUnrelatedPublish(t *testing.T) {
	bus := New()
	defer bus.Close()

	stalled := bus.Subscribe("stall.>")
	other := bus.Subscribe("other.>")

	release := parkPublisherOn(t, bus, stalled, "stall.x")
	defer release()

	done := make(chan error, 1)
	go func() { done <- bus.Publish(context.Background(), pingAt("other.y")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unrelated publish: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("one stalled subscriber blocked a publish on an unrelated topic; fan-out still holds a process-wide lock")
	}
	if _, ok := recv(t, other); !ok {
		t.Error("unrelated subscriber received nothing")
	}
}

// TestSubscriberCanPublishWhileDraining is the self-deadlock half of 4.8a: a
// subscriber whose handler publishes (the runtime and the memory listener both
// do) must not wedge because some other publisher is parked mid-fan-out.
func TestSubscriberCanPublishWhileDraining(t *testing.T) {
	bus := New()
	defer bus.Close()

	stalled := bus.Subscribe("stall.>")
	work := bus.Subscribe("work.>")
	echo := bus.Subscribe("echo.>")

	release := parkPublisherOn(t, bus, stalled, "stall.x")
	defer release()

	// A subscriber that republishes as it drains.
	go func() {
		for range work {
			_ = bus.Publish(context.Background(), pingAt("echo.y"))
		}
	}()

	pub := make(chan error, 1)
	go func() { pub <- bus.Publish(context.Background(), pingAt("work.z")) }()
	select {
	case err := <-pub:
		if err != nil {
			t.Fatalf("publish work.z: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publish to a draining subscriber blocked behind a parked fan-out")
	}
	if _, ok := recvIn(t, echo, 2*time.Second); !ok {
		t.Fatal("subscriber that publishes while draining made no progress (self-deadlock)")
	}
}

// TestFanoutFIFOSurvivesTheStalledSubscriber guards the property the old
// process-wide lock was buying: per-subscriber FIFO. Releasing the lock before
// delivery must not let two publishers interleave their sends to one
// subscriber, so this runs the concurrent-publisher check while a *different*
// subscriber is stalled.
func TestFanoutFIFOSurvivesTheStalledSubscriber(t *testing.T) {
	bus := New()
	defer bus.Close()

	stalled := bus.Subscribe("stall.>")
	ch := bus.Subscribe("test.>")

	release := parkPublisherOn(t, bus, stalled, "stall.x")
	defer release()

	const pubs, perPub = 8, 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(pubs)
	for p := 0; p < pubs; p++ {
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perPub; i++ {
				if err := bus.Publish(context.Background(), ping("t")); err != nil {
					t.Errorf("publish: %v", err)
					return
				}
			}
		}()
	}
	close(start)

	var last uint64
	for i := 0; i < pubs*perPub; i++ {
		env, ok := recvIn(t, ch, 2*time.Second)
		if !ok {
			t.Fatalf("event %d/%d: timed out", i, pubs*perPub)
		}
		if env.Seq <= last {
			t.Fatalf("FIFO violated at index %d: Seq %d after Seq %d", i, env.Seq, last)
		}
		last = env.Seq
	}
	wg.Wait()
}

// --- 4.8b: an undelivered event must be observable ---

// TestUndeliveredEventIsCounted is the 4.8b driver. sendSub used to wrap the
// delivery in a recover() that set err = nil, so an event that lost its channel
// vanished with no signal anywhere. Unsubscribing a subscriber that a publisher
// is parked on is the reproducible version of that race; the drop must show up
// in Stats.
func TestUndeliveredEventIsCounted(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.subscribe([]Topic{"test.>"}, 0) // unbuffered: the send parks
	pub := make(chan error, 1)
	go func() { pub <- bus.Publish(context.Background(), ping("t")) }()
	time.Sleep(50 * time.Millisecond) // let the publisher reach the send

	bus.Unsubscribe(ch)

	select {
	case err := <-pub:
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Unsubscribe did not release the parked fan-out")
	}
	if got := bus.Stats().Dropped; got != 1 {
		t.Errorf("Stats().Dropped = %d, want 1 (an undelivered event must be counted, not swallowed)", got)
	}
}

func TestStatsCountsCompletedPublishes(t *testing.T) {
	bus := New()
	defer bus.Close()

	bus.Subscribe("test.>")
	for i := 0; i < 3; i++ {
		if err := bus.Publish(context.Background(), ping("t")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	got := bus.Stats()
	if got.Published != 3 {
		t.Errorf("Stats().Published = %d, want 3", got.Published)
	}
	if got.Dropped != 0 {
		t.Errorf("Stats().Dropped = %d, want 0", got.Dropped)
	}
}

// --- 4.8c: Unsubscribe ---

func TestUnsubscribeStopsDeliveryAndClosesChannel(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")
	if err := bus.Publish(context.Background(), ping("t")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, ok := recv(t, ch); !ok {
		t.Fatal("subscriber received nothing before Unsubscribe")
	}

	bus.Unsubscribe(ch)

	// The channel must be closed so a `for range ch` consumer terminates.
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed by Unsubscribe; a ranging consumer would leak")
	}
	// And the detached subscriber must no longer cost the fan-out anything.
	if err := bus.Publish(context.Background(), ping("t")); err != nil {
		t.Fatalf("publish after unsubscribe: %v", err)
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	bus := New()
	defer bus.Close()

	ch := bus.Subscribe("test.>")
	bus.Unsubscribe(ch)
	bus.Unsubscribe(ch) // must not panic (double close of the channel)

	// A channel the bus never issued is also a no-op, not a panic.
	bus.Unsubscribe(make(chan Envelope))
}

func TestUnsubscribeAfterCloseIsSafe(t *testing.T) {
	bus := New()
	ch := bus.Subscribe("test.>")
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	bus.Unsubscribe(ch) // Close already closed the channel; must not double-close
}

// TestUnsubscribeConcurrentWithPublish is the -race driver for 4.8c: subscribers
// churning while publishers fan out must never produce a send on a closed
// channel, a double close, or a data race.
func TestUnsubscribeConcurrentWithPublish(t *testing.T) {
	bus := New()
	defer bus.Close()

	// A steady subscriber that always drains, so publishers keep moving.
	steady := bus.Subscribe("test.>")
	go func() {
		for range steady {
		}
	}()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := bus.Publish(context.Background(), ping("t")); err != nil {
				return
			}
		}
	}()

	// Transient subscribers: subscribe, drain briefly, detach.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				ch := bus.Subscribe("test.>")
				go func() {
					for range ch {
					}
				}()
				time.Sleep(time.Millisecond)
				bus.Unsubscribe(ch)
				bus.Unsubscribe(ch) // double detach under contention
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestUnsubscribeDoesNotLeakSubscriptions proves the detach actually removes the
// subscription rather than leaving a dead channel in the fan-out list.
func TestUnsubscribeDoesNotLeakSubscriptions(t *testing.T) {
	bus := New()
	defer bus.Close()

	for i := 0; i < 100; i++ {
		bus.Unsubscribe(bus.Subscribe("test.>"))
	}
	bus.subsMu.Lock()
	n := len(bus.subs)
	bus.subsMu.Unlock()
	if n != 0 {
		t.Errorf("bus retains %d subscriptions after 100 subscribe/unsubscribe cycles, want 0", n)
	}
}

// TestReentrantPublishBlocksOnAFullBuffer pins the *real* reentrancy contract,
// which the header used to overstate. Dropping the process-wide fanoutMu fixed
// the case TestSubscriberCanPublishWhileDraining covers (a draining subscriber
// wedged behind an unrelated stalled one). It did not — and could not, without
// changing the delivery semantics — make a reentrant publish immune to its own
// backpressure: sendSub still parks on `s.ch <- env`. Here the draining
// goroutine is the only thing that could relieve the buffer it is publishing
// into, so the publish returns only when its context expires.
//
// This test documents rather than fixes. The fix, for a caller that cannot
// block, is TryPublish (next test).
func TestReentrantPublishBlocksOnAFullBuffer(t *testing.T) {
	bus := New()
	defer bus.Close()

	loop := bus.Subscribe("loop.>")

	// Fill the subscriber's own buffer before it drains anything.
	for i := 0; i < subscriberBuf; i++ {
		if err := bus.Publish(context.Background(), pingAt("loop.fill")); err != nil {
			t.Fatalf("fill publish %d: %v", i, err)
		}
	}

	// Drain exactly one slot, then publish back into the same topic from the
	// drain goroutine. The republish refills the slot we just freed; the next
	// one has nowhere to go, and this goroutine is not draining while it waits.
	result := make(chan error, 1)
	go func() {
		<-loop
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = bus.Publish(ctx, pingAt("loop.echo")) // refills the freed slot
		result <- bus.Publish(ctx, pingAt("loop.echo"))
	}()

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("reentrant publish into a full buffer = %v, want context.DeadlineExceeded "+
				"(if this now succeeds, the bus grew an overflow policy and the header comment "+
				"needs to say so)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reentrant publish neither blocked-and-timed-out nor returned")
	}

	// Drain what is left so Close does not wait on a parked fan-out.
	go func() {
		for range loop {
		}
	}()
}

// TestTryPublishDropsInsteadOfBlocking is the escape hatch for the shape above:
// a publish that must not block. It drops the delivery, reports it, and counts
// it in Stats().Dropped rather than losing it silently.
func TestTryPublishDropsInsteadOfBlocking(t *testing.T) {
	bus := New()
	defer bus.Close()

	sub := bus.Subscribe("full.>")
	for i := 0; i < subscriberBuf; i++ {
		if err := bus.Publish(context.Background(), pingAt("full.fill")); err != nil {
			t.Fatalf("fill publish %d: %v", i, err)
		}
	}
	before := bus.Stats().Dropped

	done := make(chan int, 1)
	go func() {
		dropped, err := bus.TryPublish(pingAt("full.over"))
		if err != nil {
			t.Errorf("TryPublish = %v, want nil", err)
		}
		done <- dropped
	}()

	select {
	case dropped := <-done:
		if dropped != 1 {
			t.Errorf("TryPublish dropped = %d, want 1", dropped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TryPublish blocked on a full buffer; the whole point is that it cannot")
	}
	if got := bus.Stats().Dropped; got != before+1 {
		t.Errorf("Stats().Dropped = %d, want %d (the drop must be visible, not silent)", got, before+1)
	}

	// Non-blocking is not the same as lossy-when-there-is-room: a subscriber
	// with a free slot still receives.
	<-sub
	if dropped, err := bus.TryPublish(pingAt("full.again")); err != nil || dropped != 0 {
		t.Errorf("TryPublish with a free slot = (%d, %v), want (0, nil)", dropped, err)
	}

	go func() {
		for range sub {
		}
	}()
}

// TestTryPublishFromDrainingSubscriberDoesNotWedge is the reachable shape the
// header now names: a store that reacts to an event by publishing another one,
// synchronously, from inside its drain loop, with no deadline. With Publish
// that hangs forever once the buffer fills (and a Close that waits on the drain
// goroutine hangs with it). With TryPublish the loop keeps running and the
// losses are counted.
func TestTryPublishFromDrainingSubscriberDoesNotWedge(t *testing.T) {
	bus := New()
	defer bus.Close()

	loop := bus.Subscribe("loop.>")
	for i := 0; i < subscriberBuf; i++ {
		if err := bus.Publish(context.Background(), pingAt("loop.fill")); err != nil {
			t.Fatalf("fill publish %d: %v", i, err)
		}
	}

	drained := make(chan int, 1)
	go func() {
		n := 0
		for range loop {
			n++
			// The memory.publishUpdate shape: reentrant, synchronous, no
			// deadline. TryPublish makes it survivable.
			_, _ = bus.TryPublish(pingAt("loop.echo"))
			if n == subscriberBuf {
				break
			}
		}
		drained <- n
	}()

	select {
	case n := <-drained:
		if n != subscriberBuf {
			t.Errorf("drained %d events, want %d", n, subscriberBuf)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain loop wedged on its own reentrant publish")
	}

	go func() {
		for range loop {
		}
	}()
}

// TestTryPublishOnClosedBusReturnsError keeps TryPublish's close behaviour the
// same as Publish's: after Close it is an error, not a silent drop.
func TestTryPublishOnClosedBusReturnsError(t *testing.T) {
	bus := New()
	_ = bus.Close()
	if _, err := bus.TryPublish(ping("t_1")); !errors.Is(err, ErrBusClosed) {
		t.Errorf("TryPublish after Close = %v, want ErrBusClosed", err)
	}
}

// TestTryPublishPreservesPerSubscriberFIFO guards the property the drop policy
// must not break: TryPublish takes its turn on the subscriber's gate like any
// other publisher, so it can never overtake a lower-Seq delivery — it drops
// instead. Here a parked publisher owns the turn, so the TryPublish behind it
// must be a drop, not a delivery out of order.
func TestTryPublishPreservesPerSubscriberFIFO(t *testing.T) {
	bus := New()
	defer bus.Close()

	stalled := bus.Subscribe("stall.>")
	release := parkPublisherOn(t, bus, stalled, "stall.x")
	defer release()

	dropped, err := bus.TryPublish(pingAt("stall.y"))
	if err != nil {
		t.Fatalf("TryPublish = %v, want nil", err)
	}
	if dropped != 1 {
		t.Errorf("TryPublish behind a parked publisher dropped = %d, want 1 "+
			"(delivering here would put stall.y ahead of the parked lower-Seq event)", dropped)
	}
}
