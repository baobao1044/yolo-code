//go:build !windows

package memory

import "testing"

// TestShareRenamerAddsNothingOffWindows pins the Unix half of the platform
// contract: the retry policy must be inert here. If renameRetries ever became
// non-zero on a platform where nothing can block a rename, every genuine
// writeJSON failure — a full disk, a missing directory — would start costing a
// wait for a condition that cannot occur before reporting the same error.
func TestShareRenamerAddsNothingOffWindows(t *testing.T) {
	r := shareRenamer()
	if r.retries != 0 {
		t.Errorf("renameRetries = %d, want 0 where POSIX rename replaces open files", r.retries)
	}
	if r.blocked(errBlocked) {
		t.Error("renameBlocked() is true off Windows; it should be constantly false")
	}
}
