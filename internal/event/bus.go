// Package event — Bus, Envelope, and topic routing (L3-002).
//
// This is the first slice of Layer 3 (File 05): a small typed bus where
// publishers call Publish and subscribers receive Envelopes on channels keyed
// by dotted Topic strings with a `prefix.>` wildcard.
//
// Delivery semantics here are intentionally minimal — fan-out happens inline
// in Publish. The per-subscriber FIFO guarantee under concurrent publishers
// (File 05 §5.2) is added in L3-003 via a single dispatch goroutine; that
// ticket's test is what forces the refactor, so the gap is caught by a test,
// not assumed.

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

// ErrBusClosed is returned by Publish after Close has been called.
var ErrBusClosed = errors.New("event: bus closed")

// subscriberBuf is the bounded channel size for every subscriber (File 05
// §5.6). A subscriber that falls behind fills this buffer; the next Publish
// then blocks (backpressure) rather than dropping.
const subscriberBuf = 64

// Bus is the system backbone. One instance per session.
type Bus struct {
	next   atomic.Uint64 // monotonic Seq source
	mu     sync.Mutex    // guards subs
	subs   []subscription
	closed atomic.Bool
}

type subscription struct {
	topics []Topic
	ch     chan Envelope
}

// New returns a ready-to-use Bus.
func New() *Bus { return &Bus{} }

// Subscribe registers a new subscriber for the given topics and returns its
// receive channel. A topic of the form "prefix.>" matches any topic that
// starts with "prefix.". Repeated calls each yield an independent subscriber.
func (b *Bus) Subscribe(topics ...Topic) <-chan Envelope {
	ch := make(chan Envelope, subscriberBuf)
	b.mu.Lock()
	b.subs = append(b.subs, subscription{topics: topics, ch: ch})
	b.mu.Unlock()
	return ch
}

// Publish stamps an envelope with the next Seq, then fans it out to every
// matching subscriber. If the bus is closed it returns ErrBusClosed without
// stamping. Fan-out blocks on full subscriber channels until they drain or
// ctx is canceled (backpressure, File 05 §5.6).
func (b *Bus) Publish(ctx context.Context, e Event) error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	seq := b.next.Add(1)
	env := Envelope{Seq: seq, At: time.Now().UTC(), Evt: e}

	b.mu.Lock()
	subs := append([]subscription(nil), b.subs...)
	b.mu.Unlock()

	for _, s := range subs {
		if !matches(s.topics, e.Type()) {
			continue
		}
		select {
		case s.ch <- env:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Close marks the bus closed and closes every subscriber channel. It is
// idempotent.
func (b *Bus) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	b.mu.Lock()
	for _, s := range b.subs {
		close(s.ch)
	}
	b.subs = nil
	b.mu.Unlock()
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
