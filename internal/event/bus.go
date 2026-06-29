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
	fanoutMu sync.Mutex // serializes stamp + durability + fan-out → FIFO
	closed   atomic.Bool
	closeCh  chan struct{} // closed by Close to unblock stalled fan-outs
	log      appender      // nil for New(); set by Open() for durability
}

type subscription struct {
	topics []Topic
	ch     chan Envelope
}

// New returns a ready-to-use, in-memory Bus with no durability log. Use Open
// when events must survive a crash.
func New() *Bus { return &Bus{closeCh: make(chan struct{})} }

// Open returns a Bus whose events are fsynced to an append-only log at path
// before any subscriber sees them (durability before visibility, File 05
// §5.3). The log is closed by Close.
func Open(path string) (*Bus, error) {
	l, err := OpenLog(path)
	if err != nil {
		return nil, err
	}
	return &Bus{closeCh: make(chan struct{}), log: l}, nil
}

// newBusWithAppender is a test seam for injecting a durability sink.
func newBusWithAppender(a appender) *Bus {
	return &Bus{closeCh: make(chan struct{}), log: a}
}

// Subscribe registers a new subscriber for the given topics and returns its
// receive channel. A topic of the form "prefix.>" matches any topic that
// starts with "prefix.". Repeated calls each yield an independent subscriber.
func (b *Bus) Subscribe(topics ...Topic) <-chan Envelope {
	return b.subscribe(topics, subscriberBuf)
}

// subscribe is the test-visible core: lets a caller pick the channel buffer
// size (e.g. 0 to force synchronous delivery in ordering tests).
func (b *Bus) subscribe(topics []Topic, buf int) <-chan Envelope {
	ch := make(chan Envelope, buf)
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

	// Durability before visibility (P3): fsync the envelope before any
	// subscriber can see it. A failure here means the event never fans out.
	if b.log != nil {
		if err := b.log.Append(env); err != nil {
			return err
		}
	}

	b.subsMu.Lock()
	subs := append([]subscription(nil), b.subs...)
	b.subsMu.Unlock()

	for _, s := range subs {
		if !matches(s.topics, e.Type()) {
			continue
		}
		sent, err := b.sendSub(s, env, ctx)
		if err != nil {
			return err
		}
		if !sent {
			// Channel was closed (subscriber removed); skip.
		}
	}
	return nil
}

// sendSub sends env to a single subscriber channel, recovering from a
// send-on-closed-channel panic if Close races past the closeCh guard. Returns
// (true, nil) on success, (false, nil) if the channel was closed, and
// (false, err) if the context or bus was canceled.
func (b *Bus) sendSub(s subscription, env Envelope, ctx context.Context) (sent bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			sent = false
			err = nil
		}
	}()
	select {
	case s.ch <- env:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-b.closeCh:
		return false, ErrBusClosed
	}
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

	// Release the durability log after fan-out is quiesced so any in-flight
	// Append completes before the file handle goes away.
	if b.log != nil {
		_ = b.log.Close()
	}
	return nil
}

// matches reports whether topic t is covered by any of the subscription
// patterns. A bare ">" is the root wildcard and matches every topic (File 05
// §5.2); an exact pattern matches itself; a "prefix.>" pattern matches any
// topic beginning with "prefix.".
func matches(patterns []Topic, t Topic) bool {
	for _, w := range patterns {
		if w == ">" || w == t {
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
