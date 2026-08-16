//go:build windows

package session

import (
	"errors"
	"syscall"
	"time"
)

// Windows lock errnos. Not named constants in syscall, so they are spelled out
// with the message the user would otherwise see verbatim.
const (
	errorSharingViolation = syscall.Errno(32) // "used by another process"
	errorLockViolation    = syscall.Errno(33) // byte-range lock held
)

// isLockedByAnotherProcess reports whether err is Windows' transient
// "somebody else has this file open" — the state a reader lands in while
// MoveFileEx is replacing the destination. See readJSON for why that is worth
// waiting out rather than reporting.
//
// Deliberately just these two. ERROR_ACCESS_DENIED also appears during some
// replace windows, but it is likewise how a genuinely unreadable file answers,
// and retrying it would turn a permanent permission fault into the same fault
// delivered late.
func isLockedByAnotherProcess(err error) bool {
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}

// The write side of the same hazard. Windows cannot replace a file that any
// handle still holds open — Go's os.Open asks for FILE_SHARE_READ |
// FILE_SHARE_WRITE and not FILE_SHARE_DELETE — so writeJSON's rename fails
// outright where readJSON merely read short. 50 × 5ms caps the wait at 250ms.
const (
	renameRetries = 50
	renameBackoff = 5 * time.Millisecond
)

// renameBlocked reports whether err is Windows refusing a replace because
// someone still has a handle, rather than refusing it on the merits.
//
// Unlike isLockedByAnotherProcess above this DOES include
// ERROR_ACCESS_DENIED, and the difference is deliberate. That comment declines
// to retry code 5 on a read because a file the process genuinely may not read
// answers the same way, and retrying would deliver a permanent fault late. On
// a replace the balance flips: code 5 is what a blocked destination actually
// reported in CI, and the temp file we are renaming from was created by this
// process moments ago in the same directory, so "you may not write here" is
// already ruled out by the write that succeeded.
func renameBlocked(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation)
}
