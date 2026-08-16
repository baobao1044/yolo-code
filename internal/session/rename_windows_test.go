//go:build windows

package session

import (
	"os"
	"syscall"
	"testing"
)

// TestRenameBlockedIncludesAccessDeniedUnlikeTheReadSide pins the deliberate
// asymmetry between this package's two Windows predicates, which is the detail
// most likely to be "tidied" into a bug later.
//
// isLockedByAnotherProcess (the read side) excludes ERROR_ACCESS_DENIED on
// purpose: a file the process genuinely may not read answers with code 5 too,
// and retrying would deliver a permanent fault late. renameBlocked (the write
// side) includes it, because a blocked replace reporting code 5 is what CI
// actually observed in internal/memory, and the temp file being renamed was
// created by this process moments ago in the same directory — so "you may not
// write here" is already ruled out by the write that succeeded.
//
// Making the two agree in either direction is a regression, so both halves are
// asserted here rather than only the surprising one.
func TestRenameBlockedIncludesAccessDeniedUnlikeTheReadSide(t *testing.T) {
	denied := &os.LinkError{Op: "rename", Old: "from", New: "to", Err: syscall.ERROR_ACCESS_DENIED}
	if !renameBlocked(denied) {
		t.Error("renameBlocked(ERROR_ACCESS_DENIED) = false; a held-open destination reports code 5")
	}
	if isLockedByAnotherProcess(syscall.ERROR_ACCESS_DENIED) {
		t.Error("isLockedByAnotherProcess(ERROR_ACCESS_DENIED) = true; the read side must not retry a real permission fault")
	}
}

// TestRenameBlockedRecognisesTheSharingErrnos covers the two codes both
// predicates agree on, through the *os.LinkError that os.Rename really
// returns — a predicate written with == rather than errors.Is would pass
// neither.
func TestRenameBlockedRecognisesTheSharingErrnos(t *testing.T) {
	for _, errno := range []syscall.Errno{errorSharingViolation, errorLockViolation} {
		wrapped := &os.LinkError{Op: "rename", Old: "from", New: "to", Err: errno}
		if !renameBlocked(wrapped) {
			t.Errorf("renameBlocked(%v) = false, want true", errno)
		}
	}
}

// TestRenameBlockedRejectsAnUnrelatedErrno pins the narrowness: a missing file
// must fail at once rather than after the full budget.
func TestRenameBlockedRejectsAnUnrelatedErrno(t *testing.T) {
	wrapped := &os.LinkError{Op: "rename", Old: "from", New: "to", Err: syscall.ERROR_FILE_NOT_FOUND}
	if renameBlocked(wrapped) {
		t.Error("renameBlocked(ERROR_FILE_NOT_FOUND) = true; waiting will not conjure a missing file")
	}
}

// TestShareRenamerIsArmedOnWindows guards against the policy constants being
// zeroed here, which would silently restore the original bug.
func TestShareRenamerIsArmedOnWindows(t *testing.T) {
	r := shareRenamer()
	if r.retries <= 0 || r.backoff <= 0 {
		t.Errorf("shareRenamer() = {retries: %d, backoff: %v}, want both > 0", r.retries, r.backoff)
	}
}
