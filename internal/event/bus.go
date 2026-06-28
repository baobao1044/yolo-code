// Package event — Bus, Envelope, and topic routing (L3-002, L3-003).
//
// Delivery semantics (File 05 §5.2/§5.6):
//   - per-subscriber FIFO: stamp + fan-out happen under one mutex, so the
//     order in which subscribers receive envelopes is exactly the Seq order.
//     Concurrent publishers serialize on fanoutMu rather than interleaving.
//   - at-least-once, bounded backpressure: each subscriber channel is buffered
//     to 64; a full channel blocks the fan-out (and therefore the publisher)
//     until the subscriber drains or the publisher's context is canceled.
//   - close safety: Close unblocks any fan-out stalled on a full channel via
//     closeCh, then takes fanoutMu so no publisher is mid-send when channels
//     are closed — eliminating send-on-closed panics.
//
// The durability log (fsync-before-fan-out) is added in L3-004.

package event

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Topic is a dotted hierarchical event channel, e.g. "tool.call" or "tool.>"
// (wildcard prefix). See File 05 §5.4.9 for the registry.
type Topic string

// TaskID identifies the task an event belongs to; the causal glue the runtime
// and golden transcripts key on. A string alias for now; later layers may
// tighten it.
type TaskID string

// Event is anything the bus carries. Every event declares its own Topic and
// the task it belongs to.
type Event interface {
	Type() Topic
	CausalID() TaskID
}

// Envelope is the bus-assigned wrapper around an Event: a monotonic sequence
// number, a timestamp, and the event itself. Seq is per-session and is what
// idempotent subscribers dedup on (File 05 §5.6.1).
type Envelope struct {
	Seq uint64
	At  time.Time
	Evt Event
}

// ErrBusClosed is returned by Publish after Close has been called (or while a
// publish is unblocked by Close).
var ErrBusClosed = errors.New("event: bus closed")

// subscriberBuf is the bounded channel size for every subscriber (File 05
// §5.6). A subscriber that falls behind fills this buffer; the next fan-out
// then blocks (backpressure) rather than dropping.
const subscriberBuf = 64

// Bus is the system backbone. One instance per session.
type Bus struct {
	next     atomic.Uint64 // monotonic Seq source
	subsMu   sync.Mutex    // guards the subs slice (Subscribe / Close)
	subs     []subscription
	fanoutMu sync.Mutex // serializes stamp + fan-out → per-subscriber FIFO
	closed   atomic.Bool
	closeCh  chan struct{} // closed by Close to unblock stalled fan-outs
}

type subscription struct {
	topics []Topic
	ch     chan Envelope
}

// New returns a ready-to-use Bus.
func New() *Bus { return &Bus{closeCh: make(chan struct{})} }

// Subscribe registers a new subscriber for the given topics and returns its
// receive channel. A topic of the form "prefix.>" matches any topic that
// starts with "prefix.". Repeated calls each yield an independent subscriber.
func (b *Bus) Subscribe(topics ...Topic) <-chan Envelope {
	ch := make(chan Envelope, subscriberBuf)
	b.subsMu.Lock()
	b.subs = append(b.subs, subscription{topics: topics, ch: ch})
	b.subsMu.Unlock()
	return ch
}

// Publish stamps an envelope with the next Seq, then fans it out to every
// matching subscriber. Stamp + fan-out happen under fanoutMu, so concurrent
// publishers deliver in Seq order (per-subscriber FIFO). Fan-out blocks on
// full subscriber channels until they drain, the context is canceled, or the
// bus is closed (backpressure, File 05 §5.6).
func (b *Bus) Publish(ctx context.Context, e Event) error {
	if b.closed.Load() {
		return ErrBusClosed
	}

	b.fanoutMu.Lock()
	defer b.fanoutMu.Unlock()

	// Re-check under the lock: Close may have run between the load above and
	// acquiring fanoutMu.
	if b.closed.Load() {
		return ErrBusClosed
	}

	seq := b.next.Add(1)
	env := Envelope{Seq: seq, At: time.Now().UTC(), Evt: e}

	b.subsMu.Lock()
	subs := append([]subscription(nil), b.subs...)
	b.subsMu.Unlock()

	for _, s := range subs {
		if !matches(s.topics, e.Type()) {
			continue
		}
		select {
		case s.ch <- env:
		case <-ctx.Done():
			return ctx.Err()
		case <-b.closeCh:
			return ErrBusClosed
		}
	}
	return nil
}

// Close marks the bus closed and closes every subscriber channel. It is
// idempotent. Close first signals stalled fan-outs via closeCh (so a publish
// blocked on backpressure unblocks instead of deadlocking Close), then takes
// fanoutMu to guarantee no publisher is mid-send when channels close.
func (b *Bus) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(b.closeCh) // unblock any fan-out stalled on a full subscriber

	b.fanoutMu.Lock() // wait for in-flight fan-outs to finish
	b.subsMu.Lock()
	for _, s := range b.subs {
		close(s.ch)
	}
	b.subs = nil
	b.subsMu.Unlock()
	b.fanoutMu.Unlock()
	return nil
}

// matches reports whether topic t is covered by any of the subscription
// patterns. An exact pattern matches itself; a "prefix.>" pattern matches any
// topic beginning with "prefix.".
func matches(patterns []Topic, t Topic) bool {
	for _, w := range patterns {
		if w == t {
			return true
		}
		if strings.HasSuffix(string(w), ".>") {
			prefix := strings.TrimSuffix(string(w), ">")
			if strings.HasPrefix(string(t), prefix) {
				return true
			}
		}
	}
	return false
}
