// Package event — Bus, Envelope, and topic routing (L3-002, L3-003).
//
// Delivery semantics (File 05 §5.2/§5.6):
//   - per-subscriber FIFO: stamping and *ticket reservation* happen under one
//     mutex, so each subscriber's delivery slots are handed out in Seq order.
//     The delivery itself runs with no process-wide lock held: a publisher
//     waits for its turn on the target subscriber's own gate. Concurrent
//     publishers therefore still cannot interleave sends to one subscriber,
//     but a subscriber that stalls only blocks the publishers that target it —
//     not, as before, every publisher in the process.
//   - at-least-once, bounded backpressure: each subscriber channel is buffered
//     to 64; a full channel blocks the fan-out (and therefore the publisher)
//     until the subscriber drains or the publisher's context is canceled.
//     This is the whole contract, and it has no reentrancy exemption. A
//     subscriber that publishes while draining no longer wedges behind an
//     *unrelated* stalled subscriber (that is what dropping the process-wide
//     lock bought), but it is not immune to its own backpressure: if the
//     publish targets a subscriber whose 64 slots are full — its own channel,
//     or a chain that leads back to it — it blocks, and if the goroutine that
//     would drain those slots is the one publishing, nothing will drain them.
//     Such a publish returns only when its context is canceled, the subscriber
//     detaches, or the bus closes; with context.Background() it never returns.
//     TestReentrantPublishBlocksOnAFullBuffer pins exactly that. A caller that
//     cannot afford to block (a drain-loop reaction, a shutdown path) must use
//     TryPublish, which drops instead and counts the drop in Stats().Dropped.
//   - close safety: a channel is closed only after its gate is shut and the one
//     send that may be in flight has returned, so a send-on-closed channel is
//     structurally impossible rather than recovered from. Close signals closeCh
//     and Unsubscribe signals the subscription's done channel to release a
//     fan-out parked on a full buffer.
//   - undelivered events are counted, never silent: a delivery skipped because
//     its subscriber detached mid-fan-out — or because a TryPublish found the
//     buffer full — increments Stats().Dropped.
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
	next      atomic.Uint64 // monotonic Seq source
	subsMu    sync.Mutex    // guards the subs slice (Subscribe / Unsubscribe / Close)
	subs      []*subscription
	fanoutMu  sync.Mutex // serializes stamp + durability + ticket reservation → FIFO
	closed    atomic.Bool
	closeCh   chan struct{} // closed by Close to unblock stalled fan-outs
	log       appender      // nil for New(); set by Open() for durability
	published atomic.Uint64 // publishes that completed their fan-out
	dropped   atomic.Uint64 // deliveries skipped: subscriber detached, or TryPublish overflow
}

type subscription struct {
	topics []Topic
	ch     chan Envelope
	done   chan struct{} // closed by Unsubscribe to release a parked fan-out
	gate   *subGate
}

// Stats is a snapshot of the bus counters. It is additive: fields may be
// appended, never removed.
type Stats struct {
	Published uint64 // publishes that completed the full fan-out
	Dropped   uint64 // deliveries that never reached their subscriber
}

// Stats returns a snapshot of the bus counters. Dropped is the signal the old
// recover()-and-nil-the-error path threw away: an event that could not be
// handed to a subscriber is accounted for here instead of vanishing.
func (b *Bus) Stats() Stats {
	return Stats{Published: b.published.Load(), Dropped: b.dropped.Load()}
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
	s := &subscription{
		topics: topics,
		ch:     make(chan Envelope, buf),
		done:   make(chan struct{}),
		gate:   newSubGate(),
	}
	b.subsMu.Lock()
	b.subs = append(b.subs, s)
	b.subsMu.Unlock()
	return s.ch
}

// Unsubscribe detaches the subscriber that Subscribe returned ch for and closes
// ch, so a `for range ch` consumer terminates and the fan-out stops paying for
// a channel nobody reads. It is safe to call concurrently with Publish, twice
// on the same channel, after Close, or with a channel this bus never issued —
// all of those are no-ops rather than panics.
func (b *Bus) Unsubscribe(ch <-chan Envelope) {
	b.subsMu.Lock()
	var found *subscription
	for i, s := range b.subs {
		if (<-chan Envelope)(s.ch) != ch {
			continue
		}
		found = s
		copy(b.subs[i:], b.subs[i+1:])
		b.subs[len(b.subs)-1] = nil // drop the backing-array reference too
		b.subs = b.subs[:len(b.subs)-1]
		break
	}
	b.subsMu.Unlock()
	if found == nil {
		return
	}

	// Release a fan-out parked on this channel *before* waiting for it, then
	// let the gate quiesce so the close below cannot race a send. subsMu is not
	// held here: the parked publisher must be able to finish without it.
	close(found.done)
	if found.gate.shutdown() {
		close(found.ch)
	}
}

