//go:build windows

package memory

import (
	"os"
	"syscall"
	"testing"
)

// TestShareRenamerRetriesOnWindows pins the Windows half of the platform
// contract: the policy must actually be armed here, since this is the only
// platform where a rename can fail for a reason that goes away on its own.
func TestShareRenamerRetriesOnWindows(t *testing.T) {
	r := shareRenamer()
	if r.retries <= 0 {
		t.Errorf("renameRetries = %d, want > 0 where a held-open destination blocks the replace", r.retries)
	}
	if r.backoff <= 0 {
		t.Errorf("renameBackoff = %v, want > 0; retrying without pausing just burns the budget", r.backoff)
	}
}

// TestRenameBlockedRecognisesTheHeldOpenErrnos pins the mapping the whole fix
// rests on, and is the one assertion in this package that can only ever run
// here. It is written from what CI reported rather than from documentation:
// the failure that started this was os.Rename returning "Access is denied"
// (code 5) because a concurrent os.ReadFile held the destination — not the 32
// one might expect from the name "sharing violation".
//
// os.Rename wraps the errno in an *os.LinkError, so this also pins that the
// predicate looks through the wrapper. A renameBlocked written with == instead
// of errors.Is would pass no case here.
func TestRenameBlockedRecognisesTheHeldOpenErrnos(t *testing.T) {
	for _, tc := range []struct {
		name  string
		errno syscall.Errno
	}{
		{"ERROR_ACCESS_DENIED (destination held open — what CI observed)", syscall.ERROR_ACCESS_DENIED},
		{"ERROR_SHARING_VIOLATION (source held open)", errSharingViolation},
		{"ERROR_LOCK_VIOLATION (byte-range lock)", errLockViolation},
	} {
		wrapped := &os.LinkError{Op: "rename", Old: "from", New: "to", Err: tc.errno}
		if !renameBlocked(wrapped) {
			t.Errorf("renameBlocked(%s) = false, want true", tc.name)
		}
	}
}

// TestRenameBlockedRejectsAnUnrelatedErrno pins the narrowness on the platform
// where it costs something. A predicate that said yes to everything would make
// each real failure — a missing source, a bad path — take the full 250ms
// budget before reporting the identical error.
func TestRenameBlockedRejectsAnUnrelatedErrno(t *testing.T) {
	for _, errno := range []syscall.Errno{
		syscall.ERROR_FILE_NOT_FOUND,
		syscall.ERROR_PATH_NOT_FOUND,
	} {
		wrapped := &os.LinkError{Op: "rename", Old: "from", New: "to", Err: errno}
		if renameBlocked(wrapped) {
			t.Errorf("renameBlocked(%v) = true, want false — waiting will not conjure a missing file", errno)
		}
	}
}
