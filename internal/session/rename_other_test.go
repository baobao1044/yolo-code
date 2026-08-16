//go:build !windows

package session

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestRenamePolicyIsInertOffWindows pins the Unix half of the contract for
// both predicates: POSIX rename replaces a destination that readers hold open,
// so neither the write-side wait nor the read-side retry should ever engage.
// A non-zero budget here would make every real failure pause first and then
// report the same error.
func TestRenamePolicyIsInertOffWindows(t *testing.T) {
	if r := shareRenamer(); r.retries != 0 {
		t.Errorf("renameRetries = %d, want 0 where nothing can block a rename", r.retries)
	}
	// EACCES is the closest Unix analogue to the errno the Windows predicate
	// deliberately accepts; here it must mean what it says.
	denied := &os.LinkError{Op: "rename", Old: "from", New: "to", Err: syscall.EACCES}
	if renameBlocked(denied) {
		t.Error("renameBlocked() answered true off Windows; it should be constantly false")
	}
	if isLockedByAnotherProcess(errors.New("anything")) {
		t.Error("isLockedByAnotherProcess() answered true off Windows; it should be constantly false")
	}
}