// Publish stamps an envelope with the next Seq, then fans it out to every
// matching subscriber. Stamping and ticket reservation happen under fanoutMu,
// so each subscriber's slots are ordered by Seq (per-subscriber FIFO); the
// delivery itself runs with the lock released, so one stalled subscriber no
// longer freezes every publisher in the process. Fan-out blocks on full
// subscriber channels until they drain, the context is canceled, the
// subscriber detaches, or the bus is closed (backpressure, File 05 §5.6).
func (b *Bus) Publish(ctx context.Context, e Event) error {
	if b.closed.Load() {
		return ErrBusClosed
	}

	env, targets, tickets, err := b.stamp(e)
	if err != nil {
		return err
	}

	for i, s := range targets {
		if err := b.sendSub(s, tickets[i], env, ctx); err != nil {
			// Retire the slots we will never use. Without this every later
			// publisher queued behind them waits for a turn that never comes.
			for j := i + 1; j < len(targets); j++ {
				targets[j].gate.abandon(tickets[j])
			}
			return err
		}
	}
	b.published.Add(1)
	return nil
}

// TryPublish stamps and fans out like Publish but never blocks: a subscriber
// whose buffer is full (or whose turn has not come up, because an earlier
// publisher is parked on that full buffer) does not get this envelope. It is
// the escape hatch for the one shape Publish cannot serve — a publish issued
// from inside a subscriber's own drain loop, where blocking on backpressure
// means blocking the goroutine that would relieve it.
//
// The overflow policy is drop-newest, and it is not silent: every skipped
// delivery increments Stats().Dropped, the same counter a detached subscriber
// feeds. Stamping, durability, and per-subscriber FIFO are unchanged — a
// dropped delivery is a gap in one subscriber's stream, never a reordering, and
// the envelope is still in the durability log. It returns the number of
// subscribers that missed the event, so a caller that cares can log it. A
// TryPublish that dropped every delivery still completed its fan-out, so it
// counts toward Stats().Published: Published counts publishes that finished,
// Dropped counts the deliveries inside them that did not land.
//
// Publish remains the default: at-least-once delivery is the contract the
// spine relies on, and only a caller that can prove it must not block should
// trade it away.
func (b *Bus) TryPublish(e Event) (dropped int, err error) {
	if b.closed.Load() {
		return 0, ErrBusClosed
	}

	env, targets, tickets, err := b.stamp(e)
	if err != nil {
		return 0, err
	}

	for i, s := range targets {
		if !b.trySendSub(s, tickets[i], env) {
			dropped++
		}
	}
	b.published.Add(1)
	return dropped, nil
}

// trySendSub is sendSub's non-blocking twin: it takes its turn only if the turn
// is already available and the channel already has room, and otherwise retires
// the ticket so the publishers queued behind it are not stranded. It reports
// whether the envelope was delivered.
func (b *Bus) trySendSub(s *subscription, ticket uint64, env Envelope) bool {
	if !s.gate.tryEnter(ticket) {
		s.gate.abandon(ticket)
		b.dropped.Add(1)
		return false
	}
	defer s.gate.leave()

	select {
	case s.ch <- env:
		return true
	default:
		b.dropped.Add(1)
		return false
	}
}

// stamp assigns the next Seq, makes the envelope durable, and reserves a
// delivery slot on every matching subscriber — all under fanoutMu, which is the
// whole of the critical section. Nothing that can block on a subscriber runs
// here, so the lock is held for a bounded time.
func (b *Bus) stamp(e Event) (Envelope, []*subscription, []uint64, error) {
	b.fanoutMu.Lock()
	defer b.fanoutMu.Unlock()

	// Re-check under the lock: Close may have run between the load in Publish
	// and acquiring fanoutMu.
	if b.closed.Load() {
		return Envelope{}, nil, nil, ErrBusClosed
	}

	env := Envelope{Seq: b.next.Add(1), At: time.Now().UTC(), Evt: e}

	// Durability before visibility (P3): fsync the envelope before any
	// subscriber can see it. A failure here means the event never fans out.
	if b.log != nil {
		if err := b.log.Append(env); err != nil {
			return Envelope{}, nil, nil, err
		}
	}

	b.subsMu.Lock()
	defer b.subsMu.Unlock()
	var targets []*subscription
	var tickets []uint64
	for _, s := range b.subs {
		if !matches(s.topics, e.Type()) {
			continue
		}
		targets = append(targets, s)
		tickets = append(tickets, s.gate.reserve())
	}
	return env, targets, tickets, nil
}

