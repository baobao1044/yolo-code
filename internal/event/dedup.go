// Package event — subscriber-side idempotency (L3-005).
//
// Delivery is at-least-once across restart (File 05 §5.2/§5.6.1), so every
// subscriber that applies an effect must dedup on the bus-assigned Seq. Dedup
// is the per-subscriber (or per-task) "seen" set the spec describes: a
// subscriber wraps its receive loop with Mark(seq) and drops any envelope
// whose Seq it has already applied.
//
// This lives in the event package (not each subscriber) so the dedup policy is
// defined once and every layer uses the same one.

package event

import "sync"

// Dedup is a per-subscriber set of already-applied Seqs. It is safe for
// concurrent use because a subscriber may redeliver from multiple goroutines
// (e.g. a replay worker and the live stream).
type Dedup struct {
	mu   sync.Mutex
	seen map[uint64]struct{}
}

// NewDedup returns an empty Dedup.
func NewDedup() *Dedup { return &Dedup{seen: map[uint64]struct{}{}} }

// Mark reports whether seq is seen for the first time. It returns true (and
// records seq) on the first call for a given seq, and false on every
// subsequent call with the same seq. A subscriber applies an effect only when
// Mark returns true.
func (d *Dedup) Mark(seq uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[seq]; ok {
		return false
	}
	d.seen[seq] = struct{}{}
	return true
}
