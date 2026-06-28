// The Checkpointer is the snapshot primitive the Session Manager uses for undo
// and rollback (File 03 §3.3/§3.4). The real implementation lives in the Patch
// Engine (File 10) and uses git; Sprint 1 injects an in-memory stub so the
// undo/checkpoint machinery can be built and tested before git wiring exists.

package session

import (
	"context"
	"sync/atomic"
)

// Checkpointer takes a restorable snapshot of a set of paths and can roll back
// to one. Snapshot returns an opaque ref; Rollback restores the tree to that
// ref. The Patch Engine (File 10) provides the git-backed implementation.
type Checkpointer interface {
	Snapshot(ctx context.Context, paths []string) (SnapshotRef, error)
	Rollback(ctx context.Context, ref SnapshotRef) error
}

// InMemCheckpointer is the Sprint 1 stub: it mints monotonic refs and records
// rollbacks so tests can assert the Manager called Rollback with the right ref.
// It touches no files.
type InMemCheckpointer struct {
	next      atomic.Uint64
	rollbacks []SnapshotRef
}

// NewInMemCheckpointer returns a fresh in-memory checkpointer.
func NewInMemCheckpointer() *InMemCheckpointer { return &InMemCheckpointer{} }

// Snapshot mints an opaque ref like "snap-1". The paths are accepted but not
// stored (no real files to snapshot in Sprint 1).
func (c *InMemCheckpointer) Snapshot(_ context.Context, _ []string) (SnapshotRef, error) {
	n := c.next.Add(1)
	return SnapshotRef("snap-" + itoa(n)), nil
}

// Rollback records that ref was rolled back. Always succeeds in Sprint 1.
func (c *InMemCheckpointer) Rollback(_ context.Context, ref SnapshotRef) error {
	c.rollbacks = append(c.rollbacks, ref)
	return nil
}

// Rollbacks returns the refs Rollback was called with, in order. Test-only.
func (c *InMemCheckpointer) Rollbacks() []SnapshotRef {
	return append([]SnapshotRef(nil), c.rollbacks...)
}

// itoa avoids importing strconv for a one-line uint→string.
func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