// sendSub waits for ticket's turn on the subscriber's gate, then delivers env.
// The gate guarantees per-subscriber FIFO and that the channel cannot be closed
// underneath the send, so there is no panic to recover from — and therefore no
// error to swallow. A delivery skipped because the subscriber detached is
// counted in Stats().Dropped rather than disappearing.
func (b *Bus) sendSub(s *subscription, ticket uint64, env Envelope, ctx context.Context) error {
	if !s.gate.enter(ticket) {
		// Detached (or the bus closed) before this envelope's turn came up.
		if b.closed.Load() {
			return ErrBusClosed
		}
		b.dropped.Add(1)
		return nil
	}
	defer s.gate.leave()

	select {
	case s.ch <- env:
		return nil
	case <-s.done:
		b.dropped.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closeCh:
		return ErrBusClosed
	}
}

// subGate orders deliveries to one subscriber and owns its close handshake.
// Publish reserves a ticket under fanoutMu — so tickets follow Seq order — and
// then waits here for its turn. That preserves per-subscriber FIFO while the
// fan-out runs lock-free, and it means a channel is closed only once no sender
// is inside it.
type subGate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	next    uint64          // next ticket to hand out
	serving uint64          // ticket whose turn it is
	skipped map[uint64]bool // tickets abandoned before their turn arrived
	sending bool            // a sender is past the gate, maybe blocked on the channel
	closed  bool            // no further sends; the channel is closing
}

func newSubGate() *subGate {
	g := &subGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// reserve claims the next delivery slot. Called under fanoutMu, which is what
// makes the slot order the Seq order.
func (g *subGate) reserve() uint64 {
	g.mu.Lock()
	t := g.next
	g.next++
	g.mu.Unlock()
	return t
}

// enter blocks until it is ticket t's turn. It reports false if the
// subscription closed first, in which case the caller must not touch the
// channel and must not call leave. Waiting here is bounded by the predecessors,
// which are themselves bounded by their context, Unsubscribe, or Close.
func (g *subGate) enter(t uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for !g.closed && g.serving != t {
		g.cond.Wait()
	}
	if g.closed {
		return false
	}
	g.sending = true
	return true
}

// tryEnter is enter without the wait: it reports false rather than blocking
// when the gate is closed or the turn belongs to someone else. A caller that
// gets false must abandon the ticket (it will never be used), which is why
// there is no leave to pair with a false return.
func (g *subGate) tryEnter(t uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.serving != t {
		return false
	}
	g.sending = true
	return true
}

// leave releases the turn once a delivery attempt has finished.
func (g *subGate) leave() {
	g.mu.Lock()
	g.sending = false
	g.advanceLocked()
	g.mu.Unlock()
	g.cond.Broadcast()
}

// abandon retires a ticket whose publish gave up before reaching it, so the
// publishers queued behind it are not stranded.
func (g *subGate) abandon(t uint64) {
	g.mu.Lock()
	if g.serving == t {
		g.advanceLocked()
	} else {
		if g.skipped == nil {
			g.skipped = map[uint64]bool{}
		}
		g.skipped[t] = true
	}
	g.mu.Unlock()
	g.cond.Broadcast()
}

// advanceLocked moves to the next ticket, stepping over any that were abandoned
// out of order.
func (g *subGate) advanceLocked() {
	g.serving++
	for g.skipped[g.serving] {
		delete(g.skipped, g.serving)
		g.serving++
	}
}

// shutdown closes the gate, wakes everyone waiting for a turn, and waits for the
// one send that may be in flight, so the caller can close the channel without
// racing a sender. It reports false if the gate was already shut down — which is
// what makes a double Unsubscribe, or Unsubscribe after Close, a no-op instead
// of a double close. The caller must already have signalled done/closeCh, or the
// in-flight send has nothing to unblock it.
func (g *subGate) shutdown() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.closed = true
	g.cond.Broadcast()
	for g.sending {
		g.cond.Wait()
	}
	return true
}

// Close marks the bus closed and closes every subscriber channel. It is
// idempotent. Close first signals stalled fan-outs via closeCh (so a publish
// blocked on backpressure unblocks instead of deadlocking Close), then quiesces
// each subscriber's gate, which is what guarantees no publisher is mid-send
// when its channel closes. It deliberately does *not* hold fanoutMu across the
// quiesce: that would reintroduce the process-wide stall Close exists to break.
func (b *Bus) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(b.closeCh) // unblock any fan-out stalled on a full subscriber

	b.subsMu.Lock()
	subs := b.subs
	b.subs = nil
	b.subsMu.Unlock()

	for _, s := range subs {
		if s.gate.shutdown() {
			close(s.ch)
		}
	}

	// Release the durability log under fanoutMu, which is the only place Append
	// runs, so a close can never race an in-flight Append. Taking the lock here
	// is bounded: it is no longer held across a delivery.
	b.fanoutMu.Lock()
	if b.log != nil {
		_ = b.log.Close()
	}
	b.fanoutMu.Unlock()
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
